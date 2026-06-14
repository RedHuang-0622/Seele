package session

import (
	"context"
	"fmt"
	"log"

	history "github.com/RedHuang-0622/Seele/history"
	types "github.com/RedHuang-0622/Seele/types"
)

// Chat 追加 userInput 消息，驱动 LLM 推理并自动执行 tool_calls，
// 直至 LLM 返回纯文本回复或达到 maxLoops 上限。
func (h *Holder) Chat(ctx context.Context, userInput string) (string, error) {
	if userInput != "" {
		h.history = append(h.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := h.filteredTools(h.tools.Tools())

	for loop := 0; loop < h.maxLoops; loop++ {
		cfg := h.contextCfg
		if h.lastCompressLoop < 0 || loop-h.lastCompressLoop > 1 {
			if history.NeedCompression(h.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, h.llm, h.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[session.Chat] %s compression failed: %v, using hard trim", h.sessionID, err)
					h.history = history.TrimHistory(h.history, cfg.MaxTokens)
				} else {
					h.history = compressed
				}
				h.lastCompressLoop = loop
			}
		}

		msg, err := h.llm.Complete(ctx, h.history, tools)
		if err != nil {
			return "", fmt.Errorf("session[%s] chat loop %d: %w", h.sessionID, loop, err)
		}

		h.history = append(h.history, types.Message{
			Role:             "assistant",
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        msg.ToolCalls,
		})

		if len(msg.ToolCalls) == 0 {
			if msg.Content == nil {
				return "", fmt.Errorf("LLM returned empty content with no tool calls")
			}
			return *msg.Content, nil
		}

		h.dispatchToolCalls(ctx, msg.ToolCalls)
		tools = h.filteredTools(h.tools.Tools())
	}

	return "", fmt.Errorf("[session.Chat] %s reached maxLoops (%d) without a final text reply",
		h.sessionID, h.maxLoops)
}

// ChatStream 与 Chat 行为完全一致，但最终的文本回复改为流式推送。
//
// tool_call 前的思考文本会通过 onChunk 推送给调用方（如 "我需要查一下文件..."），
// 方便前端将其渲染为独立的"思考"模块。
func (h *Holder) ChatStream(ctx context.Context, userInput string, onChunk func(delta string)) (string, error) {
	if userInput != "" {
		h.history = append(h.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := h.filteredTools(h.tools.Tools())

	for loop := 0; loop < h.maxLoops; loop++ {
		cfg := h.contextCfg
		if h.lastCompressLoop < 0 || loop-h.lastCompressLoop > 1 {
			if history.NeedCompression(h.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, h.llm, h.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[session.ChatStream] %s compression failed: %v, using hard trim", h.sessionID, err)
					h.history = history.TrimHistory(h.history, cfg.MaxTokens)
				} else {
					h.history = compressed
				}
				h.lastCompressLoop = loop
			}
		}

		var chunks []string
		fullContent, reasoningContent, toolCalls, err := h.llm.CompleteStream(
			ctx, h.history, tools,
			func(delta string) { chunks = append(chunks, delta) },
		)
		if err != nil {
			return "", fmt.Errorf("[session.ChatStream] %s stream loop %d: %w", h.sessionID, loop, err)
		}

		if len(toolCalls) == 0 {
			for _, c := range chunks {
				onChunk(c)
			}
			h.history = append(h.history, types.Message{
				Role:             "assistant",
				Content:          &fullContent,
				ReasoningContent: reasoningContent,
			})
			return fullContent, nil
		}

		// 推送 tool_call 前的思考文本，方便前端渲染
		for _, c := range chunks {
			onChunk(c)
		}

		h.history = append(h.history, types.Message{
			Role:             "assistant",
			Content:          &fullContent,
			ReasoningContent: reasoningContent,
			ToolCalls:        toolCalls,
		})

		h.dispatchToolCalls(ctx, toolCalls)
		tools = h.filteredTools(h.tools.Tools())
	}

	return "", fmt.Errorf("[session.ChatStream] %s reached maxLoops (%d) without a final text reply",
		h.sessionID, h.maxLoops)
}
