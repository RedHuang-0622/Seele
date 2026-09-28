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
	"sync/atomic"
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
	// mu 只护 Session 自己的短状态（lastTrace、闸门惰性初始化）。回合串行不靠它：
	// Chat / ChatStream 领的是容量 1 的回合令牌通道（turn），没有任何会话锁被整轮
	// 持有（见 turn.go）。
	mu sync.Mutex

	// turn 是回合闸门：一轮一枚令牌，用通道代替「把会话锁征用整轮」。
	turn chan struct{}

	agent     Agent
	llm       types.ChatCompleter
	loop      Loop
	tracer    tracer.Tracer
	lastTrace *tracer.Tree

	cfg       SessionConfig
	history   []types.Message
	sessionID string
	// published 是最近一次发布的历史快照（观测面无锁读取；见
	// HistoryIfAvailable 与 WithHistoryPublisher）。运行期间由循环在历史
	// 检查点更新，空闲时由 HistoryIfAvailable 的直读路径顺手刷新。
	published atomic.Pointer[sessionHistorySnapshot]
	cache             cache.Provider
	store             storage.Storage
	modelName         string
	hooks             *LoopHooks
	telemetryHook     telemetry.Hook
	blockSystemPrompt bool
}

// sessionHistorySnapshot 是发布给观测面的历史快照（切片已拷贝，调用方只读）。
type sessionHistorySnapshot struct {
	messages []types.Message
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
		turn:      make(chan struct{}, 1),
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
		rl.historyPublisher = e.publishHistory
		rl.sessionID = e.sessionID
		rl.tracer = e.tracer
		rl.modelName = e.modelName
		rl.cache = e.cache
		rl.respCache = cache.NewResponseCache(e.cache)
		rl.store = e.store
		rl.hooks = e.hooks
		rl.telemetryHook = e.telemetryHook
		rl.blockSystemPrompt = e.blockSystemPrompt
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

// History 返回当前对话历史的拷贝。**永不阻塞**：工作历史由循环的工作状态短临界区
// 保护，回合在飞时也一样立刻返回。
//
// 环内的工具 handler / LoopHooks / ContextController 与环外的 UI、折叠路径走的是
// 同一个方法、同一份数据（旧模型下环内调用是同 goroutine 自锁，绕开它需要一张注入
// ctx 的把手；那个模型连同把手一起删掉了，见 turn.go）。
func (e *Session) History() []types.Message {
	if e == nil || e.loop == nil {
		return nil
	}
	return e.loop.History()
}

// ReplaceHistory 就地替换工作历史：会话空闲 → 当场生效；回合在飞 → 挂进循环的检查点
// 队列并**立即返回**，由循环在下一个安全检查点落地（同回合的下一次请求读到的就是替换
// 结果）。会丢掉在飞 tool_call 单元的替换被拒（ErrInFlightToolCallDropped）且历史不动。
//
// 自定义 Loop 未实现该能力时报 ErrLoopUnsupported。
func (e *Session) ReplaceHistory(history []types.Message) error {
	capable, ok := e.historyCapability()
	if !ok {
		return ErrLoopUnsupported
	}
	return capable.ReplaceHistory(history)
}

// historyCapability 返回底层 Loop 的工作历史能力。ReActLoop 实现它；自定义 Loop 未
// 实现时 ok=false，写路径按各自语义退化（见 turn.go 的 workingHistory）。
func (e *Session) historyCapability() (workingHistory, bool) {
	if e == nil || e.loop == nil {
		return nil, false
	}
	capable, ok := e.loop.(workingHistory)
	return capable, ok
}

// HistoryIfAvailable 返回当前对话历史的副本，**永不阻塞**：
//
//   - 会话空闲：直接读，拿到的是权威历史（顺手刷新发布面）；
//   - 会话正忙（ChatStream 持锁跑整段 ReAct 循环）：返回循环最近一次发布的
//     快照——它在每个历史检查点（模型调用前 / assistant 落历史后 / 工具结果
//     落历史后）更新，因此代价是最多滞后一个检查点，而不是等整轮跑完。
//
// 观测面与执行面分工（调用方必须遵守）：执行路径（与 ChatStream 同一
// goroutine，如流式回调内）用 History()；其它 goroutine 的观测路径（宿主 UI、
// 详情读取、落账投影）用本方法。原因：ChatStream 从进函数持到出函数持有整把
// 会话锁，一次长文流式可达数十秒，任何 History() 都会排在它后面——观测面被
// 拖住的直接表现是"表格/详情卡住不动"。
//
// 注意 (nil, false) 只表示"此刻确实读不到"（尚未发布过且锁被别处短暂持有），
// 不表示"没有历史"——调用方不要把 false 当作空历史覆盖已有缓存。
func (e *Session) HistoryIfAvailable() ([]types.Message, bool) {
	if e == nil || e.loop == nil {
		return nil, false
	}
	if e.mu.TryLock() {
		history := e.loop.History()
		e.mu.Unlock()
		// 空闲直读顺带刷新发布面：会话外（ClearHistory/Reset/AppendHistory/
		// SetSystemPrompt 等）的历史变更由此被观测面看到。
		e.publishHistory(history)
		return history, true
	}
	if snapshot := e.published.Load(); snapshot != nil {
		return snapshot.messages, true
	}
	return nil, false
}

// publishHistory 记录一份历史快照（切片已由调用方拷贝）。循环在持锁的
// 检查点调用它，观测面据此无锁读取运行中的历史。
func (e *Session) publishHistory(history []types.Message) {
	if e == nil {
		return
	}
	e.published.Store(&sessionHistorySnapshot{messages: history})
}

// ClearHistory 清空对话历史（保留 system 消息）。
func (e *Session) ClearHistory() {
	if e == nil || e.loop == nil {
		return
	}
	e.loop.ClearHistory()
}

// Reset clears both the in-memory working history and the caller-owned
// durable snapshot, when one was injected. It is the explicit operation for
// starting a fresh conversation; ClearHistory only changes the current
// working view for compatibility with the lower-level Loop API.
func (e *Session) Reset(ctx context.Context) error {
	if e == nil || e.loop == nil {
		return nil
	}
	rl, ok := e.loop.(*ReActLoop)
	if !ok {
		e.loop.ClearHistory()
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
	if err := e.acquireTurn(ctx); err != nil {
		return "", err
	}
	defer e.releaseTurn()
	reply, err := e.loop.Run(ctx, userInput, nil)
	e.rememberTrace(ctx)
	return reply, err
}

// ChatStream 执行流式 ReAct 循环。
func (e *Session) ChatStream(ctx context.Context, userInput string, onChunk func(string)) (string, error) {
	if err := e.acquireTurn(ctx); err != nil {
		return "", err
	}
	defer e.releaseTurn()
	reply, err := e.loop.Run(ctx, userInput, onChunk)
	e.rememberTrace(ctx)
	return reply, err
}

// rememberTrace 记录本轮追踪树（短临界区；ExportTrace 取同一把锁读）。
func (e *Session) rememberTrace(ctx context.Context) {
	tree := e.tracer.Export(ctx)
	e.mu.Lock()
	e.lastTrace = tree
	e.mu.Unlock()
}

// SetMaxLoops 动态设置最大 tool_call 循环次数。
// 0 表示使用默认值（25）。
func (e *Session) SetMaxLoops(n int) {
	if e == nil || e.loop == nil {
		return
	}
	if rl, ok := e.loop.(*ReActLoop); ok {
		rl.SetMaxLoops(n)
	}
}

// AppendHistory 追加一条消息到对话历史：空闲当场落地，回合在飞排队到下一个检查点
// （回合里插消息若落进「tool_calls 已发、结果未落」的中间态会造孤儿）。
func (e *Session) AppendHistory(msg types.Message) {
	if capable, ok := e.historyCapability(); ok {
		capable.AppendHistory(msg)
		return
	}
	if rl, ok := e.loop.(*ReActLoop); ok {
		rl.AppendHistory(msg)
	}
}

// SetSystemPrompt 动态替换 system prompt：找到已有 system 消息替换，没有则前插；会话
// 把 prompt 静态挂在 promptBlocks 上时改块。落点规则与 ReplaceHistory 一致（空闲当场、
// 回合在飞排队到下一个检查点）。
func (e *Session) SetSystemPrompt(prompt string) {
	if e == nil {
		return
	}
	if capable, ok := e.historyCapability(); ok {
		capable.SetSystemPrompt(prompt)
		return
	}
	if rl, ok := e.loop.(*ReActLoop); ok {
		rl.SetSystemPrompt(prompt)
	}
}
