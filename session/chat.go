// Package session provides Seele's user-facing ReAct Session and the lower
// level ReActLoop primitive.
//
// A Session owns working history and the Chat / ChatStream lifecycle. It uses
// an injected Agent for model and tool access, and an optional caller-owned
// DurableHistory for persistence. New remains only as a compatibility
// constructor for the previous functional-options API.
//
//	build history -> get tools -> call LLM -> tool calls -> dispatch -> repeat
package session

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

// Agent is the minimal assembled Agent surface consumed by Session. Session
// does not depend on the concrete agent package or any tool provider. The
// concrete agent.Agent implementation satisfies this interface implicitly.
type Agent interface {
	ToolRuntime
	LLM() types.ChatCompleter
}

// Session is the user-facing conversation object.
type Session struct {
	// mu serializes one ordered conversation. Use separate Sessions for
	// parallel work; a ChatStream callback must not re-enter this Session.
	mu sync.Mutex

	agent     Agent
	llm       types.ChatCompleter
	loop      Loop
	tracer    tracer.Tracer
	lastTrace *tracer.Tree

	cfg               SessionConfig
	history           []types.Message
	sessionID         string
	cache             cache.Provider
	store             storage.Storage
	modelName         string
	hooks             *LoopHooks
	telemetryHook     telemetry.Hook
	blockSystemPrompt bool
}

// Option 配置 Session 的创建参数。
type Option func(*Session)

func WithSessionConfig(cfg SessionConfig) Option {
	return func(e *Session) { e.cfg = cfg }
}
func WithCache(c cache.Provider) Option {
	return func(e *Session) { e.cache = c }
}
func WithStore(s storage.Storage) Option {
	return func(e *Session) { e.store = s }
}
func WithTracer(t tracer.Tracer) Option {
	return func(e *Session) { e.tracer = t }
}
func WithSystemPrompt(prompt string) Option {
	return func(e *Session) {
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
	return func(e *Session) { e.loop = l }
}

// WithHooks 设置 ReAct 循环的可视化回调。
// 回调在每次 LLM 调用和工具调度前后触发，用于实现交互式进度展示。
func WithHooks(hooks *LoopHooks) Option {
	return func(e *Session) { e.hooks = hooks }
}

// WithTelemetryHook installs structured OTel-aligned lifecycle hooks on the
// default ReAct loop.
func WithTelemetryHook(hook telemetry.Hook) Option {
	return func(e *Session) { e.telemetryHook = hook }
}

// New creates a Session through the legacy functional-options API.
//
// Deprecated: use NewSession so Runtime, History,
// Context, and Telemetry ownership are explicit.
func New(a Agent, opts ...Option) *Session {
	e := &Session{
		agent:     a,
		cfg:       DefaultSessionConfig(),
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		tracer:    &tracer.NoopTracer{},
	}
	if a != nil {
		e.llm = a.LLM()
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
		rl.respCache = cache.NewResponseCache(e.cache)
		rl.store = e.store
		rl.hooks = e.hooks
		rl.telemetryHook = e.telemetryHook
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

// AgentRuntime returns the assembled agent used by this Session.
func (e *Session) AgentRuntime() Agent { return e.agent }

// History 返回当前对话历史。
func (e *Session) History() []types.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.loop != nil {
		return e.loop.History()
	}
	return nil
}

// ClearHistory 清空对话历史。
func (e *Session) ClearHistory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.clearHistoryLocked()
}

func (e *Session) clearHistoryLocked() {
	if e.loop != nil {
		e.loop.ClearHistory()
	}
}

// Reset clears both the in-memory working history and the caller-owned
// durable snapshot, when one was injected. It is the explicit operation for
// starting a fresh conversation; ClearHistory only changes the current
// working view for compatibility with the lower-level Loop API.
func (e *Session) Reset(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		e.clearHistoryLocked()
		return nil
	}
	if rl.historyOwner != nil {
		if err := rl.historyOwner.Clear(ctx); err != nil {
			return fmt.Errorf("session: clear durable history: %w", err)
		}
		rl.ClearHistory()
		return nil
	}
	if rl.store != nil {
		if err := rl.store.Delete(rl.sessionID); err != nil {
			return fmt.Errorf("session: clear stored history: %w", err)
		}
	}
	if rl.cache != nil {
		rl.cache.Delete(rl.sessionID)
	}
	rl.ClearHistory()
	return nil
}

// SessionID 返回当前会话 ID。
func (e *Session) SessionID() string { return e.sessionID }

// Tracer 返回当前追踪器。
func (e *Session) Tracer() tracer.Tracer { return e.tracer }

// ExportTrace 返回上一次 Chat 的追踪树。
func (e *Session) ExportTrace() *tracer.Tree {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastTrace
}

// Chat 执行 ReAct 循环，返回最终文本回复。
func (e *Session) Chat(ctx context.Context, userInput string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reply, err := e.loop.Run(ctx, userInput, nil)
	e.lastTrace = e.tracer.Export(ctx)
	return reply, err
}

// ChatStream 执行流式 ReAct 循环。
func (e *Session) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	reply, err := e.loop.Run(ctx, userInput, onChunk)
	e.lastTrace = e.tracer.Export(ctx)
	return reply, err
}

// SetMaxLoops 动态设置最大 tool_call 循环次数。
// 0 表示使用默认值（25）。
func (e *Session) SetMaxLoops(n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		return
	}
	if n <= 0 {
		n = 25
	}
	rl.cfg.MaxLoops = n
}

// AppendHistory 追加消息到对话历史。
func (e *Session) AppendHistory(msg types.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if rl, ok := e.loop.(*ReActLoop); ok {
		rl.history = append(rl.history, msg)
	}
}

// SetSystemPrompt 动态替换 system prompt。
// 找到已有 system 消息替换，没有则追加到历史开头。
func (e *Session) SetSystemPrompt(prompt string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		return
	}
	msg := types.Message{Role: "system", Content: &prompt}
	if e.blockSystemPrompt {
		for i := range rl.promptBlocks {
			if rl.promptBlocks[i].Name == "system" {
				rl.promptBlocks[i].Messages = []types.Message{msg}
				return
			}
		}
	}
	for i, m := range rl.history {
		if m.Role == "system" {
			rl.history[i] = msg
			return
		}
	}
	rl.history = append([]types.Message{msg}, rl.history...)
}
