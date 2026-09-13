package limits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// scriptedCompleter 是可编排的 types.ChatCompleter 替身：记录调用次数并按
// 脚本返回结果，用来验证装饰器的准入、重试与结算行为。
type scriptedCompleter struct {
	mu sync.Mutex

	completeFunc func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
	streamFunc   func(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error)
	eventsFunc   func(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error)

	completeCalls int
	streamCalls   int
	eventsCalls   int
}

func (s *scriptedCompleter) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	s.mu.Lock()
	s.completeCalls++
	fn := s.completeFunc
	s.mu.Unlock()
	if fn == nil {
		return types.Message{}, nil
	}
	return fn(ctx, messages, tools)
}

func (s *scriptedCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
	s.mu.Lock()
	s.streamCalls++
	fn := s.streamFunc
	s.mu.Unlock()
	if fn == nil {
		return "ok", "", nil, nil
	}
	return fn(ctx, messages, tools, onChunk)
}

func (s *scriptedCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	s.mu.Lock()
	s.eventsCalls++
	fn := s.eventsFunc
	s.mu.Unlock()
	if fn == nil {
		return "ok", "", nil, nil
	}
	return fn(ctx, messages, tools, onEvent)
}

func (s *scriptedCompleter) calls() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeCalls, s.streamCalls, s.eventsCalls
}

var _ types.ChatCompleter = (*scriptedCompleter)(nil)

// fastRetryParams 是没有等待的重试参数，让重试测试保持毫秒级。
func fastRetryParams(attempts int) Params {
	return Params{
		Enabled:        true,
		MaxConcurrency: 2,
		PerKey:         true,
		Retry: RetryPolicy{
			MaxAttempts: attempts,
			BaseDelay:   Duration(time.Millisecond),
			MaxDelay:    Duration(10 * time.Millisecond),
		},
	}
}

func statusError(status int, retryAfter time.Duration) error {
	error := types.NewHTTPStatusError(status, []byte(`{"error":"rate limited"}`), nil)
	error.RetryAfter = retryAfter
	return error
}

func TestWrapperSuccessSettlesUsage(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		content := "pong"
		return types.Message{Role: "assistant", Content: &content, Usage: &types.Usage{PromptTokens: 30, CompletionTokens: 5, TotalTokens: 35}}, nil
	}
	assembly, err := Assemble(fastRetryParams(1), WithEstimator(func([]types.Message, []types.Tool) Cost {
		return Cost{Requests: 1, InputTokens: 1000}
	}))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	message, err := wrapped.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if message.Content == nil || *message.Content != "pong" {
		t.Fatalf("返回值被破坏: %+v", message)
	}
	stats := assembly.Snapshot().Stats["agent-1"]
	if stats.Admitted != 1 || stats.Completed != 1 || stats.InFlight != 0 {
		t.Errorf("准入统计 = %+v", stats)
	}
	if stats.Estimated != 1000 {
		t.Errorf("Estimated = %d, want 1000", stats.Estimated)
	}
	if stats.Actual != 35 {
		t.Errorf("Actual = %d, want 35（真实用量结算）", stats.Actual)
	}
}

func TestWrapperRetriesRetryableStatus(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		if complete := completer.callsComplete(); complete < 3 {
			return types.Message{}, statusError(429, 0)
		}
		content := "finally"
		return types.Message{Role: "assistant", Content: &content}, nil
	}
	assembly, err := Assemble(fastRetryParams(3))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	message, err := wrapped.Complete(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("重试后应成功：%v", err)
	}
	if message.Content == nil || *message.Content != "finally" {
		t.Fatalf("返回值 = %+v", message)
	}
	if complete, _, _ := completer.calls(); complete != 3 {
		t.Errorf("调用次数 = %d, want 3", complete)
	}
	stats := assembly.Snapshot().Stats["agent-1"]
	if stats.Retried != 2 {
		t.Errorf("Retried = %d, want 2", stats.Retried)
	}
	// 每次尝试都重新走准入：3 次尝试 → 3 次准入。
	if stats.Admitted != 3 || stats.Completed != 3 {
		t.Errorf("Admitted/Completed = %d/%d, want 3/3", stats.Admitted, stats.Completed)
	}
}

func TestWrapperStopsAfterMaxAttempts(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		return types.Message{}, statusError(503, 0)
	}
	assembly, err := Assemble(fastRetryParams(2))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	_, err = wrapped.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("重试用尽后必须返回错误")
	}
	if status, ok := StatusOf(err); !ok || status != 503 {
		t.Fatalf("错误应保留原始状态码，实际 %v", err)
	}
	if complete, _, _ := completer.calls(); complete != 2 {
		t.Errorf("调用次数 = %d, want 2", complete)
	}
}

func TestWrapperDoesNotRetryNonRetryable(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		return types.Message{}, statusError(400, 0)
	}
	assembly, err := Assemble(fastRetryParams(5))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	if _, err = wrapped.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("400 应当失败")
	}
	if complete, _, _ := completer.calls(); complete != 1 {
		t.Errorf("不可重试错误只应调用一次，实际 %d", complete)
	}
}

func TestWrapperHonorsRetryAfter(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		if complete := completer.callsComplete(); complete == 1 {
			return types.Message{}, statusError(429, 120*time.Millisecond)
		}
		return types.Message{Role: "assistant"}, nil
	}
	params := fastRetryParams(2)
	params.Retry.IgnoreRetryAfter = false         // 默认即尊重 Retry-After
	params.Retry.MaxDelay = Duration(time.Second) // 让 Retry-After 不被上限夹掉
	assembly, err := Assemble(params)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	start := time.Now()
	if _, err = wrapped.Complete(context.Background(), nil, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("应至少等待 Retry-After，实际 %s", elapsed)
	}
}

func TestWrapperCallerCancellationWins(t *testing.T) {
	completer := &scriptedCompleter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 第一次尝试失败并同时取消调用方上下文：重试必须立刻放弃。
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		cancel()
		return types.Message{}, statusError(429, 0)
	}
	assembly, err := Assemble(fastRetryParams(3))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	_, err = wrapped.Complete(ctx, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if complete, _, _ := completer.calls(); complete != 1 {
		t.Errorf("调用方取消后不应重试，调用次数 = %d", complete)
	}
}

func TestWrapperRejectsBeforeCallingOnCanceledContext(t *testing.T) {
	completer := &scriptedCompleter{}
	assembly, err := Assemble(Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: Duration(time.Second)})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = wrapped.Complete(ctx, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if complete, _, _ := completer.calls(); complete != 0 {
		t.Errorf("已取消的上下文不得打到 provider，调用次数 = %d", complete)
	}
}

func TestWrapperAdmissionRejectionSkipsInnerCall(t *testing.T) {
	completer := &scriptedCompleter{}
	assembly, err := Assemble(Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: 0})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	gate, err := assembly.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	held, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	defer held.Release()

	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	_, err = wrapped.Complete(context.Background(), nil, nil)
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want ErrQueueTimeout", err)
	}
	if complete, _, _ := completer.calls(); complete != 0 {
		t.Errorf("被拒绝的请求不得打到 provider，调用次数 = %d", complete)
	}
}

func TestWrapperStreamDoesNotReplayAfterEmission(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.streamFunc = func(_ context.Context, _ []types.Message, _ []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
		onChunk("部分")
		onChunk("输出")
		return "部分输出", "", nil, statusError(429, 0)
	}
	assembly, err := Assemble(fastRetryParams(3))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	var chunks []string
	content, _, _, err := wrapped.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err == nil {
		t.Fatal("已输出后失败必须返回错误")
	}
	if content != "部分输出" {
		t.Errorf("部分内容应被返回，实际 %q", content)
	}
	if len(chunks) != 2 {
		t.Errorf("增量不应重复：%v", chunks)
	}
	if _, stream, _ := completer.calls(); stream != 1 {
		t.Errorf("已输出后不得重试，调用次数 = %d", stream)
	}
}

func TestWrapperStreamRetriesBeforeEmission(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.streamFunc = func(_ context.Context, _ []types.Message, _ []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
		if _, stream, _ := completer.calls(); stream == 1 {
			return "", "", nil, statusError(429, 0)
		}
		onChunk("重试成功")
		return "重试成功", "", nil, nil
	}
	assembly, err := Assemble(fastRetryParams(2))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	var chunks []string
	content, _, _, err := wrapped.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err != nil {
		t.Fatalf("未输出前失败应可重试：%v", err)
	}
	if content != "重试成功" || len(chunks) != 1 {
		t.Fatalf("content = %q, chunks = %v", content, chunks)
	}
	if _, stream, _ := completer.calls(); stream != 2 {
		t.Errorf("调用次数 = %d, want 2", stream)
	}
}

func TestWrapperStreamEventsSameRules(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.eventsFunc = func(_ context.Context, _ []types.Message, _ []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
		if _, _, events := completer.calls(); events == 1 {
			return "", "", nil, statusError(503, 0)
		}
		onEvent(types.StreamEvent{Type: types.StreamEventText, Content: "事件流"})
		return "事件流", "", nil, nil
	}
	assembly, err := Assemble(fastRetryParams(2))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}

	var events []types.StreamEvent
	content, _, _, err := wrapped.CompleteStreamEvents(context.Background(), nil, nil, func(event types.StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("事件流重试失败: %v", err)
	}
	if content != "事件流" || len(events) != 1 {
		t.Fatalf("content = %q, events = %d", content, len(events))
	}

	// 已输出事件后失败：不重试。
	emitted := &scriptedCompleter{}
	emitted.eventsFunc = func(_ context.Context, _ []types.Message, _ []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
		onEvent(types.StreamEvent{Type: types.StreamEventText, Content: "开始"})
		return "开始", "", nil, statusError(503, 0)
	}
	wrapped, err = assembly.WrapFor("agent-2", emitted)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	if _, _, _, err = wrapped.CompleteStreamEvents(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("已输出事件后失败必须报错")
	}
	if _, _, events := emitted.calls(); events != 1 {
		t.Errorf("已输出事件后不得重试，调用次数 = %d", events)
	}
}

func TestWrapperDisabledAssemblyStillRetries(t *testing.T) {
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		if complete := completer.callsComplete(); complete == 1 {
			return types.Message{}, statusError(429, 0)
		}
		return types.Message{Role: "assistant"}, nil
	}
	params := fastRetryParams(2)
	params.Enabled = false
	assembly, err := Assemble(params)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	if _, err = wrapped.Complete(context.Background(), nil, nil); err != nil {
		t.Fatalf("旁路 + 重试应成功：%v", err)
	}
	if complete, _, _ := completer.calls(); complete != 2 {
		t.Errorf("旁路不应关闭重试，调用次数 = %d, want 2", complete)
	}
}

// callsComplete 供脚本函数在内部读取当前调用次数（避免与 calls() 的锁重入）。
func (s *scriptedCompleter) callsComplete() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completeCalls
}
