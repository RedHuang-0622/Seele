package seelectx

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"

	"github.com/RedHuang-0622/Seele/contexts/history"
	"github.com/RedHuang-0622/Seele/contexts/react"
	types "github.com/RedHuang-0622/Seele/types"
)

// ── chatLoop：模板方法 ──────────────────────────────────────────────

// chatLoop 是 Chat / ChatStream 的统一实现。
// strategy 决定 LLM 调用方式，其余逻辑（压缩、dispatch、缓存、循环控制）完全相同。
func (h *Holder) chatLoop(ctx context.Context, userInput string, strategy react.CompletionStrategy) (string, error) {
	if userInput != "" {
		h.history = append(h.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := h.filteredTools(h.tools.Tools())

	// ── 缓存检查 ──────────────────────────────────────────────────
	//
	// 如果缓存中有对相同输入的完整纯文本回复，直接返回，跳过 LLM 调用。
	// 缓存键 = SHA256(用户输入)。
	// 仅缓存纯文本回复（无 tool_calls 的最终结果）。
	cacheKey := h.buildChatCacheKey(userInput)
	if h.cache != nil {
		if cached, ok := h.cache.Get(cacheKey); ok {
			// 缓存命中：注入 assistant 消息到历史，直接返回
			h.history = append(h.history, types.Message{Role: "assistant", Content: &cached})
			return cached, nil
		}
	}

	for loop := 0; loop < h.cfg.MaxLoops; loop++ {
		// 上下文压缩
		cfg := h.cfg.ContextCfg
		if h.lastCompressLoop < 0 || loop-h.lastCompressLoop > 1 {
			if history.NeedCompression(h.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, h.llm, h.history, cfg.MaxTokens)
				if err != nil {
					slog.Default().Info("compression failed, using hard trim", "session_id", h.sessionID, "error", err)
					h.history = history.TrimHistory(h.history, cfg.MaxTokens)
				} else {
					h.history = compressed
				}
				h.lastCompressLoop = loop
			}
		}

		result, err := strategy.Execute(ctx, h.llm, h.history, tools)
		if err != nil {
			return "", fmt.Errorf("session[%s] loop %d: %w", h.sessionID, loop, err)
		}

		// 构建 assistant 消息（tool_call 消息通常无 content，与 OpenAI API 一致）
		assistantMsg := types.Message{
			Role:             "assistant",
			Content:          react.ContentPtr(result.Content, result.ToolCalls),
			ReasoningContent: result.ReasoningContent,
			ToolCalls:        result.ToolCalls,
		}
		h.history = append(h.history, assistantMsg)

		if len(result.ToolCalls) == 0 {
			if result.Content == "" {
				return "", fmt.Errorf("LLM returned empty content with no tool calls")
			}
			// 缓存纯文本回复（无 tool_calls 的最终结果）
			if h.cache != nil {
				h.cache.Set(cacheKey, result.Content)
			}
			return result.Content, nil
		}

		h.dispatchToolCalls(ctx, result.ToolCalls)
		tools = h.filteredTools(h.tools.Tools())
	}

	return "", fmt.Errorf("[seelectx] %s reached maxLoops (%d) without a final text reply",
		h.sessionID, h.cfg.MaxLoops)
}

// ── 公开方法 ────────────────────────────────────────────────────────

// Chat 追加 userInput 消息，驱动 LLM 推理并自动执行 tool_calls，
// 直至 LLM 返回纯文本回复或达到 maxLoops 上限。
func (h *Holder) Chat(ctx context.Context, userInput string) (string, error) {
	return h.chatLoop(ctx, userInput, &react.SyncStrategy{})
}

// ChatStream 与 Chat 行为完全一致，但最终的文本回复改为流式推送。
//
// tool_call 前的思考文本会通过 onChunk 推送给调用方（如 "我需要查一下文件..."），
// 方便前端将其渲染为独立的"思考"模块。
func (h *Holder) ChatStream(ctx context.Context, userInput string, onChunk func(delta string)) (string, error) {
	return h.chatLoop(ctx, userInput, &react.StreamStrategy{OnChunk: onChunk})
}

// ChatStreamEvents 与 ChatStream 功能相同，但通过 onEvent 传递结构化事件。
// 事件类型包括文本 delta、推理内容、工具调用、错误和完成信号，
// 方便前端渲染更丰富的流式交互体验。
func (h *Holder) ChatStreamEvents(ctx context.Context, userInput string, onEvent func(types.StreamEvent)) (string, error) {
	return h.chatLoop(ctx, userInput, &react.StreamEventStrategy{OnEvent: onEvent})
}

// buildChatCacheKey 构建聊天缓存键。
// 使用 SHA256(userInput) 作为缓存键，确保相同输入可复用结果。
// 注意：此缓存仅适用于 stateless 的纯文本回复。多轮对话中相同输入在不同上下文中
// 可能产生不同回复，调用方应按需启用缓存。
func (h *Holder) buildChatCacheKey(userInput string) string {
	if userInput == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(userInput))
	return fmt.Sprintf("chat:%x", sum)
}
