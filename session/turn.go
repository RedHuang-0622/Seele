package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/RedHuang-0622/Seele/types"
)

// 本文件是「回合闸门 + 工作状态短临界区」这一版会话模型的入口，它替换掉了此前
// 的环内历史把手（session/inloop.go，已删除）。
//
// 旧模型的病：Chat / ChatStream 从进函数持锁到出函数，整段 ReAct 循环（工具
// Dispatch、全部 LoopHooks、ContextController 回调）都在同一 goroutine、同一把
// 非重入锁内。宿主在这条路径上再调 Session 的历史方法，就是同 goroutine 抢自己
// 已持有的锁 = 永久自锁（Seelex 的 compact_context 踩在这上面，且它顺手攥着宿主
// 的端口锁，把故障面从「这一轮没回复」放大到整进程）。旧补丁给引擎注入一张
// 「环内把手」，宿主凭本轮 ctx 走不取锁的旁路——它治的是症状：能不能用取决于
// 调用方有没有把 ctx 透传到位。
//
// 新模型把准入方式本身换掉：
//
//  1. 回合闸门：一轮 = 一枚容量 1 的令牌通道（Session.turn）。Chat / ChatStream
//     先领令牌、再跑整轮、跑完归还；领令牌感知 ctx（排队时 ctx 取消即返回）。同一
//     会话仍然同时只有一轮，但**没有任何会话锁被整轮征用**——用通道代替了锁征用。
//  2. 工作状态短临界区：history / promptBlocks / cfg 这些工作状态由
//     ReActLoop.stateMu 保护，临界区里**绝不**调用模型、工具、回调、发布器或持久化。
//     于是读历史在任何 goroutine、任何时刻都只是一次短暂加锁：
//     Session.History() 永不阻塞。
//  3. 写历史只有两种落点：会话空闲 → 当场落地；回合在飞 → 挂进检查点队列，由循环
//     在下一个安全点落地（工具派发返回后、追加 tool 结果之前；以及每次模型请求之前）。
//     安全点上「在飞 tool_call 单元」的状态与提交时刻一致，所以提交时的那次校验就是
//     全部校验。
//
// 于是环内与环外走的是同一套公开方法、同一份数据：宿主不再需要判断「我此刻在不在
// 环内」，也不需要把 ctx 透传进折叠路径。

// ErrInFlightToolCallDropped 表示这次替换会丢掉正在飞的那一截 tool_call 单元
// （assistant 的 tool_calls 已入历史、其结果尚未 append），因此被拒且历史不动。
//
// 为什么必须拒：紧随其后 append 的 tool 结果会成孤儿，provider 直接拒请求。这不是
// 压缩策略问题，是「不要把正在执行的这一步弄丢」。调用方应把该尾部保留在替换结果里
// 再试一次（Seelex 的 withInFlightTail 做的正是这件事）。
var ErrInFlightToolCallDropped = errors.New("session: replacement would drop the in-flight tool call")

// ErrLoopUnsupported 表示当前 Loop 实现不支持就地替换工作历史（自定义 Loop 未实现
// workingHistory 时，写路径明确报错，不回退成「追加两条」这种近似）。
var ErrLoopUnsupported = errors.New("session: loop does not support in-place history replacement")

// workingHistory 是「回合闸门模型」下 Session 依赖的 Loop 能力：短临界区读历史，
// 加上两种落点的写（空闲立即、忙时排队）。ReActLoop 实现它；自定义 Loop 未实现时，
// 替换报 ErrLoopUnsupported，追加/系统提示退回原有直写路径。
type workingHistory interface {
	History() []types.Message
	ReplaceHistory(history []types.Message) error
	AppendHistory(msg types.Message)
	SetSystemPrompt(prompt string)
}

// historyCommand 是一条排队到下一个检查点落地的工作状态写命令。
type historyCommand struct {
	replace   []types.Message
	append    *types.Message
	systemRaw *string
}

// acquireTurn 领取本会话的回合令牌。闸门被占用时排队等待，ctx 取消即返回错误
// （不把等待方永久扣住：旧实现里第二个 Chat 只能僵在 Mutex 上，取消也没用）。
func (e *Session) acquireTurn(ctx context.Context) error {
	gate := e.turnGate()
	select {
	case gate <- struct{}{}:
		return nil
	default:
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("session: turn gate: %w", ctx.Err())
	}
}

// releaseTurn 归还回合令牌（必须与 acquireTurn 配对，通常 defer）。
func (e *Session) releaseTurn() {
	<-e.turnGate()
}

// turnGate 惰性初始化回合闸门，零值 Session 也能安全使用。
func (e *Session) turnGate() chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.turn == nil {
		e.turn = make(chan struct{}, 1)
	}
	return e.turn
}

// droppedInFlightUnit 返回被替换丢掉的那个在飞 tool_call ID（无则空串）。
//
// 在飞单元 = 当前历史里最后一条带 ToolCalls 的 assistant，且它的某个 call ID 在当前
// 历史中还没有配对的 tool 消息。替换结果里若还留着那条 assistant（ToolCalls 相同），
// 尾部由循环自己续写，不算丢。
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
	for _, msg := range replacement {
		if msg.Role == "assistant" && reflect.DeepEqual(msg.ToolCalls, current[assistantAt].ToolCalls) {
			return ""
		}
	}
	return pending[0]
}