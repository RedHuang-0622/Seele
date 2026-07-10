package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	seelectx "github.com/RedHuang-0622/Seele/contexts"
	"github.com/RedHuang-0622/Seele/contexts/cache"
	"github.com/RedHuang-0622/Seele/contexts/storage"
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

// Loop 是可替换的 LLM 循环策略接口。
//
// Run 执行一次完整的 LLM 循环；History 返回当前对话历史的只读副本；
// ClearHistory 清空对话历史但保留 system 消息。
type Loop interface {
	Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
	History() []types.Message
	ClearHistory()
}

// ReActLoop 实现 Loop 接口，执行完整的 ReAct 循环。
//
// 持有所有会话状态：对话历史、配置、缓存、存储、追踪器。
// 通过新 ReActLoop 创建，支持功能选项模式配置。
type ReActLoop struct {
	agent     *agent.Agent
	llm       types.ChatCompleter
	history   []types.Message
	cfg       SessionConfig
	sessionID string
	cache     cache.Provider
	store     *storage.Store
	modelName string
	tracer    tracer.Tracer
}

// ReActLoopOption 配置 ReActLoop 的创建参数。
type ReActLoopOption func(*ReActLoop)

// NewReActLoop 创建 ReActLoop。
//
//	rl := NewReActLoop(agt, agt.LLM(),
//	    WithSessionID("sess_xxx"),
//	    WithTracer(tracer.NewSimpleTracer()),
//	)
func NewReActLoop(a *agent.Agent, llm types.ChatCompleter, opts ...ReActLoopOption) *ReActLoop {
	rl := &ReActLoop{
		agent:     a,
		llm:       llm,
		history:   make([]types.Message, 0),
		cfg:       DefaultSessionConfig(),
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		tracer:    &tracer.NoopTracer{},
	}
	for _, opt := range opts {
		opt(rl)
	}
	rl.cfg = rl.cfg.Effective()
	return rl
}

// ── ReActLoopOption 函数 ────────────────────────────────────────────────

// WithMaxLoops 设置最大 tool_call 循环次数。默认 10。
func WithMaxLoops(n int) ReActLoopOption {
	return func(rl *ReActLoop) { rl.cfg.MaxLoops = n }
}

// WithSessionID 设置会话 ID（缓存键）。自动生成时使用时间戳前缀。
func WithSessionID(id string) ReActLoopOption {
	return func(rl *ReActLoop) { rl.sessionID = id }
}

// WithModelName 设置模型名称（供 tracer 使用）。
func WithModelName(name string) ReActLoopOption {
	return func(rl *ReActLoop) { rl.modelName = name }
}

// ── Loop 接口实现 ───────────────────────────────────────────────────────

// Run 执行一次完整的 ReAct 循环。
//
// 循环流程：
//  0. 检查缓存（如有）：sessionID -> 从 cache.Provider 恢复历史
//  1. 追加 user 消息到 history
//  2. 通过 Agent.VisibleTools 获取当前可见工具列表
//  3. 调用 LLM（同步或流式）
//  4. 若 LLM 返回纯文本 -> 追加 assistant 消息，返回文本
//  5. 若 LLM 返回 tool_calls -> 逐条 dispatch，结果截断后注入 history，回到步骤 2
//  6. 达到 MaxLoops 仍未获得纯文本回复 -> 返回错误
//
// 可观测性：
//  每次 Run 执行生成一个 Trace Tree，包含：
//  - root span: 整个 ReAct 循环
//  - llm_call span: 每次 LLM 调用（含 token 计数、模型名、工具数）
//  - tool_dispatch span: 每次工具调度（含工具名、参数摘要、结果长度）
func (rl *ReActLoop) Run(ctx context.Context, userInput string, onChunk func(string)) (result string, err error) {
	defer rl.saveToCache()

	// ── Trace: root span ──────────────────────────────────────────────────
	ctx, rootSpan := rl.tracer.NewTrace(ctx, rl.sessionID)
	rootSpan.SetAttr("user_input", tracer.Truncate(userInput, 500))
	if rl.modelName != "" {
		rootSpan.SetAttr("model", rl.modelName)
	}
	defer func() {
		if err != nil {
			rootSpan.End(tracer.WithError(err))
		} else {
			rootSpan.End()
		}
	}()

	// 步骤 0：从缓存恢复历史（TTL + 置信度）
	rl.restoreFromCache()

	rl.history = append(rl.history, types.Message{
		Role:    "user",
		Content: &userInput,
	})

	// 压缩过长的历史，避免超出 LLM 上下文窗口
	if seelectx.EstimateHistoryTokens(rl.history) > 6144 {
		compressed, err := seelectx.CompressHistory(ctx, rl.llm, rl.history, 8192)
		if err == nil {
			rl.history = compressed
		}
	}

	// 保存 root context（子 span 统一用 root 作为父，保证 sibling 关系）
	rootCtx := ctx

	for loop := 0; loop < rl.cfg.MaxLoops; loop++ {
		tools := rl.agent.VisibleTools(ctx)

		// ── Trace: LLM call span ─────────────────────────────────────────
		_, llmSpan := rl.tracer.StartSpan(rootCtx,
			fmt.Sprintf("LLM Call #%d", loop+1),
			tracer.SpanLLMCall,
			map[string]string{
				"model":       rl.modelName,
				"tools_count": fmt.Sprint(len(tools)),
				"history_len": fmt.Sprint(len(rl.history)),
			})

		assistantMsg, callErr := rl.callLLM(ctx, tools, onChunk)
		if callErr != nil {
			llmSpan.End(tracer.WithError(callErr))
			return "", fmt.Errorf("engine loop %d: %w", loop, callErr)
		}
		rl.history = append(rl.history, assistantMsg)

		// ── Token 用量 ──────────────────────────────────────────────────
		if assistantMsg.Usage != nil {
			llmSpan.SetAttr("input_tokens", fmt.Sprint(assistantMsg.Usage.PromptTokens))
			llmSpan.SetAttr("output_tokens", fmt.Sprint(assistantMsg.Usage.CompletionTokens))
			llmSpan.SetAttr("total_tokens", fmt.Sprint(assistantMsg.Usage.TotalTokens))
		}

		// 纯文本回复 -> 完成
		if len(assistantMsg.ToolCalls) == 0 {
			if assistantMsg.Content == nil || *assistantMsg.Content == "" {
				llmSpan.End(tracer.WithAttr("response_type", "empty"))
				return "", fmt.Errorf("engine loop %d: LLM returned empty content with no tool calls", loop)
			}
			llmSpan.SetAttr("response_type", "text")
			llmSpan.End()
			return *assistantMsg.Content, nil
		}

		llmSpan.SetAttr("response_type", "tool_calls")
		llmSpan.SetAttr("tool_count", fmt.Sprint(len(assistantMsg.ToolCalls)))
		llmSpan.End()

		// ── Trace: tool dispatch spans ───────────────────────────────────
		for _, tc := range assistantMsg.ToolCalls {
			toolAttrs := map[string]string{"tool": tc.Function.Name}
			if len(tc.Function.Arguments) > 200 {
				toolAttrs["arguments"] = tc.Function.Arguments[:200] + "..."
			} else {
				toolAttrs["arguments"] = tc.Function.Arguments
			}
			_, toolSpan := rl.tracer.StartSpan(rootCtx,
				tc.Function.Name,
				tracer.SpanToolDispatch,
				toolAttrs)

			out, dErr := rl.agent.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			if dErr != nil {
				out = fmt.Sprintf(`{"error": %q}`, dErr.Error())
				toolSpan.End(tracer.WithError(dErr))
			} else {
				toolSpan.SetAttr("result_length", fmt.Sprint(len(out)))
				toolSpan.End()
			}

			content := truncateResult(out, rl.cfg.MaxToolResultChars)
			rl.history = append(rl.history, types.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    &content,
			})
		}
	}

	return "", fmt.Errorf("engine: reached maxLoops (%d) without final text reply", rl.cfg.MaxLoops)
}

// History 返回当前对话历史的只读副本。
func (rl *ReActLoop) History() []types.Message {
	cp := make([]types.Message, len(rl.history))
	copy(cp, rl.history)
	return cp
}

// ClearHistory 清空对话历史，但保留 system 消息（如有）。
func (rl *ReActLoop) ClearHistory() {
	var sys []types.Message
	for _, m := range rl.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	rl.history = sys
}

// ── 内部方法 ────────────────────────────────────────────────────────────

// callLLM 调用 LLM，onChunk 非 nil 时使用流式模式。
func (rl *ReActLoop) callLLM(ctx context.Context, tools []types.Tool, onChunk func(string)) (types.Message, error) {
	if onChunk != nil {
		content, _, toolCalls, err := rl.llm.CompleteStream(ctx, rl.history, tools, onChunk)
		if err != nil {
			return types.Message{}, err
		}
		if len(toolCalls) > 0 {
			return types.Message{
				Role:      "assistant",
				Content:   nil,
				ToolCalls: toolCalls,
			}, nil
		}
		return types.Message{
			Role:    "assistant",
			Content: &content,
		}, nil
	}

	msg, err := rl.llm.Complete(ctx, rl.history, tools)
	if err != nil {
		return types.Message{}, err
	}
	return msg, nil
}

// restoreFromCache 尝试从缓存或持久化存储恢复对话历史。
// 优先从缓存读取（TTL + 置信度），未命中时尝试从存储恢复。
func (rl *ReActLoop) restoreFromCache() {
	if rl.sessionID == "" {
		return
	}
	// 1. 尝试从缓存恢复（TTL + 置信度）
	if rl.cache != nil {
		val, ok := rl.cache.Get(rl.sessionID)
		if ok && val != "" {
			var cached []types.Message
			if err := json.Unmarshal([]byte(val), &cached); err == nil && len(cached) > 0 {
				rl.history = cached
				return
			}
		}
	}
	// 2. 缓存未命中，尝试从持久化存储恢复
	if rl.store != nil {
		stored, err := rl.store.Load(rl.sessionID)
		if err == nil && len(stored) > 0 {
			rl.history = stored
		}
	}
}

// saveToCache 将当前对话历史存入缓存和持久化存储。
func (rl *ReActLoop) saveToCache() {
	if rl.sessionID == "" || len(rl.history) == 0 {
		return
	}
	// 写入缓存
	if rl.cache != nil {
		data, err := json.Marshal(rl.history)
		if err == nil {
			rl.cache.SetWithTTL(rl.sessionID, string(data), 5*time.Minute)
		}
	}
	// 写入持久化存储
	if rl.store != nil {
		_ = rl.store.Save(rl.sessionID, rl.history)
	}
}

// truncateResult 截断工具结果到指定最大字符数。
func truncateResult(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n...[truncated]"
}
