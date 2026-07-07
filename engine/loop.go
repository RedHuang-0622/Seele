package engine

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/types"
)

// chatLoop 是 Chat / ChatStream 的统一 ReAct 循环实现。
//
// 循环流程：
//  1. 追加 user 消息到 history
//  2. 通过 Agent.VisibleTools 获取当前可见工具列表
//  3. 调用 LLM（同步或流式）
//  4. 若 LLM 返回纯文本 → 追加 assistant 消息，返回文本
//  5. 若 LLM 返回 tool_calls → 逐条 dispatch，结果截断后注入 history，回到步骤 2
//  6. 达到 MaxLoops 仍未获得纯文本回复 → 返回错误
func (e *Engine) chatLoop(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	e.history = append(e.history, types.Message{
		Role:    "user",
		Content: &userInput,
	})

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
	return e.chatLoop(ctx, userInput, nil)
}

// ChatStream 追加用户输入并运行流式 ReAct 循环。
// 文本 token 到达时通过 onChunk 实时推送；tool_call 阶段不会触发 onChunk。
func (e *Engine) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	return e.chatLoop(ctx, userInput, onChunk)
}
