// Package engine 提供 Agent 的上层 ReAct 循环封装。
//
// Engine 持有 Agent、LLM 客户端、会话配置，并管理独立的对话历史，
// 提供 Chat / ChatStream 两种入口执行完整的 ReAct 循环：
//
//	build history → get tools → call LLM → tool calls → dispatch → repeat
package engine

import (
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/contexts/cache"
	"github.com/RedHuang-0622/Seele/contexts/storage"
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

// Engine 封装 Agent 与 LLM 客户端，提供便捷的 ReAct 循环。
//
// 每个 Engine 实例管理自己的对话历史，支持多轮对话。
// 可选的 cache.Provider 用于会话历史缓存（JSON 文件，带 TTL + 置信度）。
// 可选的 tracer.Tracer 用于全链路追踪（默认 NoopTracer 零开销）。
type Engine struct {
	agent     *agent.Agent
	llm       types.ChatCompleter
	cfg       SessionConfig
	history   []types.Message
	sessionID string         // 缓存键
	cache     cache.Provider
	store     *storage.Store
	modelName string         // 供 tracer 使用
	tracer    tracer.Tracer  // 可观测性追踪器
	lastTrace *tracer.Tree   // 上次 Chat/ChatStream 的追踪树
}

// Option 配置 Engine 的创建参数。
type Option func(*Engine)

// WithSessionConfig 设置会话配置。
func WithSessionConfig(cfg SessionConfig) Option {
	return func(e *Engine) {
		e.cfg = cfg
	}
}

// WithCache 设置会话历史缓存（JSON 文件，TTL + 置信度）。
// Chat/ChatStream 会优先从缓存读取历史，命中且未过期则跳过完整 ReAct 循环。
func WithCache(c cache.Provider) Option {
	return func(e *Engine) {
		e.cache = c
	}
}

// WithStore 设置会话持久化存储。当 store 不为 nil 时，Chat/ChatStream 会在
// 每次循环结束后将历史保存到持久化存储，并在启动时优先从缓存、其次从存储恢复。
func WithStore(s *storage.Store) Option {
	return func(e *Engine) {
		e.store = s
	}
}

// WithTracer 设置全链路追踪器。不调用此方法时使用 NoopTracer（零开销）。
// 传入 tracer.NewSimpleTracer() 可在 Chat/ChatStream 结束后通过
// History() 或 ExportTrace 获取完整追踪树。
func WithTracer(t tracer.Tracer) Option {
	return func(e *Engine) {
		e.tracer = t
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
	// 尝试获取模型名（从账号池）
	modelName := ""
	if pool := a.AccountPool(); pool != nil {
		if accts := pool.All(); len(accts) > 0 {
			modelName = accts[0].Model
		}
	}

	e := &Engine{
		agent:     a,
		llm:       a.LLM(),
		cfg:       DefaultSessionConfig(),
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		modelName: modelName,
		tracer:    &tracer.NoopTracer{},
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

// Tracer 返回当前追踪器。可为 nil（未调用 WithTracer 时返回 NoopTracer）。
func (e *Engine) Tracer() tracer.Tracer { return e.tracer }

// ExportTrace 返回上一次 Chat/ChatStream 的完整追踪树。
// 每次 Chat/ChatStream 结束后自动导出并存储，之后追踪器内部状态自动重置。
// 返回 nil 表示尚未执行过 Chat/ChatStream，或上次未产生追踪数据。
func (e *Engine) ExportTrace() *tracer.Tree {
	return e.lastTrace
}
