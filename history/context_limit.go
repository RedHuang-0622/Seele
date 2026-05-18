package history

import (
	"strings"

	types "github.com/sukasukasuka123/Seele/types"
)

// ── 上下文预算常量 ─────────────────────────────────────────────────

const (
	// MaxContextTokens 硬上限：单次 LLM 请求的最大上下文 token 数。
	MaxContextTokens = 2048

	// CompressThreshold 压缩触发阈值：history token 数超过此值时触发压缩。
	CompressThreshold = 1536 // 75% of MaxContextTokens

	// MaxToolResultChars 单个 tool 结果追加到 history 前的最大字符数。
	// 超过此长度则截断，末尾追加 "[truncated]" 标记让 LLM 感知到裁剪。
	MaxToolResultChars = 2000

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
	// tool_calls 的开销（函数名 + 参数 JSON）
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
// 若 content 较短则原样返回；否则截断并追加 "[truncated]" 标记。
// 截断点在 MaxToolResultChars 处，尽量在换行符处断开以保持可读性。
func TruncateToolResult(content string) string {
	if len(content) <= MaxToolResultChars {
		return content
	}
	cut := content[:MaxToolResultChars]
	// 尽量在最后一个换行符处截断
	if idx := strings.LastIndex(cut, "\n"); idx > MaxToolResultChars/2 {
		cut = cut[:idx]
	}
	return cut + truncatedMarker
}

// ── 硬截断（保底策略）──────────────────────────────────────────────

// TrimHistory 硬截断消息历史以适应 maxTokens 限制。
// 规则：
//   - 始终保留 system 消息
//   - 从最旧的非 system 消息开始丢弃，直到总 token 数 ≤ maxTokens
//   - 若单条消息超过 maxTokens（罕见，如超大 tool 结果），截断其内容
func TrimHistory(msgs []types.Message, maxTokens int) []types.Message {
	if maxTokens <= 0 {
		maxTokens = MaxContextTokens
	}

	// 分离 system 消息和非 system 消息
	var sys []types.Message
	var rest []types.Message
	for _, m := range msgs {
		if m.Role == "system" {
			sys = append(sys, m)
		} else {
			rest = append(rest, m)
		}
	}

	// 从头部（最旧）开始丢弃，直到满足预算
	for len(rest) > 0 {
		total := EstimateHistoryTokens(sys) + EstimateHistoryTokens(rest)
		if total <= maxTokens {
			break
		}
		// 丢弃最旧的非 system 消息
		rest = rest[1:]
	}

	// 保底：若仅 system 消息就超限，截断 system 消息内容
	result := append(sys, rest...)
	if EstimateHistoryTokens(result) > maxTokens && len(result) > 0 {
		// 截断最长的一条消息的内容
		for i := range result {
			if result[i].Content != nil && len(*result[i].Content) > 500 {
				s := TruncateToolResult(*result[i].Content)
				result[i].Content = &s
				break
			}
		}
	}

	return result
}

// NeedCompression 判断历史消息是否需要压缩。
// 当总 token 数超过 CompressThreshold 时返回 true。
func NeedCompression(msgs []types.Message) bool {
	return EstimateHistoryTokens(msgs) > CompressThreshold
}
