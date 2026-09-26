package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/RedHuang-0622/Seele/types"
)

// InLoop 是「本轮已经持有 Session.mu」的历史读写把手。
//
// 背景：Chat / ChatStream 从进函数持锁到出函数释放，整段 ReAct 循环（含工具
// Dispatch 与全部 LoopHooks、ContextController 回调）都跑在同一 goroutine、
// 同一把锁内。这条路径上的宿主代码再调 Session 的历史方法（History /
// ReplaceHistory / AppendHistory / SetSystemPrompt）就是同 goroutine 抢自己
// 已持有的非重入 Mutex = 永久自锁。实测表现：会话永远「运行中」，排队输入再
// 也发不出去。
//
// 本把手不提供「绕锁」的能力：它只在锁**已经**由当前回合持有的前提下工作，
// 因此循环内改写历史与循环外改写走的是同一份数据、同一套语义，只是不再第二
// 次取锁。
//
// 取用方式唯一：由 Chat / ChatStream 注入本轮 ctx，宿主凭 ctx 调
// InLoopFrom。这样「我此刻在不在环内」由引擎作证，而不需要宿主用调用计数、
// 时间戳之类的手段去猜。
//
// 边界（调用方必须知道）：
//   - 把手绑定一次回合（世代号）。回合结束后凭旧 ctx 再取会被拒
//     （ErrInLoopExpired）；把 ctx 存进更长命的对象里是错的用法。
//   - 把手**不校验 goroutine**。Go 无法可靠取得 goroutine 身份，所以「本轮
//     进行中被别的 goroutine 拿走这个 ctx」这一种误用只能在回合内成立：它
//     会与循环抢同一份历史。仅在同一 goroutine 的调用栈内使用（工具
//     handler、LoopHooks、ContextController）。
type InLoop struct {
	sess *Session
	gen  uint64
}

// historyReplacer 是就地替换工作历史的可选能力（ReActLoop 实现）。自定义 Loop
// 未实现时，环内替换明确报错，不回退成「追加两条」这种近似。
type historyReplacer interface {
	ReplaceHistory(history []types.Message)
}

// inLoopKey 是本轮把手在 ctx 中的键（私有类型，外部无法伪造）。
type inLoopKey struct{}

// ErrNotInLoop 表示该 ctx 不来自一次进行中的回合：调用方应当改走常规的
// Session 方法（那会正常排队等锁，而不是自锁）。
var ErrNotInLoop = errors.New("session: context is not inside an active turn")

// ErrInLoopExpired 表示把手来自已经结束的那一回合。
var ErrInLoopExpired = errors.New("session: in-loop handle belongs to a finished turn")

// ErrLoopUnsupported 表示当前 Loop 实现不支持环内历史替换。
var ErrLoopUnsupported = errors.New("session: loop does not support in-loop history replacement")

// ErrInLoopInFlightDropped 表示这次环内替换会丢掉正在飞的那一截 tool_call
// 单元（assistant 的 tool_calls 已入历史、其 tool 结果尚未 append）。调用方
// 应把该尾部保留在替换结果里再试一次，而不是忽略——否则紧随其后 append 的
// tool 消息会成孤儿。
var ErrInLoopInFlightDropped = errors.New("session: in-loop replacement would drop the in-flight tool call")

// enterInLoop 在 Chat / ChatStream 取到会话锁之后调用。
//
// 守卫用「单调回合序号 + 进行中标志」两枚原子量：只靠序号并在出回合时清零，
// 下一回合会从 1 重新数，上一回合泄漏的把手就会被误判成有效。
func (e *Session) enterInLoop(ctx context.Context) context.Context {
	gen := e.inLoopSeq.Add(1)
	e.inLoopActive.Store(true)
	return context.WithValue(ctx, inLoopKey{}, &InLoop{sess: e, gen: gen})
}

// exitInLoop 与 enterInLoop 配对（在释放会话锁之前执行）。
func (e *Session) exitInLoop() { e.inLoopActive.Store(false) }

// InLoopFrom 返回注入在 ctx 里的环内把手。ok=false 表示该 ctx 不在回合内
// （从未进过 Chat / ChatStream，或已被转手成另一个不带本值的 ctx）。
func InLoopFrom(ctx context.Context) (*InLoop, bool) {
	if ctx == nil {
		return nil, false
	}
	handle, ok := ctx.Value(inLoopKey{}).(*InLoop)
	if !ok || handle == nil || handle.sess == nil {
		return nil, false
	}
	if !handle.sess.inLoopActive.Load() || handle.sess.inLoopSeq.Load() != handle.gen {
		return nil, false
	}
	return handle, true
}

// History 返回本轮当前工作历史的拷贝，不取会话锁。
func (h *InLoop) History() ([]types.Message, error) {
	if h == nil || h.sess == nil {
		return nil, ErrNotInLoop
	}
	if !h.active() {
		return nil, ErrInLoopExpired
	}
	if h.sess.loop == nil {
		return nil, nil
	}
	return h.sess.loop.History(), nil
}

// ReplaceHistory 就地替换本轮工作历史，语义与「循环外清空后逐条追加」一致，
// 差别只在不再二次取锁。替换后的历史会被重新发布给观测面（与
// handleContextEvent 的决策落地同一条口径）。
//
// 下界校验：环内调用点必然落在「assistant 已带 tool_calls、其 tool 结果尚未
// append」的时刻（ReActLoop 在 Dispatch 返回之后才追加结果，见 loop.go），所以
// 正在飞的这一截必须留在替换后的历史里——否则紧接着 append 的 tool 消息就成了
// 孤儿，下一次请求会被 provider 拒掉。这不是压缩策略，是「不要把正在执行的
// 这一步弄丢」。
func (h *InLoop) ReplaceHistory(history []types.Message) error {
	if h == nil || h.sess == nil {
		return ErrNotInLoop
	}
	if !h.active() {
		return ErrInLoopExpired
	}
	replacer, ok := h.sess.loop.(historyReplacer)
	if !ok {
		return ErrLoopUnsupported
	}
	if dropped := droppedInFlightUnit(h.sess.loop.History(), history); dropped != "" {
		return fmt.Errorf("%w: tool call %q", ErrInLoopInFlightDropped, dropped)
	}
	replaced := append([]types.Message(nil), history...)
	replacer.ReplaceHistory(replaced)
	h.sess.publishHistory(replaced)
	return nil
}

// droppedInFlightUnit 返回被替换丢掉的那个在飞 tool_call ID（无则空串）。
//
// 在飞单元 = 当前历史里最后一条带 ToolCalls 的 assistant，且它的某个 call ID
// 在当前历史中还没有配对的 tool 消息。
func droppedInFlightUnit(current, replacement []types.Message) string {
	resolved := make(map[string]struct{}, len(current))
	assistantAt := -1
	for i, msg := range current {
		switch msg.Role {
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				assistantAt = i
			}
		case "tool":
			if msg.ToolCallID != "" {
				resolved[msg.ToolCallID] = struct{}{}
			}
		}
	}
	if assistantAt < 0 {
		return ""
	}
	var pending []string
	for _, call := range current[assistantAt].ToolCalls {
		if _, done := resolved[call.ID]; !done {
			pending = append(pending, call.ID)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	// 在飞 assistant 若还在替换结果里，尾部由循环自己续写，不算丢。
	for _, msg := range replacement {
		if msg.Role == "assistant" && reflect.DeepEqual(msg.ToolCalls, current[assistantAt].ToolCalls) {
			return ""
		}
	}
	return pending[0]
}

// SetSystemPrompt 与 Session.SetSystemPrompt 同一实现（同一把锁内复用
// setSystemPromptLocked），语义完全一致：已有 system 消息则替换，否则前插。
func (h *InLoop) SetSystemPrompt(prompt string) error {
	if h == nil || h.sess == nil {
		return ErrNotInLoop
	}
	if !h.active() {
		return ErrInLoopExpired
	}
	h.sess.setSystemPromptLocked(prompt)
	return nil
}

// active 复判把手是否仍属于正在进行的那一回合。
func (h *InLoop) active() bool {
	return h.sess.inLoopActive.Load() && h.sess.inLoopSeq.Load() == h.gen
}
