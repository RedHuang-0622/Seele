// Package engine 提供 Agent 的上层 ReAct 循环封装。
//
// Engine 持有 Agent、LLM 客户端，并委托 Loop 接口执行实际 ReAct 循环，
// 提供 Chat / ChatStream 两种入口。
//
//	build history -> get tools -> call LLM -> tool calls -> dispatch -> repeat
package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

// Engine 封装 Agent 与 LLM 客户端，提供便捷的 ReAct 循环。
type Engine struct {
	agent     *agent.Agent
	llm       types.ChatCompleter
	loop      Loop
	tracer    tracer.Tracer
	lastTrace *tracer.Tree

	cfg       SessionConfig
	history   []types.Message
	sessionID string
	cache     cache.Provider
	store     *storage.Store
	modelName string
	hooks     *LoopHooks
}

// Option 配置 Engine 的创建参数。
type Option func(*Engine)

func WithSessionConfig(cfg SessionConfig) Option {
	return func(e *Engine) { e.cfg = cfg }
}
func WithCache(c cache.Provider) Option {
	return func(e *Engine) { e.cache = c }
}
func WithStore(s *storage.Store) Option {
	return func(e *Engine) { e.store = s }
}
func WithTracer(t tracer.Tracer) Option {
	return func(e *Engine) { e.tracer = t }
}
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
func WithLoop(l Loop) Option {
	return func(e *Engine) { e.loop = l }
}

// WithHooks 设置 ReAct 循环的可视化回调。
// 回调在每次 LLM 调用和工具调度前后触发，用于实现交互式进度展示。
func WithHooks(hooks *LoopHooks) Option {
	return func(e *Engine) { e.hooks = hooks }
}

// New 创建 Engine。
func New(a *agent.Agent, opts ...Option) *Engine {
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

	if e.loop == nil {
		rl := NewReActLoop(a, e.llm)
		rl.sessionID = e.sessionID
		rl.tracer = e.tracer
		rl.modelName = e.modelName
		rl.cache = e.cache
		rl.store = e.store
		rl.hooks = e.hooks
		if e.cfg.MaxLoops != DefaultSessionConfig().MaxLoops {
			rl.cfg.MaxLoops = e.cfg.MaxLoops
		}
		if len(e.history) > 0 {
			rl.history = append(rl.history, e.history...)
		}
		e.loop = rl
	}

	return e
}

// Agent 返回底层的 Agent 实例。
func (e *Engine) Agent() *agent.Agent { return e.agent }

// History 返回当前对话历史。
func (e *Engine) History() []types.Message {
	if e.loop != nil {
		return e.loop.History()
	}
	return nil
}

// ClearHistory 清空对话历史。
func (e *Engine) ClearHistory() {
	if e.loop != nil {
		e.loop.ClearHistory()
	}
}

// SessionID 返回当前会话 ID。
func (e *Engine) SessionID() string { return e.sessionID }

// Tracer 返回当前追踪器。
func (e *Engine) Tracer() tracer.Tracer { return e.tracer }

// ExportTrace 返回上一次 Chat 的追踪树。
func (e *Engine) ExportTrace() *tracer.Tree { return e.lastTrace }

// Chat 执行 ReAct 循环，返回最终文本回复。
func (e *Engine) Chat(ctx context.Context, userInput string) (string, error) {
	reply, err := e.loop.Run(ctx, userInput, nil)
	e.lastTrace = e.tracer.Export(ctx)
	return reply, err
}

// ChatStream 执行流式 ReAct 循环。
func (e *Engine) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	reply, err := e.loop.Run(ctx, userInput, onChunk)
	e.lastTrace = e.tracer.Export(ctx)
	return reply, err
}

// SetMaxLoops 动态设置最大 tool_call 循环次数。
// 0 表示使用默认值（25）。
func (e *Engine) SetMaxLoops(n int) {
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		return
	}
	if n <= 0 {
		n = 25
	}
	rl.cfg.MaxLoops = n
}

// SetSystemPrompt 动态替换 system prompt。
// 找到已有 system 消息替换，没有则追加到历史开头。
func (e *Engine) SetSystemPrompt(prompt string) {
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		return
	}
	msg := types.Message{Role: "system", Content: &prompt}
	for i, m := range rl.history {
		if m.Role == "system" {
			rl.history[i] = msg
			return
		}
	}
	rl.history = append([]types.Message{msg}, rl.history...)
}
