package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	seelectx "github.com/RedHuang-0622/Seele/contexts"
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
func (e *Engine) chatLoop(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
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

	for loop := 0; loop < e.cfg.MaxLoops; loop++ {
		tools := e.agent.VisibleTools(ctx)

		// ── LLM 调用 ──────────────────────────────────────────────
		assistantMsg, err := e.callLLM(ctx, tools, onChunk)
		if err != nil {
			return "", fmt.Errorf("engine loop %d: %w", loop, err)
		}
		e.history = append(e.history, assistantMsg)

		// 纯文本回复 → 完成
		if len(assistantMsg.ToolCalls) == 0 {
			if assistantMsg.Content == nil || *assistantMsg.Content == "" {
				return "", fmt.Errorf("engine loop %d: LLM returned empty content with no tool calls", loop)
			}
			return *assistantMsg.Content, nil
		}

		// ── 工具调度 ──────────────────────────────────────────────
		for _, tc := range assistantMsg.ToolCalls {
			out, dErr := e.agent.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			if dErr != nil {
				out = fmt.Sprintf(`{"error": %q}`, dErr.Error())
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
func (e *Engine) Chat(ctx context.Context, userInput string) (string, error) {
	reply, err := e.chatLoop(ctx, userInput, nil)
	e.saveToCache()
	return reply, err
}

// ChatStream 追加用户输入并运行流式 ReAct 循环。
// 文本 token 到达时通过 onChunk 实时推送；tool_call 阶段不会触发 onChunk。
func (e *Engine) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	reply, err := e.chatLoop(ctx, userInput, onChunk)
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
