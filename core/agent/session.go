package agent

import (
	"context"

	"github.com/RedHuang-0622/Seele/core/session"
	"github.com/RedHuang-0622/Seele/core/tool_holder"
)

// NewSession 创建一个绑定到本 Agent 工具集的对话会话。
// prompt 为空时不注入 system 消息。
func (a *Agent) NewSession(prompt string, loops int) *session.Holder {
	return session.New(a.llm, a.tools, prompt, loops)
}

// QuickChat 一次性对话，不保留历史。
func (a *Agent) QuickChat(ctx context.Context, prompt, input string) (string, error) {
	return a.NewSession(prompt, 8).Chat(ctx, input)
}

// QuickChatStream 一次性流式对话，不保留历史。
func (a *Agent) QuickChatStream(ctx context.Context, prompt, input string, onChunk func(string)) (string, error) {
	return a.NewSession(prompt, 8).ChatStream(ctx, input, onChunk)
}

// DirectDispatch 直接调度工具调用，绕过 LLM 循环。
// 用于 REPL 拦截审批响应后直接发送 _decide。
func (a *Agent) DirectDispatch(ctx context.Context, name, argsJSON string) (string, error) {
	return a.tools.Dispatch(ctx, name, argsJSON)
}

// Tools 暴露底层 tool_holder，供需要精细控制的场景使用。
func (a *Agent) Tools() *tool_holder.Holder { return a.tools }

