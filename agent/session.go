package agent

import (
	"context"

	seelectx "github.com/RedHuang-0622/Seele/contexts"
)

// NewSession 创建绑定到本 Agent 工具集的对话会话。
//
// Deprecated: 会话管理不属于 Agent 职责。后续版本将移除。
// 替代方案：
//   - engine.New().Chat()  /  engine.New().ChatStream()
//   - seelectx.New(a.LLM(), a.Tools(), prompt, cfg).Chat(ctx, input)
func (a *Agent) NewSession(prompt string, maxLoops int) *seelectx.Holder {
	return seelectx.New(a.llmClient, a.toolGW, prompt, seelectx.SessionConfig{MaxLoops: maxLoops})
}

// QuickChat 一次性对话。
//
// Deprecated: 使用 engine.New().Chat() 替代。
func (a *Agent) QuickChat(ctx context.Context, prompt, input string) (string, error) {
	return a.NewSession(prompt, 8).Chat(ctx, input)
}

// QuickChatStream 一次性流式对话。
//
// Deprecated: 使用 engine.New().ChatStream() 替代。
func (a *Agent) QuickChatStream(ctx context.Context, prompt, input string, onChunk func(string)) (string, error) {
	return a.NewSession(prompt, 8).ChatStream(ctx, input, onChunk)
}
