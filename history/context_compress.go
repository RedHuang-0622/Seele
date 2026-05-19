package history

import (
	"context"
	"fmt"
	"log"

	llm "github.com/sukasukasuka123/Seele/llm"
	types "github.com/sukasukasuka123/Seele/types"
)

// keepRecent 在压缩时始终保留的最后 N 条非 system 消息。
// 这些是最近的上下文，对 LLM 下一步推理最关键。
const keepRecent = 4

// compressSystemPrompt 是压缩专用 LLM 调用的 system prompt。
// 要求 LLM 把历史 tool 执行结果浓缩为简洁摘要。
const compressSystemPrompt = `Summarize the following execution history.
Focus on: key findings, errors encountered, actions taken, and final outcomes.
Be extremely concise — output a short paragraph under 150 words.
Do NOT re-execute any tools. Only summarize what already happened.`

// compressMaxTokens 压缩 LLM 调用的输出上限。
// 摘要应该远小于原始消息。
const compressMaxTokens = 300

// ── 压缩入口 ───────────────────────────────────────────────────────

// CompressHistory 用 LLM 将早期 tool 执行记录压缩为简短摘要。
//
// 算法：
//  1. 保留所有 system 消息 + 最近 keepRecent 条非 system 消息
//  2. 将其余消息（旧 tool 结果和 assistant tool_call）打包为压缩输入
//  3. 调用 LLM（无工具、低 max_tokens）生成摘要
//  4. 若压缩后 token 数仍超限，fallback 到硬截断
//
// 返回压缩后的 history；永远不会返回 nil。
func CompressHistory(ctx context.Context, client *llm.ChatClient, history []types.Message, maxTokens int) ([]types.Message, error) {
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
func callCompressLLM(ctx context.Context, client *llm.ChatClient, input string) (string, error) {
	if input == "" {
		return "no previous context to summarize", nil
	}

	// 创建副本（值拷贝 Cfg），避免修改共享 client 的配置引发竞态。
	// http.Client 是并发安全的，共享同一个实例没问题。
	compressClient := *client
	compressClient.Cfg.MaxTokens = compressMaxTokens
	compressClient.Cfg.Temperature = 0.3

	messages := []types.Message{
		{Role: "system", Content: strPtr(compressSystemPrompt)},
		{Role: "user", Content: &input},
	}

	msg, err := compressClient.Complete(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	if msg.Content == nil {
		return "", fmt.Errorf("compression LLM returned nil content")
	}
	return *msg.Content, nil
}

func strPtr(s string) *string { return &s }
