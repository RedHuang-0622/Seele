package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	seelectx "github.com/RedHuang-0622/Seele/contexts"
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

// chatLoop 是 Chat / ChatStream 的统一 ReAct 循环实现。
//
// 循环流程：
//  0. 检查缓存（如有）：sessionID → 从 cache.Provider 恢复历史
//  1. 追加 user 消息到 history
//  2. 通过 Agent.VisibleTools 获取当前可见工具列表
//  3. 调用 LLM（同步或流式）
//  4. 若 LLM 返回纯文本 → 追加 assistant 消息，返回文本
//  5. 若 LLM 返回 tool_calls → 逐条 dispatch，结果截断后注入 history，回到步骤 2
//  6. 达到 MaxLoops 仍未获得纯文本回复 → 返回错误
//
// 可观测性：
//  每次 chatLoop 执行生成一个 Trace Tree，包含：
//  - root span: 整个 ReAct 循环
//  - llm_call span: 每次 LLM 调用（含 token 计数、模型名、工具数）
//  - tool_dispatch span: 每次工具调度（含工具名、参数摘要、结果长度）
func (e *Engine) chatLoop(ctx context.Context, userInput string, onChunk func(string)) (result string, err error) {
	// ── Trace: root span ──────────────────────────────────────────────────
	ctx, rootSpan := e.tracer.NewTrace(ctx, e.sessionID)
	rootSpan.SetAttr("user_input", tracer.Truncate(userInput, 500))
	if e.modelName != "" {
		rootSpan.SetAttr("model", e.modelName)
	}
	defer func() {
		if err != nil {
			rootSpan.End(tracer.WithError(err))
		} else {
			rootSpan.End()
		}
	}()

	// 步骤 0：从缓存恢复历史（TTL + 置信度）
	e.restoreFromCache()

	e.history = append(e.history, types.Message{
		Role:    "user",
		Content: &userInput,
	})

	// 压缩过长的历史，避免超出 LLM 上下文窗口
	if seelectx.EstimateHistoryTokens(e.history) > 6144 {
		compressed, err := seelectx.CompressHistory(ctx, e.llm, e.history, 8192)
		if err == nil {
			e.history = compressed
		}
	}

	// 保存 root context（子 span 统一用 root 作为父，保证 sibling 关系）
	rootCtx := ctx

	for loop := 0; loop < e.cfg.MaxLoops; loop++ {
		tools := e.agent.VisibleTools(ctx)

		// ── Trace: LLM call span ─────────────────────────────────────────
		_, llmSpan := e.tracer.StartSpan(rootCtx,
			fmt.Sprintf("LLM Call #%d", loop+1),
			tracer.SpanLLMCall,
			map[string]string{
				"model":       e.modelName,
				"tools_count": fmt.Sprint(len(tools)),
				"history_len": fmt.Sprint(len(e.history)),
			})

		assistantMsg, callErr := e.callLLM(ctx, tools, onChunk)
		if callErr != nil {
			llmSpan.End(tracer.WithError(callErr))
			return "", fmt.Errorf("engine loop %d: %w", loop, callErr)
		}
		e.history = append(e.history, assistantMsg)

		// ── Token 用量 ─────────────────────────────────────────────────
		if assistantMsg.Usage != nil {
			llmSpan.SetAttr("input_tokens", fmt.Sprint(assistantMsg.Usage.PromptTokens))
			llmSpan.SetAttr("output_tokens", fmt.Sprint(assistantMsg.Usage.CompletionTokens))
			llmSpan.SetAttr("total_tokens", fmt.Sprint(assistantMsg.Usage.TotalTokens))
		}

		// 纯文本回复 → 完成
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

		// ── Trace: tool dispatch spans ──────────────────────────────────
		for _, tc := range assistantMsg.ToolCalls {
			toolAttrs := map[string]string{"tool": tc.Function.Name}
			if len(tc.Function.Arguments) > 200 {
				toolAttrs["arguments"] = tc.Function.Arguments[:200] + "..."
			} else {
				toolAttrs["arguments"] = tc.Function.Arguments
			}
			_, toolSpan := e.tracer.StartSpan(rootCtx,
				tc.Function.Name,
				tracer.SpanToolDispatch,
				toolAttrs)

			out, dErr := e.agent.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			if dErr != nil {
				out = fmt.Sprintf(`{"error": %q}`, dErr.Error())
				toolSpan.End(tracer.WithError(dErr))
			} else {
				toolSpan.SetAttr("result_length", fmt.Sprint(len(out)))
				toolSpan.End()
			}

			content := truncateResult(out, e.cfg.MaxToolResultChars)
			e.history = append(e.history, types.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    &content,
			})
		}
	}

	return "", fmt.Errorf("engine: reached maxLoops (%d) without final text reply", e.cfg.MaxLoops)
}

// callLLM 调用 LLM，onChunk 非 nil 时使用流式模式。
func (e *Engine) callLLM(ctx context.Context, tools []types.Tool, onChunk func(string)) (types.Message, error) {
	if onChunk != nil {
		content, _, toolCalls, err := e.llm.CompleteStream(ctx, e.history, tools, onChunk)
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

	msg, err := e.llm.Complete(ctx, e.history, tools)
	if err != nil {
		return types.Message{}, err
	}
	return msg, nil
}

// truncateResult 截断工具结果到指定最大字符数。
func truncateResult(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n...[truncated]"
}

// ── 公开方法 ──────────────────────────────────────────────────────────

// Chat 追加用户输入并运行 ReAct 循环，返回最终文本回复。
// 结束后可通过 ExportTrace() 获取本次执行的 Trace Tree。
func (e *Engine) Chat(ctx context.Context, userInput string) (string, error) {
	reply, err := e.chatLoop(ctx, userInput, nil)
	e.lastTrace = e.tracer.Export(ctx)
	e.saveToCache()
	return reply, err
}

// ChatStream 追加用户输入并运行流式 ReAct 循环。
// 文本 token 到达时通过 onChunk 实时推送；tool_call 阶段不会触发 onChunk。
// 结束后可通过 ExportTrace() 获取本次执行的 Trace Tree。
func (e *Engine) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	reply, err := e.chatLoop(ctx, userInput, onChunk)
	e.lastTrace = e.tracer.Export(ctx)
	e.saveToCache()
	return reply, err
}

// restoreFromCache 尝试从缓存或持久化存储恢复对话历史。
// 优先从缓存读取（TTL + 置信度），未命中时尝试从存储恢复。
func (e *Engine) restoreFromCache() {
	if e.sessionID == "" {
		return
	}
	// 1. 尝试从缓存恢复（TTL + 置信度）
	if e.cache != nil {
		val, ok := e.cache.Get(e.sessionID)
		if ok && val != "" {
			var cached []types.Message
			if err := json.Unmarshal([]byte(val), &cached); err == nil && len(cached) > 0 {
				e.history = cached
				return
			}
		}
	}
	// 2. 缓存未命中，尝试从持久化存储恢复
	if e.store != nil {
		stored, err := e.store.Load(e.sessionID)
		if err == nil && len(stored) > 0 {
			e.history = stored
		}
	}
}

// saveToCache 将当前对话历史存入缓存和持久化存储。
func (e *Engine) saveToCache() {
	if e.sessionID == "" || len(e.history) == 0 {
		return
	}
	// 写入缓存
	if e.cache != nil {
		data, err := json.Marshal(e.history)
		if err == nil {
			e.cache.SetWithTTL(e.sessionID, string(data), 5*time.Minute)
		}
	}
	// 写入持久化存储
	if e.store != nil {
		_ = e.store.Save(e.sessionID, e.history)
	}
}

