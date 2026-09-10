package session

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

type blockingCompleter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCompleter) Complete(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	content := "done"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func (c *blockingCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(string)) (string, string, []types.ToolCall, error) {
	message, err := c.Complete(ctx, messages, tools)
	if err != nil || message.Content == nil {
		return "", "", nil, err
	}
	return *message.Content, "", nil, nil
}

func (c *blockingCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return c.CompleteStream(ctx, messages, tools, nil)
}

func TestSessionSerializesConcurrentChats(t *testing.T) {
	llm := &blockingCompleter{started: make(chan struct{}), release: make(chan struct{})}
	session, err := NewSession(SessionComponents{Agent: sessionRuntime{llm: llm}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { _, err := session.Chat(context.Background(), "first"); firstDone <- err }()
	<-llm.started
	go func() { _, err := session.Chat(context.Background(), "second"); secondDone <- err }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Chat completed before first turn released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(llm.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Chat() error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Chat() error = %v", err)
	}
}

// TestHistoryIfAvailableDoesNotBlockWhileStreaming 覆盖观测面契约：
// ChatStream 持锁跑整段循环期间，HistoryIfAvailable 必须立刻返回**已发布的
// 检查点快照**，而不是排队等执行面放锁；放锁后又能读到完整权威历史。
// 宿主/UI 的详情读取走这条路径，阻塞它就会表现为"运行中子代理的表格/详情
// 卡住不动"。
func TestHistoryIfAvailableDoesNotBlockWhileStreaming(t *testing.T) {
	llm := &blockingCompleter{started: make(chan struct{}), release: make(chan struct{})}
	session, err := NewSession(SessionComponents{Agent: sessionRuntime{llm: llm}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	streamDone := make(chan error, 1)
	go func() {
		_, err := session.ChatStream(context.Background(), "first", nil)
		streamDone <- err
	}()
	<-llm.started

	startedAt := time.Now()
	history, ok := session.HistoryIfAvailable()
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("HistoryIfAvailable waited %v while the session was busy", elapsed)
	}
	if !ok {
		t.Fatal("HistoryIfAvailable must serve the published checkpoint while streaming")
	}
	// 模型调用前检查点已发布：此刻至少能看到本轮 user 消息，而不是空。
	if len(history) != 1 || history[0].Role != "user" {
		t.Fatalf("published history during streaming = %+v, want the user turn", history)
	}

	close(llm.release)
	if err := <-streamDone; err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}

	// 执行面放锁后，观测面读到的就是权威历史（user + assistant），不再是快照。
	released, ok := session.HistoryIfAvailable()
	if !ok {
		t.Fatal("HistoryIfAvailable must succeed once the session is idle")
	}
	if len(released) != 2 {
		t.Fatalf("history length = %d, want 2", len(released))
	}
	if full := session.History(); len(full) != len(released) {
		t.Fatalf("History() length = %d, HistoryIfAvailable() length = %d", len(full), len(released))
	}
}

// scriptedThenBlockingCompleter 第一轮返回一次 tool_call（让 ReAct 进入第二轮），
// 第二轮阻塞——用来把"流式中途"这个瞬间钉住，检查发布快照是否随检查点推进。
type scriptedThenBlockingCompleter struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newScriptedThenBlockingCompleter() *scriptedThenBlockingCompleter {
	return &scriptedThenBlockingCompleter{entered: make(chan struct{}), release: make(chan struct{})}
}

func (c *scriptedThenBlockingCompleter) Complete(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
	if c.calls.Add(1) == 1 {
		return types.Message{
			Role: "assistant",
			ToolCalls: []types.ToolCall{{
				ID: "call-1", Type: "function",
				Function: types.ToolCallFunction{Name: "probe", Arguments: "{}"},
			}},
		}, nil
	}
	c.once.Do(func() { close(c.entered) })
	<-c.release
	content := "done"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func (c *scriptedThenBlockingCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(string)) (string, string, []types.ToolCall, error) {
	message, err := c.Complete(ctx, messages, tools)
	if err != nil {
		return "", "", nil, err
	}
	if message.Content == nil {
		return "", "", message.ToolCalls, nil
	}
	return *message.Content, "", message.ToolCalls, nil
}

func (c *scriptedThenBlockingCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return c.CompleteStream(ctx, messages, tools, nil)
}

// TestHistoryIfAvailableAdvancesAtEveryCheckpoint 验证发布面不是"只在开头发
// 一次"：第一轮的 assistant 与 tool 结果落历史后都会刷新检查点，因此第二轮
// 流式期间观测面看到的是 user + assistant + tool 三条，而不是停在开头。
func TestHistoryIfAvailableAdvancesAtEveryCheckpoint(t *testing.T) {
	llm := newScriptedThenBlockingCompleter()
	session, err := NewSession(SessionComponents{Agent: sessionRuntime{llm: llm}})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	streamDone := make(chan error, 1)
	go func() {
		_, err := session.ChatStream(context.Background(), "first", func(string) {})
		streamDone <- err
	}()
	<-llm.entered

	history, ok := session.HistoryIfAvailable()
	if !ok {
		t.Fatal("HistoryIfAvailable must serve the published checkpoint while streaming")
	}
	if len(history) != 3 {
		t.Fatalf("published history length = %d, want 3 (user/assistant/tool)", len(history))
	}
	if history[0].Role != "user" || history[1].Role != "assistant" || history[2].Role != "tool" {
		t.Fatalf("published history roles = %s/%s/%s, want user/assistant/tool", history[0].Role, history[1].Role, history[2].Role)
	}

	close(llm.release)
	if err := <-streamDone; err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
}
