package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// 本文件钉住「回合闸门 + 工作状态短临界区」这一版模型的行为（见 turn.go）：
//
//  1. 环内（工具 handler / LoopHooks / ContextController）读历史**立刻返回**，
//     不再需要任何注入 ctx 的环内把手；
//  2. 环内替换走普通公开方法：会话在飞时排队到循环的下一个安全检查点，同回合的
//     下一次模型请求读到的就是替换结果；
//  3. 会丢掉在飞 tool_call 单元的替换仍被拒（ErrInFlightToolCallDropped）且历史不动；
//  4. 空闲路径当场生效；
//  5. 回合闸门串行同一会话的回合，且感知 ctx（排队时取消即返回，不僵在锁上）。

// scriptedTurnCompleter：第 1 次请求返回一个工具调用，之后返回收尾正文；记录每次请求
// 实际收到的消息序列。
type scriptedTurnCompleter struct {
	mu       sync.Mutex
	toolName string
	turns    int
	requests [][]types.Message
	entered  chan struct{}
	release  chan struct{}
}

func (c *scriptedTurnCompleter) next(messages []types.Message) (string, []types.ToolCall) {
	c.mu.Lock()
	c.turns++
	turn := c.turns
	c.requests = append(c.requests, append([]types.Message(nil), messages...))
	c.mu.Unlock()
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	if c.release != nil {
		<-c.release
	}
	if turn == 1 {
		return "", []types.ToolCall{{
			ID: "call-1", Type: "function",
			Function: types.ToolCallFunction{Name: c.toolName, Arguments: "{}"},
		}}
	}
	return "done", nil
}

func (c *scriptedTurnCompleter) Complete(_ context.Context, messages []types.Message, _ []types.Tool) (types.Message, error) {
	content, calls := c.next(messages)
	return types.Message{Role: "assistant", Content: &content, ToolCalls: calls}, nil
}

func (c *scriptedTurnCompleter) CompleteStream(_ context.Context, messages []types.Message, _ []types.Tool, _ func(string)) (string, string, []types.ToolCall, error) {
	content, calls := c.next(messages)
	return content, "", calls, nil
}

func (c *scriptedTurnCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return c.CompleteStream(ctx, messages, tools, nil)
}

func (c *scriptedTurnCompleter) snapshot() [][]types.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]types.Message(nil), c.requests...)
}

func requestHas(messages []types.Message, want string) bool {
	for _, message := range messages {
		if message.Content != nil && strings.Contains(*message.Content, want) {
			return true
		}
	}
	return false
}

// turnProbeAgent 在一次工具派发里跑宿主探针（与 ChatStream 同一 goroutine），探针
// 返回值即工具结果正文——折叠路径就长这样。
type turnProbeAgent struct {
	llm      types.ChatCompleter
	toolName string
	session  **Session
	probe    func(sess *Session) string
}

func (a turnProbeAgent) VisibleTools(context.Context) []types.Tool {
	return []types.Tool{{Type: "function", Function: types.ToolFunction{
		Name: a.toolName, Description: "probe", Parameters: map[string]any{"type": "object"},
	}}}
}

func (a turnProbeAgent) Dispatch(_ context.Context, name, _ string) (string, error) {
	if name != a.toolName {
		return "", nil
	}
	return a.probe(*a.session), nil
}

func (a turnProbeAgent) LLM() types.ChatCompleter { return a.llm }

// newTurnSession 起一个真实 Session（回合闸门与工作状态只在它身上存在）。
func newTurnSession(t *testing.T, probe func(sess *Session) string, hooks *LoopHooks) (*Session, *scriptedTurnCompleter) {
	t.Helper()
	completer := &scriptedTurnCompleter{toolName: "compact_context"}
	var session *Session
	agent := turnProbeAgent{llm: completer, toolName: "compact_context", session: &session, probe: probe}
	created, err := NewSession(SessionComponents{Agent: agent, Hooks: hooks})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session = created
	return created, completer
}

// runTurn 跑一轮 ChatStream 并要求在超时内收尾（超时即视为自锁回归）。
func runTurn(t *testing.T, session *Session, input string) string {
	t.Helper()
	done := make(chan struct{})
	var reply string
	var err error
	go func() {
		reply, err = session.ChatStream(context.Background(), input, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("回合没有在超时内收尾：环内调用又自锁了（回合持上了跨整轮的锁）")
	}
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	return reply
}

// TestHistoryFromToolHandlerReturnsPromptly 钉住回合内读历史立刻返回：回合不再持有
// 跨整轮的会话锁，工具 handler 读到的就是当前工作历史（旧实现在这里永久自锁，只能靠
// ctx 把手绕开）。
func TestHistoryFromToolHandlerReturnsPromptly(t *testing.T) {
	var observed int
	session, _ := newTurnSession(t, func(sess *Session) string {
		observed = len(sess.History())
		return "probe returned"
	}, nil)

	if reply := runTurn(t, session, "go"); reply != "done" {
		t.Fatalf("reply = %q, want done", reply)
	}
	// 探针触发时历史里应有 user("go") + assistant(tool_calls)。
	if observed < 2 {
		t.Fatalf("回合内读到的历史长度 = %d, want >= 2", observed)
	}
}

// foldProduct 返回一次折叠产物：压缩帧 + 当前历史里正在飞的那一截（assistant 带
// tool_calls、其结果尚未 append）。尾部必须保留，否则引擎拒收。
func foldProduct(history []types.Message) []types.Message {
	folded := []types.Message{{Role: "user", Content: stringPointer("COMPACTED-FRAME")}}
	for _, message := range history {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			folded = append(folded, message)
		}
	}
	return folded
}

func stringPointer(value string) *string { return &value }

// TestReplaceHistoryInsideTurnTakesEffectInSameTurn 是要交付的行为：回合内的工具
// handler 用普通公开方法提交替换（不需要环内把手、不需要 ctx 透传），替换在循环的
// 下一个检查点落地，同回合的下一次请求读到的已是折叠后的历史。
func TestReplaceHistoryInsideTurnTakesEffectInSameTurn(t *testing.T) {
	var (
		readCount int
		writeErr  error
	)
	session, completer := newTurnSession(t, func(sess *Session) string {
		history := sess.History()
		readCount = len(history)
		writeErr = sess.ReplaceHistory(foldProduct(history))
		return "folded in-turn"
	}, nil)

	if reply := runTurn(t, session, "go"); reply != "done" {
		t.Fatalf("reply = %q, want done", reply)
	}
	if readCount != 2 {
		t.Fatalf("回合内读到的历史长度 = %d, want 2（user + assistant tool_calls）", readCount)
	}
	if writeErr != nil {
		t.Fatalf("回合内替换被拒：%v", writeErr)
	}
	requests := completer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("模型调用次数 = %d, want 2", len(requests))
	}
	if requestHas(requests[0], "COMPACTED-FRAME") {
		t.Fatal("折叠不该出现在第一次请求里")
	}
	if !requestHas(requests[1], "COMPACTED-FRAME") {
		t.Fatal("回合内替换未即时生效：同回合的下一次请求看不到折叠帧")
	}
	if requestHas(requests[1], "go") {
		t.Fatal("折叠后旧输入仍在请求里")
	}
	final := session.History()
	// 帧 + 在飞 assistant + 循环随后 append 的 tool 结果 + 收尾 assistant。
	if len(final) != 4 {
		t.Fatalf("终态历史长度 = %d, want 4：%+v", len(final), final)
	}
	if final[0].Content == nil || *final[0].Content != "COMPACTED-FRAME" {
		t.Fatalf("折叠帧没有落在历史首位：%+v", final[0])
	}
	if final[1].Role != "assistant" || len(final[1].ToolCalls) == 0 {
		t.Fatal("在飞 assistant 被丢了")
	}
	if final[2].Role != "tool" || final[2].ToolCallID != "call-1" {
		t.Fatalf("tool 结果成孤儿：role=%s callID=%s", final[2].Role, final[2].ToolCallID)
	}
}

// TestReplaceHistoryDropsInFlightTailIsRefused 钉住下界：替换若会丢掉正在飞的
// tool_call 单元，必须被拒且不改历史——否则紧随其后 append 的结果行成孤儿。
func TestReplaceHistoryDropsInFlightTailIsRefused(t *testing.T) {
	var (
		refuseErr  error
		lenAfter   int
		restoreErr error
	)
	session, _ := newTurnSession(t, func(sess *Session) string {
		history := sess.History()
		refuseErr = sess.ReplaceHistory([]types.Message{{Role: "user", Content: stringPointer("COMPACTED-FRAME")}})
		lenAfter = len(sess.History())
		restoreErr = sess.ReplaceHistory(foldProduct(history))
		return "tail preserved"
	}, nil)

	if reply := runTurn(t, session, "go"); reply != "done" {
		t.Fatalf("reply = %q, want done", reply)
	}
	if refuseErr == nil {
		t.Fatal("丢掉在飞尾部的替换被接受了（下界失效）")
	}
	if !errors.Is(refuseErr, ErrInFlightToolCallDropped) {
		t.Fatalf("拒收理由不是「会丢在飞 tool_call 单元」：%v", refuseErr)
	}
	if lenAfter != 2 {
		t.Fatalf("被拒的替换改动了历史：len=%d want 2", lenAfter)
	}
	if restoreErr != nil {
		t.Fatalf("保留尾部的替换被拒：%v", restoreErr)
	}
}

// TestReplaceHistoryOutsideTurnAppliesImmediately 是空闲路径的对照：没有回合在飞时
// 替换当场落地（不需要 ctx、不需要把手）。
func TestReplaceHistoryOutsideTurnAppliesImmediately(t *testing.T) {
	completer := &scriptedTurnCompleter{toolName: "compact_context"}
	session, err := NewSession(SessionComponents{Agent: turnProbeAgent{
		llm: completer, toolName: "compact_context", probe: func(*Session) string { return "" },
	}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session.AppendHistory(types.Message{Role: "user", Content: stringPointer("seed")})
	if err := session.ReplaceHistory([]types.Message{{Role: "user", Content: stringPointer("COMPACTED-FRAME")}}); err != nil {
		t.Fatalf("空闲替换: %v", err)
	}
	history := session.History()
	if len(history) != 1 || history[0].Content == nil || *history[0].Content != "COMPACTED-FRAME" {
		t.Fatalf("空闲替换没有当场生效：%+v", history)
	}
}

// TestInTurnAppendAndSetSystemPromptDoNotSelfLock 钉住「环内写历史不自锁」：LoopHooks
// 与工具 handler 同 goroutine，写的是普通公开方法；命令排队到下一个检查点落地，本回合
// 的下一次请求就能看到。
func TestInTurnAppendAndSetSystemPromptDoNotSelfLock(t *testing.T) {
	completer := &scriptedTurnCompleter{toolName: "compact_context"}
	var session *Session
	hooks := &LoopHooks{OnIterationComplete: func(_ context.Context, _ int) bool {
		session.AppendHistory(types.Message{Role: "user", Content: stringPointer("INJECTED-NOTE")})
		session.SetSystemPrompt("REPLACED-SYSTEM")
		_ = session.History()
		return true
	}}
	agent := turnProbeAgent{llm: completer, toolName: "compact_context", session: &session, probe: func(*Session) string { return "ok" }}
	created, err := NewSession(SessionComponents{Agent: agent, Hooks: hooks})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session = created

	if reply := runTurn(t, session, "go"); reply != "done" {
		t.Fatalf("reply = %q, want done", reply)
	}
	requests := completer.snapshot()
	if len(requests) != 2 {
		t.Fatalf("模型调用次数 = %d, want 2", len(requests))
	}
	if !requestHas(requests[1], "INJECTED-NOTE") {
		t.Fatal("回合内追加的消息没有在下一个检查点落地")
	}
	if !requestHas(requests[1], "REPLACED-SYSTEM") {
		t.Fatal("回合内替换的 system prompt 没有在下一个检查点落地")
	}
}

// TestTurnGateSerializesAndHonorsContext 钉住闸门的两条性质：同一会话的回合串行；排队
// 领令牌的调用方在 ctx 取消时立刻返回（旧实现里第二个 Chat 只能僵在 Mutex 上）。
func TestTurnGateSerializesAndHonorsContext(t *testing.T) {
	release := make(chan struct{})
	completer := &scriptedTurnCompleter{
		toolName: "none", entered: make(chan struct{}, 1), release: release,
	}
	var session *Session
	created, err := NewSession(SessionComponents{Agent: turnProbeAgent{
		llm: completer, toolName: "none", session: &session, probe: func(*Session) string { return "" },
	}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	session = created
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	first := make(chan string, 1)
	go func() {
		reply, _ := session.Chat(context.Background(), "first")
		first <- reply
	}()
	select {
	case <-completer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("第一轮没有进入模型调用")
	}

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := session.Chat(ctx, "second")
		second <- err
	}()
	cancel()
	select {
	case err := <-second:
		if err == nil {
			t.Fatal("排队等闸门的回合没有被 ctx 取消")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后排队方仍然僵在闸门上")
	}

	close(release)
	select {
	case <-first:
	case <-time.After(10 * time.Second):
		t.Fatal("第一轮没有收尾")
	}
	for _, request := range completer.snapshot() {
		if requestHas(request, "second") {
			t.Fatal("被 ctx 取消的回合仍然跑进了模型调用")
		}
	}
}