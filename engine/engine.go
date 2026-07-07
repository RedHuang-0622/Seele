// Package engine 提供 Agent 的上层 ReAct 循环封装。
//
// Engine 持有 Agent、LLM 客户端、会话配置，并管理独立的对话历史，
// 提供 Chat / ChatStream 两种入口执行完整的 ReAct 循环：
//
//	build history → get tools → call LLM → tool calls → dispatch → repeat
package engine

import (
	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/types"
)

// Engine 封装 Agent 与 LLM 客户端，提供便捷的 ReAct 循环。
//
// 每个 Engine 实例管理自己的对话历史，支持多轮对话。
type Engine struct {
	agent   *agent.Agent
	llm     types.ChatCompleter
	cfg     SessionConfig
	history []types.Message
}

// Option 配置 Engine 的创建参数。
type Option func(*Engine)

// WithSessionConfig 设置会话配置。
func WithSessionConfig(cfg SessionConfig) Option {
	return func(e *Engine) {
		e.cfg = cfg
	}
}

// WithSystemPrompt 设置 system 消息（替换已有 system 消息或插入首条）。
func WithSystemPrompt(prompt string) Option {
	return func(e *Engine) {
		msg := types.Message{Role: "system", Content: &prompt}
		for i, m := range e.history {
			if m.Role == "system" {
				e.history[i] = msg
				return
			}
		}
		e.history = append([]types.Message{msg}, e.history...)
	}
}

// New 创建 Engine。
//
//	engine := engine.New(agt, engine.WithSessionConfig(cfg))
//
// 必须传入 *agent.Agent，不传 opts 时使用默认配置。
func New(a *agent.Agent, opts ...Option) *Engine {
	e := &Engine{
		agent: a,
		llm:   a.LLM(),
		cfg:   DefaultSessionConfig(),
	}
	for _, opt := range opts {
		opt(e)
	}
	e.cfg = e.cfg.Effective()
	return e
}

// Agent 返回底层的 Agent 实例。
func (e *Engine) Agent() *agent.Agent { return e.agent }

// History 返回当前对话历史的只读副本。
func (e *Engine) History() []types.Message {
	cp := make([]types.Message, len(e.history))
	copy(cp, e.history)
	return cp
}

// ClearHistory 清空对话历史，但保留 system 消息（如有）。
func (e *Engine) ClearHistory() {
	var sys []types.Message
	for _, m := range e.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	e.history = sys
}
