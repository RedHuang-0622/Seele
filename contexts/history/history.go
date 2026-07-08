package history

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	types "github.com/RedHuang-0622/Seele/types"
)

// ── 内部常量（不可配置）─────────────────────────────────────────────

const (
	// truncatedMarker 追加到被截断结果的末尾标记。
	truncatedMarker = "\n...[truncated]"
)

// ── Token 估算 ─────────────────────────────────────────────────────

// EstimateTokens 用字节数估算 token 数。
// 保守公式：len(text)/3。中文 UTF-8 每个字约 3 字节 ≈ 1-2 token；
// 英文每个字符 1 字节 ≈ 0.25 token。取 /3 对两类场景都偏保守（高估 token 数），
// 确保我们不会超过 LLM 的上下文窗口。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 2) / 3
}

// EstimateMessageTokens 估算单条 Message 的 token 开销。
// 包含 role 字段的固定开销（约 10 token）和内容 token。
func EstimateMessageTokens(msg types.Message) int {
	n := 10 // role + JSON 结构开销
	if msg.Content != nil {
		n += EstimateTokens(*msg.Content)
	}
	if msg.ReasoningContent != "" {
		n += EstimateTokens(msg.ReasoningContent)
	}
	for _, tc := range msg.ToolCalls {
		n += EstimateTokens(tc.Function.Name)
		n += EstimateTokens(tc.Function.Arguments)
	}
	if msg.Name != "" {
		n += EstimateTokens(msg.Name)
	}
	if msg.ToolCallID != "" {
		n += EstimateTokens(msg.ToolCallID)
	}
	return n
}

// EstimateHistoryTokens 估算全部历史消息的总 token 数。
func EstimateHistoryTokens(msgs []types.Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateMessageTokens(m)
	}
	return total
}

// ── Tool 结果截断 ──────────────────────────────────────────────────

// TruncateToolResult 将 tool 返回内容截断到安全长度。
func TruncateToolResult(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	maxBody := maxChars - len(truncatedMarker)
	if maxBody <= 0 {
		return truncatedMarker
	}
	cut := content[:maxBody]
	if idx := strings.LastIndex(cut, "\n"); idx > maxBody/2 {
		cut = cut[:idx]
	}
	return cut + truncatedMarker
}

// ── 硬截断（保底策略）──────────────────────────────────────────────

// TrimHistory 硬截断消息历史以适应 maxTokens 限制。
func TrimHistory(msgs []types.Message, maxTokens int) []types.Message {
	if maxTokens <= 0 {
		maxTokens = DefaultConfig().MaxTokens
	}
	var sys []types.Message
	var rest []types.Message
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m)
		} else {
			rest = append(rest, m)
		}
	}
	for len(rest) > 0 {
		total := EstimateHistoryTokens(sys) + EstimateHistoryTokens(rest)
		if total <= maxTokens {
			break
		}
		rest = rest[1:]
	}
	rest = stripLeadingOrphanTools(rest)
	result := append(sys, rest...)
	if EstimateHistoryTokens(result) > maxTokens && len(result) > 0 {
		for i := range result {
			if result[i].Content != nil && len(*result[i].Content) > 500 {
				s := TruncateToolResult(*result[i].Content, DefaultConfig().MaxToolResultChars)
				result[i].Content = &s
				break
			}
		}
	}
	return result
}

func stripLeadingOrphanTools(msgs []types.Message) []types.Message {
	for len(msgs) > 0 && msgs[0].Role == "tool" {
		msgs = msgs[1:]
	}
	return msgs
}

// NeedCompression 判断历史消息是否需要压缩。
func NeedCompression(msgs []types.Message, threshold int) bool {
	return EstimateHistoryTokens(msgs) > threshold
}

// ── LLM 压缩 ───────────────────────────────────────────────────────

const keepRecent = 4

const compressSystemPrompt = `Summarize the following execution history.
Focus on: key findings, errors encountered, actions taken, and final outcomes.
Be extremely concise — output a short paragraph under 150 words.
Do NOT re-execute any tools. Only summarize what already happened.`

// CompressHistory 用 LLM 将早期 tool 执行记录压缩为简短摘要。
func CompressHistory(ctx context.Context, client types.ChatCompleter, history []types.Message, maxTokens int) ([]types.Message, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultConfig().MaxTokens
	}
	sys, rest := splitSystem(history)
	if len(rest) <= keepRecent {
		return TrimHistory(history, maxTokens), nil
	}
	keep := rest[len(rest)-keepRecent:]
	compressible := rest[:len(rest)-keepRecent]
	for len(keep) > 0 && keep[0].Role == "tool" && len(compressible) > 0 {
		last := compressible[len(compressible)-1]
		compressible = compressible[:len(compressible)-1]
		keep = append([]types.Message{last}, keep...)
	}
	if len(compressible) == 0 {
		return TrimHistory(history, maxTokens), nil
	}
	compressInput := buildCompressInput(compressible)
	summary, err := callCompressLLM(ctx, client, compressInput)
	if err != nil {
		slog.Default().Warn("compression LLM call failed, falling back to hard trim", "error", err)
		return TrimHistory(history, maxTokens), nil
	}
	compressed := make([]types.Message, 0, len(sys)+1+len(keep))
	compressed = append(compressed, sys...)
	summaryText := "[Context summary of earlier execution: " + summary + "]"
	compressed = append(compressed, types.Message{
		Role:    "system",
		Content: &summaryText,
	})
	compressed = append(compressed, keep...)
	if EstimateHistoryTokens(compressed) > maxTokens {
		slog.Default().Warn("compression still over budget, applying hard trim")
		return TrimHistory(compressed, maxTokens), nil
	}
	beforeTokens := EstimateHistoryTokens(history)
	afterTokens := EstimateHistoryTokens(compressed)
	slog.Default().Info("compressed history",
		"before_tokens", beforeTokens,
		"after_tokens", afterTokens,
		"saved_tokens", beforeTokens-afterTokens)
	return compressed, nil
}

// ── 内部辅助 ───────────────────────────────────────────────────────

func splitSystem(msgs []types.Message) (sys, rest []types.Message) {
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m)
		} else {
			rest = append(rest, m)
		}
	}
	return
}

func buildCompressInput(msgs []types.Message) string {
	var b []byte
	for _, m := range msgs {
		switch m.Role {
		case "user":
			if m.Content != nil {
				b = append(b, "User: "+*m.Content+"\n"...)
			}
		case "assistant":
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					b = append(b, fmt.Sprintf("Called %s(%s)\n", tc.Function.Name, tc.Function.Arguments)...)
				}
			} else if m.Content != nil {
				b = append(b, "Assistant: "+*m.Content+"\n"...)
			}
		case "tool":
			if m.Content != nil {
				content := *m.Content
				if len(content) > 800 {
					content = content[:800] + "..."
				}
				b = append(b, fmt.Sprintf("Result from %s: %s\n", m.Name, content)...)
			}
		}
	}
	return string(b)
}

func callCompressLLM(ctx context.Context, client types.ChatCompleter, input string) (string, error) {
	if input == "" {
		return "no previous context to summarize", nil
	}
	messages := []types.Message{
		{Role: "system", Content: strPtr(compressSystemPrompt)},
		{Role: "user", Content: &input},
	}
	msg, err := client.Complete(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	if msg.Content == nil {
		return "", fmt.Errorf("compression LLM returned nil content")
	}
	return *msg.Content, nil
}

func strPtr(s string) *string { return &s }
