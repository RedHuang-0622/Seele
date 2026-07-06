package seelectx

import (
	"context"
	"fmt"
	"log"

	types "github.com/RedHuang-0622/Seele/types"
)

// ── 策略模式：LLM 调用方式 ─────────────────────────────────────────
//
// chatLoop 是模板方法，不感知底层是同步还是流式。
// completionStrategy 封装调用差异，两种实现：
//
//	syncStrategy   → Complete()
//	streamStrategy → CompleteStream() + chunk 回放

// completionStrategy 封装一次 LLM 调用的完整流程。
type completionStrategy interface {
	execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*completionResult, error)
}

type completionResult struct {
	content          string
	reasoningContent string
	toolCalls        []types.ToolCall
}

// syncStrategy：同步 LLM 调用。
type syncStrategy struct{}

func (s *syncStrategy) execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*completionResult, error) {
	msg, err := client.Complete(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	return &completionResult{
		content:          content,
		reasoningContent: msg.ReasoningContent,
		toolCalls:        msg.ToolCalls,
	}, nil
}

// streamStrategy：SSE 流式 LLM 调用。
// 收集所有 chunk 后在 execute 内部回放，保证 chatLoop 拿到的是完整结果。
type streamStrategy struct {
	onChunk func(delta string)
}

func (s *streamStrategy) execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*completionResult, error) {
	var chunks []string
	fullContent, reasoningContent, toolCalls, err := client.CompleteStream(
		ctx, messages, tools,
		func(delta string) { chunks = append(chunks, delta) },
	)
	if err != nil {
		return nil, err
	}
	// 回放 chunk（思考文本），无论最终是 tool_call 还是纯文本
	for _, c := range chunks {
		s.onChunk(c)
	}
	return &completionResult{
		content:          fullContent,
		reasoningContent: reasoningContent,
		toolCalls:        toolCalls,
	}, nil
}

// ── chatLoop：模板方法 ──────────────────────────────────────────────

// chatLoop 是 Chat / ChatStream 的统一实现。
// strategy 决定 LLM 调用方式，其余逻辑（压缩、dispatch、循环控制）完全相同。
func (h *Holder) chatLoop(ctx context.Context, userInput string, strategy completionStrategy) (string, error) {
	if userInput != "" {
		h.history = append(h.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := h.filteredTools(h.tools.Tools())

	for loop := 0; loop < h.cfg.MaxLoops; loop++ {
		// 上下文压缩
		cfg := h.cfg.ContextCfg
		if h.lastCompressLoop < 0 || loop-h.lastCompressLoop > 1 {
			if NeedCompression(h.history, cfg.CompressThreshold) {
				compressed, err := CompressHistory(ctx, h.llm, h.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[seelectx.chatLoop] %s compression failed: %v, using hard trim", h.sessionID, err)
					h.history = TrimHistory(h.history, cfg.MaxTokens)
				} else {
					h.history = compressed
				}
				h.lastCompressLoop = loop
			}
		}

		result, err := strategy.execute(ctx, h.llm, h.history, tools)
		if err != nil {
			return "", fmt.Errorf("session[%s] loop %d: %w", h.sessionID, loop, err)
		}

		// 构建 assistant 消息（tool_call 消息通常无 content，与 OpenAI API 一致）
		assistantMsg := types.Message{
			Role:             "assistant",
			Content:          contentPtr(result.content, result.toolCalls),
			ReasoningContent: result.reasoningContent,
			ToolCalls:        result.toolCalls,
		}
		h.history = append(h.history, assistantMsg)

		if len(result.toolCalls) == 0 {
			if result.content == "" {
				return "", fmt.Errorf("LLM returned empty content with no tool calls")
			}
			return result.content, nil
		}

		h.dispatchToolCalls(ctx, result.toolCalls)
		tools = h.filteredTools(h.tools.Tools())
	}

	return "", fmt.Errorf("[seelectx] %s reached maxLoops (%d) without a final text reply",
		h.sessionID, h.cfg.MaxLoops)
}

// contentPtr 返回 content 的指针。
// 当 content 为空且存在 toolCalls 时返回 nil，与 OpenAI API 行为一致。
func contentPtr(content string, toolCalls []types.ToolCall) *string {
	if content == "" && len(toolCalls) > 0 {
		return nil
	}
	return &content
}

// ── 公开方法 ────────────────────────────────────────────────────────

// Chat 追加 userInput 消息，驱动 LLM 推理并自动执行 tool_calls，
// 直至 LLM 返回纯文本回复或达到 maxLoops 上限。
func (h *Holder) Chat(ctx context.Context, userInput string) (string, error) {
	return h.chatLoop(ctx, userInput, &syncStrategy{})
}

// ChatStream 与 Chat 行为完全一致，但最终的文本回复改为流式推送。
//
// tool_call 前的思考文本会通过 onChunk 推送给调用方（如 "我需要查一下文件..."），
// 方便前端将其渲染为独立的"思考"模块。
func (h *Holder) ChatStream(ctx context.Context, userInput string, onChunk func(delta string)) (string, error) {
	return h.chatLoop(ctx, userInput, &streamStrategy{onChunk: onChunk})
}
