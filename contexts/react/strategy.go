// Package react 封装 LLM ReAct 循环的调用策略。
//
// 核心抽象：CompletionStrategy 接口定义了 LLM 调用的统一方式。
// 两种实现：
//   - SyncStrategy   → 同步 Complete（一次请求获取完整回复）
//   - StreamStrategy → SSE 流式 CompleteStream + chunk 回放
//
// 使用方（seelectx.Holder.chatLoop）通过 CompletionStrategy 接口
// 调用 LLM，不感知底层是同步还是流式。
package react

import (
	"context"

	types "github.com/RedHuang-0622/Seele/types"
)

// CompletionResult 封装一次 LLM 调用的产出。
type CompletionResult struct {
	Content          string
	ReasoningContent string
	ToolCalls        []types.ToolCall
}

// CompletionStrategy 封装一次 LLM 调用的完整流程。
// 两种实现：SyncStrategy（同步）和 StreamStrategy（流式）。
type CompletionStrategy interface {
	Execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*CompletionResult, error)
}

// SyncStrategy：同步 LLM 调用。
type SyncStrategy struct{}

func (s *SyncStrategy) Execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*CompletionResult, error) {
	msg, err := client.Complete(ctx, messages, tools)
	if err != nil {
		return nil, err
	}
	content := ""
	if msg.Content != nil {
		content = *msg.Content
	}
	return &CompletionResult{
		Content:          content,
		ReasoningContent: msg.ReasoningContent,
		ToolCalls:        msg.ToolCalls,
	}, nil
}

// StreamStrategy：SSE 流式 LLM 调用。
// 收集所有 chunk 后在 Execute 内部回放，保证调用方拿到的是完整结果。
type StreamStrategy struct {
	OnChunk func(delta string)
}

func (s *StreamStrategy) Execute(ctx context.Context, client types.ChatCompleter, messages []types.Message, tools []types.Tool) (*CompletionResult, error) {
	var chunks []string
	fullContent, reasoningContent, toolCalls, err := client.CompleteStream(
		ctx, messages, tools,
		func(delta string) { chunks = append(chunks, delta) },
	)
	if err != nil {
		return nil, err
	}
	for _, c := range chunks {
		s.OnChunk(c)
	}
	return &CompletionResult{
		Content:          fullContent,
		ReasoningContent: reasoningContent,
		ToolCalls:        toolCalls,
	}, nil
}

// ContentPtr 返回 content 的指针。
// 当 content 为空且存在 toolCalls 时返回 nil，与 OpenAI API 行为一致。
func ContentPtr(content string, toolCalls []types.ToolCall) *string {
	if content == "" && len(toolCalls) > 0 {
		return nil
	}
	return &content
}
