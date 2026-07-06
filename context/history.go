package seelectx

import (
	"context"
	"fmt"
	"log"
	"strings"

	types "github.com/RedHuang-0622/Seele/types"
)

// ── 上下文预算配置 ─────────────────────────────────────────────────

// ContextConfig 控制上下文管理的各项阈值。
// 零值字段使用 DefaultContextConfig() 中的默认值。
type ContextConfig struct {
	// MaxTokens 硬上限：单次 LLM 请求的最大上下文 token 数。
	MaxTokens int

	// CompressThreshold 压缩触发阈值：history token 数超过此值时触发压缩。
	// 通常设为 MaxTokens 的 75%。
	CompressThreshold int

	// MaxToolResultChars 单个 tool 结果追加到 history 前的最大字符数。
	// 超过此长度则截断，末尾追加 "[truncated]" 标记让 LLM 感知到裁剪。
	MaxToolResultChars int
}

// DefaultContextConfig 返回推荐的上下文配置。
// 默认值针对主流 LLM（8K 上下文窗口）设计：
//
//	MaxTokens:           8192
//	CompressThreshold:   6144 (75%)
//	MaxToolResultChars:  4000
func DefaultContextConfig() ContextConfig {
	return ContextConfig{
		MaxTokens:           8192,
		CompressThreshold:   6144,
		MaxToolResultChars:  4000,
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c ContextConfig) Effective() ContextConfig {
	d := DefaultContextConfig()
	if c.MaxTokens <= 0 {
		c.MaxTokens = d.MaxTokens
	}
	if c.CompressThreshold <= 0 {
		c.CompressThreshold = d.CompressThreshold
	}
	if c.MaxToolResultChars <= 0 {
		c.MaxToolResultChars = d.MaxToolResultChars
	}
	return c
}

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
// maxChars 为最大字符数，若 content 较短则原样返回；否则截断并追加 "[truncated]" 标记。
// 截断点在 maxChars 处，尽量在换行符处断开以保持可读性。
func TruncateToolResult(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	// 预留 marker 空间确保总长度不超限
	maxBody := maxChars - len(truncatedMarker)
	if maxBody <= 0 {
		return truncatedMarker
	}
	cut := content[:maxBody]
	// 尽量在最后一个换行符处截断
	if idx := strings.LastIndex(cut, "\n"); idx > maxBody/2 {
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
		maxTokens = DefaultContextConfig().MaxTokens
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
	// 丢弃头部孤 tool 消息（其 assistant(tool_calls) 已被丢弃）
	rest = stripLeadingOrphanTools(rest)

	// 保底：若仅 system 消息就超限，截断 system 消息内容
	result := append(sys, rest...)
	if EstimateHistoryTokens(result) > maxTokens && len(result) > 0 {
		// 截断最长的一条消息的内容
		for i := range result {
			if result[i].Content != nil && len(*result[i].Content) > 500 {
				s := TruncateToolResult(*result[i].Content, DefaultContextConfig().MaxToolResultChars)
				result[i].Content = &s
				break
			}
		}
	}

	return result
}

// stripLeadingOrphanTools removes orphan tool messages from the front of a message list.
// In a ReAct loop, tool messages always follow assistant(tool_calls). When history is
// truncated from the head, the assistant(tool_calls) may be dropped first, leaving orphan
// tool messages that cause LLM API errors.
func stripLeadingOrphanTools(msgs []types.Message) []types.Message {
	for len(msgs) > 0 && msgs[0].Role == "tool" {
		msgs = msgs[1:]
	}
	return msgs
}

// NeedCompression 判断历史消息是否需要压缩。
// threshold 是触发压缩的 token 数阈值，超过此值时返回 true。
func NeedCompression(msgs []types.Message, threshold int) bool {
	return EstimateHistoryTokens(msgs) > threshold
}

// ── LLM 压缩 ───────────────────────────────────────────────────────

// keepRecent 在压缩时始终保留的最后 N 条非 system 消息。
// 这些是最近的上下文，对 LLM 下一步推理最关键。
const keepRecent = 4

// compressSystemPrompt 是压缩专用 LLM 调用的 system prompt。
// 要求 LLM 把历史 tool 执行结果浓缩为简洁摘要。
const compressSystemPrompt = `Summarize the following execution history.
Focus on: key findings, errors encountered, actions taken, and final outcomes.
Be extremely concise — output a short paragraph under 150 words.
Do NOT re-execute any tools. Only summarize what already happened.`

// CompressHistory 用 LLM 将早期 tool 执行记录压缩为简短摘要。
//
// 算法：
//  1. 保留所有 system 消息 + 最近 keepRecent 条非 system 消息
//  2. 将其余消息（旧 tool 结果和 assistant tool_call）打包为压缩输入
//  3. 调用 LLM（无工具、低 max_tokens）生成摘要
//  4. 若压缩后 token 数仍超限，fallback 到硬截断
//
// 返回压缩后的 history；永远不会返回 nil。
func CompressHistory(ctx context.Context, client types.ChatCompleter, history []types.Message, maxTokens int) ([]types.Message, error) {
	if maxTokens <= 0 {
		maxTokens = DefaultContextConfig().MaxTokens
	}

	// 1. 拆分：system + 近期保留 + 可压缩部分
	sys, rest := splitSystem(history)
	if len(rest) <= keepRecent {
		// 消息太少，直接硬截断
		return TrimHistory(history, maxTokens), nil
	}

	keep := rest[len(rest)-keepRecent:]         // 最近 N 条完整保留
	compressible := rest[:len(rest)-keepRecent] // 剩余为可压缩部分

	// 防止拆散 assistant(tool_calls) 和 tool_result 配对：
	// 若 keep 头部是 tool 消息，其 assistant(tool_calls) 还在 compressible 末尾。
	// 向回扩展 keep 直到头部不是 tool（即遇到 assistant 消息）。
	for len(keep) > 0 && keep[0].Role == "tool" && len(compressible) > 0 {
		last := compressible[len(compressible)-1]
		compressible = compressible[:len(compressible)-1]
		keep = append([]types.Message{last}, keep...)
	}

	if len(compressible) == 0 {
		return TrimHistory(history, maxTokens), nil
	}

	// 2. 构建压缩输入：只取 compressible 消息
	compressInput := buildCompressInput(compressible)

	// 3. 调用 LLM 生成摘要（无工具、低温、低输出上限）
	summary, err := callCompressLLM(ctx, client, compressInput)
	if err != nil {
		log.Printf("[context] compression LLM call failed: %v, falling back to hard trim", err)
		return TrimHistory(history, maxTokens), nil
	}

	// 4. 组装压缩后的 history：system + 压缩摘要 + 近期消息
	compressed := make([]types.Message, 0, len(sys)+1+len(keep))
	compressed = append(compressed, sys...)
	summaryText := "[Context summary of earlier execution: " + summary + "]"
	compressed = append(compressed, types.Message{
		Role:    "system",
		Content: &summaryText,
	})
	compressed = append(compressed, keep...)

	// 5. 如果压缩后仍超过限制，硬截断保底
	if EstimateHistoryTokens(compressed) > maxTokens {
		log.Printf("[context] compression still over budget, applying hard trim")
		return TrimHistory(compressed, maxTokens), nil
	}

	beforeTokens := EstimateHistoryTokens(history)
	afterTokens := EstimateHistoryTokens(compressed)
	log.Printf("[context] compressed history: %d → %d tokens (saved %d)", beforeTokens, afterTokens, beforeTokens-afterTokens)

	return compressed, nil
}

// ── 内部辅助 ───────────────────────────────────────────────────────

// splitSystem 分离 system 消息和非 system 消息。
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

// buildCompressInput 将可压缩消息序列化为 LLM 可读的文本。
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
				// 截断过长内容，避免压缩输入本身太大
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

// callCompressLLM 调用 chatClient 生成压缩摘要。
// 使用空的工具列表，确保 LLM 不会发起 tool_call。
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
