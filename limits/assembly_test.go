package limits

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

func TestAssembleValidatesParams(t *testing.T) {
	if _, err := Assemble(Params{MaxConcurrency: -1}); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("err = %v, want ErrInvalidParams", err)
	}
	assembly, err := Assemble(DefaultParams())
	if err != nil {
		t.Fatalf("Assemble(DefaultParams): %v", err)
	}
	if !assembly.Enabled() {
		t.Error("默认参数应当启用")
	}
	if keys := assembly.Keys(); len(keys) != 0 {
		t.Errorf("未使用时不应创建 gate，实际 %v", keys)
	}
}

func TestAssemblyGateKeyMapping(t *testing.T) {
	perKey, err := Assemble(DefaultParams().WithPerKey(true))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	first, err := perKey.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	second, err := perKey.Gate("agent-2")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if first == second {
		t.Fatal("PerKey=true 时不同 key 必须得到不同 gate")
	}
	again, err := perKey.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if again != first {
		t.Fatal("同一 key 必须复用同一个 gate")
	}

	shared, err := Assemble(DefaultParams().WithPerKey(false))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	a, err := shared.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	b, err := shared.Gate("agent-2")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if a != b {
		t.Fatal("PerKey=false 时所有 key 必须共享一个 gate")
	}
	if a.Key() != sharedKey {
		t.Errorf("共享 gate 的 key = %q, want %q", a.Key(), sharedKey)
	}

	empty, err := perKey.Gate("")
	if err != nil {
		t.Fatalf("Gate(\"\"): %v", err)
	}
	if empty.Key() != sharedKey {
		t.Errorf("空 key 应映射为 %q，实际 %q", sharedKey, empty.Key())
	}
	if keys := perKey.Keys(); len(keys) != 3 {
		t.Errorf("Keys() = %v, want 3 个", keys)
	}
}

func TestAssemblySetParamsAppliesToEveryGate(t *testing.T) {
	assembly, err := Assemble(DefaultParams().WithPerKey(true))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	for _, key := range []string{"agent-1", "agent-2"} {
		if _, err := assembly.Gate(key); err != nil {
			t.Fatalf("Gate(%s): %v", key, err)
		}
	}

	if err := assembly.SetConcurrency(7); err != nil {
		t.Fatalf("SetConcurrency: %v", err)
	}
	if err := assembly.SetRate(120, 240000); err != nil {
		t.Fatalf("SetRate: %v", err)
	}
	snapshot := assembly.Snapshot()
	for key, stats := range snapshot.Stats {
		if stats.MaxConcurrency != 7 {
			t.Errorf("%s: MaxConcurrency = %d, want 7", key, stats.MaxConcurrency)
		}
		if stats.RequestsPerMin != 120 || stats.TokensPerMin != 240000 {
			t.Errorf("%s: 速率未生效 %+v", key, stats)
		}
	}
	if snapshot.Params.Burst != 20 || snapshot.Params.TokenBurst != 40000 {
		t.Errorf("速率变更应重算桶容量，实际 %d/%d", snapshot.Params.Burst, snapshot.Params.TokenBurst)
	}

	// 单 key 覆盖只影响该 key。
	if err := assembly.SetKeyParams("agent-2", DefaultParams().WithConcurrency(1)); err != nil {
		t.Fatalf("SetKeyParams: %v", err)
	}
	snapshot = assembly.Snapshot()
	if snapshot.Stats["agent-2"].MaxConcurrency != 1 {
		t.Errorf("agent-2 覆盖未生效: %+v", snapshot.Stats["agent-2"])
	}
	if snapshot.Stats["agent-1"].MaxConcurrency != 7 {
		t.Errorf("agent-1 不应受单 key 覆盖影响: %+v", snapshot.Stats["agent-1"])
	}

	// 全局 SetParams 覆盖所有 key（含此前单 key 覆盖）。
	if err := assembly.SetParams(DefaultParams().WithConcurrency(3)); err != nil {
		t.Fatalf("SetParams: %v", err)
	}
	snapshot = assembly.Snapshot()
	for key, stats := range snapshot.Stats {
		if stats.MaxConcurrency != 3 {
			t.Errorf("%s: 全局覆盖后 MaxConcurrency = %d, want 3", key, stats.MaxConcurrency)
		}
	}
}

func TestAssemblySettersRejectInvalid(t *testing.T) {
	assembly, err := Assemble(DefaultParams())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if err := assembly.SetConcurrency(-2); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetConcurrency(-2) = %v", err)
	}
	if err := assembly.SetRate(10, -1); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetRate(10,-1) = %v", err)
	}
	if err := assembly.SetImageWeight(-0.5); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetImageWeight(-0.5) = %v", err)
	}
	if err := assembly.SetQueueTimeout(-time.Second); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetQueueTimeout(-1s) = %v", err)
	}
	if err := assembly.SetRetry(RetryPolicy{MaxAttempts: 2, Jitter: 2}); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetRetry(jitter=2) = %v", err)
	}
	if err := assembly.SetParams(Params{MaxConcurrency: -1}); !errors.Is(err, ErrInvalidParams) {
		t.Errorf("SetParams(invalid) = %v", err)
	}
	params := assembly.Params()
	if params.MaxConcurrency != DefaultParams().MaxConcurrency {
		t.Errorf("无效设置不应改变参数: %+v", params)
	}
}

func TestAssemblySetEnabledAppliesToEveryGate(t *testing.T) {
	assembly, err := Assemble(DefaultParams().WithPerKey(true))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	gate, err := assembly.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	if err := assembly.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if gate.Stats().Enabled {
		t.Fatal("SetEnabled(false) 后 gate 应处于旁路状态")
	}
	permit, err := gate.Acquire(context.Background(), Cost{InputTokens: 1 << 20})
	if err != nil {
		t.Fatalf("旁路状态下应放行：%v", err)
	}
	permit.Release()
	if assembly.Enabled() {
		t.Error("Assembly.Enabled() 应为 false")
	}
}

func TestAssemblyInjectedClockDrivesBuckets(t *testing.T) {
	clock := newFakeClock()
	assembly, err := Assemble(
		Params{Enabled: true, RequestsPerMin: 60, Burst: 1, MaxConcurrency: 0},
		WithClock(clock.Now),
	)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	gate, err := assembly.Gate("agent-1")
	if err != nil {
		t.Fatalf("Gate: %v", err)
	}
	permit, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	permit.Release()
	if level := gate.Stats().RequestLevel; level != 0 {
		t.Fatalf("请求桶水位 = %v, want 0", level)
	}

	// 只有注入的时钟能补齐额度：说明 gate 用的是装配级时钟。
	clock.Advance(time.Minute)
	if level := gate.Stats().RequestLevel; level != 1 {
		t.Fatalf("推进注入时钟后水位 = %v, want 1", level)
	}
}

func TestAssemblyEstimatorOverride(t *testing.T) {
	assembly, err := Assemble(DefaultParams(), WithEstimator(func([]types.Message, []types.Tool) Cost {
		return Cost{Requests: 1, InputTokens: 4096, Images: 2}
	}))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	cost := assembly.Cost([]types.Message{{Role: "user"}}, nil)
	if cost.InputTokens != 4096 || cost.Images != 2 {
		t.Fatalf("自定义估算器未生效: %+v", cost)
	}

	assembly.SetEstimator(nil)
	if cost := assembly.Cost(nil, nil); cost.Requests != 1 {
		t.Fatalf("SetEstimator(nil) 不应清空估算器: %+v", cost)
	}
	assembly.SetEstimator(func([]types.Message, []types.Tool) Cost { return Cost{} })
	if cost := assembly.Cost(nil, nil); cost.Requests != 1 || cost.InputTokens != 0 {
		t.Fatalf("零值估算应被归一化为一次请求: %+v", cost)
	}
}

func TestDefaultEstimatorCountsTextAndTools(t *testing.T) {
	content := "hello world"
	message := types.Message{Role: "user", Content: &content}
	cost := DefaultEstimator([]types.Message{message}, nil)
	if cost.Requests != 1 {
		t.Errorf("Requests = %d, want 1", cost.Requests)
	}
	if cost.InputTokens != EstimateTextTokens(content) {
		t.Errorf("InputTokens = %d, want %d", cost.InputTokens, EstimateTextTokens(content))
	}

	withTool := DefaultEstimator(nil, []types.Tool{{
		Type: "function",
		Function: types.ToolFunction{
			Name:        "read_file",
			Description: "读取工作区文件内容",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}})
	if withTool.InputTokens <= 0 {
		t.Errorf("工具 schema 应计入估算，实际 %d", withTool.InputTokens)
	}
}

func TestEstimateTextTokensBoundaries(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "空串", text: "", want: 0},
		{name: "四字符 ASCII 一个 token", text: "abcd", want: 1},
		{name: "两字符向上取整", text: "ab", want: 1},
		{name: "每个 CJK 一个 token", text: "中文测试", want: 4},
		{name: "中英混合", text: "中文ab", want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := EstimateTextTokens(test.text); got != test.want {
				t.Errorf("EstimateTextTokens(%q) = %d, want %d", test.text, got, test.want)
			}
		})
	}
}

func TestAssemblyWrapValidation(t *testing.T) {
	assembly, err := Assemble(DefaultParams())
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if _, err := assembly.Wrap(nil); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("Wrap(nil) = %v, want ErrInvalidParams", err)
	}
	if _, err := assembly.WrapFor("agent-1", nil); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("WrapFor(nil) = %v", err)
	}
	if _, err := assembly.WrapComplete("agent-1", nil); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("WrapComplete(nil) = %v", err)
	}
	if _, err := assembly.WrapAll(map[string]types.ChatCompleter{"agent-1": nil}); err == nil {
		t.Fatal("WrapAll 遇到 nil completer 应报错")
	}
	if _, err := assembly.WrapAllComplete(map[string]CompleteOnly{"agent-1": nil}); err == nil {
		t.Fatal("WrapAllComplete 遇到 nil completer 应报错")
	}

	wrapped, err := assembly.WrapFor("agent-1", &scriptedCompleter{})
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	if _, ok := wrapped.(*wrapper); !ok {
		t.Fatalf("WrapFor 应返回装饰器，实际 %T", wrapped)
	}

	batch, err := assembly.WrapAll(map[string]types.ChatCompleter{
		"agent-1": &scriptedCompleter{},
		"agent-2": &scriptedCompleter{},
	})
	if err != nil {
		t.Fatalf("WrapAll: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("WrapAll 返回 %d 个，want 2", len(batch))
	}
	if _, ok := batch["agent-1"]; !ok {
		t.Error("WrapAll 必须保留输入 key")
	}
	if _, err := assembly.Gate("agent-1"); err != nil {
		t.Fatalf("Gate: %v", err)
	}
}

// completeOnlyCompleter 只实现 Complete，用来模拟 seelex 的 agent.Completer 形状。
type completeOnlyCompleter struct {
	calls int
}

func (c *completeOnlyCompleter) Complete(context.Context, []types.Message, []types.Tool) (types.Message, error) {
	c.calls++
	content := "同步回复"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func TestAssemblyWrapCompleteRecoversRealStreaming(t *testing.T) {
	assembly, err := Assemble(DefaultParams().WithPerKey(true))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	full := &scriptedCompleter{}
	full.streamFunc = func(_ context.Context, _ []types.Message, _ []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
		onChunk("真流式")
		return "真流式", "", nil, nil
	}

	wrapped, err := assembly.WrapComplete("agent-1", full)
	if err != nil {
		t.Fatalf("WrapComplete: %v", err)
	}
	var chunks []string
	content, _, _, err := wrapped.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if content != "真流式" || len(chunks) != 1 {
		t.Fatalf("content = %q, chunks = %v", content, chunks)
	}
	if _, stream, _ := full.calls(); stream != 1 {
		t.Errorf("具体实现具备流式能力时必须走真流式，stream 调用 = %d", stream)
	}
}

func TestAssemblyWrapCompleteFallsBackToSingleChunk(t *testing.T) {
	assembly, err := Assemble(DefaultParams().WithPerKey(true))
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	narrow := &completeOnlyCompleter{}

	wrapped, err := assembly.WrapComplete("agent-1", narrow)
	if err != nil {
		t.Fatalf("WrapComplete: %v", err)
	}
	if _, ok := wrapped.(types.ChatCompleter); !ok {
		t.Fatal("WrapComplete 必须返回完整 ChatCompleter")
	}

	var chunks []string
	content, reasoning, _, err := wrapped.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if content != "同步回复" || reasoning != "" {
		t.Fatalf("回退结果 = %q/%q", content, reasoning)
	}
	if len(chunks) != 1 || chunks[0] != "同步回复" {
		t.Fatalf("回退应产生单个增量，实际 %v", chunks)
	}

	var events []types.StreamEvent
	content, _, _, err = wrapped.CompleteStreamEvents(context.Background(), nil, nil, func(event types.StreamEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatalf("CompleteStreamEvents: %v", err)
	}
	if content != "同步回复" || len(events) != 1 || events[0].Type != types.StreamEventText {
		t.Fatalf("事件回退 = %q / %+v", content, events)
	}
	if narrow.calls != 2 {
		t.Errorf("回退路径调用 Complete 次数 = %d, want 2", narrow.calls)
	}

	batch, err := assembly.WrapAllComplete(map[string]CompleteOnly{"agent-2": &completeOnlyCompleter{}})
	if err != nil {
		t.Fatalf("WrapAllComplete: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("WrapAllComplete 返回 %d 个", len(batch))
	}
}

func TestAssemblyObserverReceivesEvents(t *testing.T) {
	var observations []Observation
	assembly, err := Assemble(
		DefaultParams(),
		WithObserver(func(observation Observation) { observations = append(observations, observation) }),
		WithEstimator(func([]types.Message, []types.Tool) Cost { return Cost{Requests: 1} }),
	)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	completer := &scriptedCompleter{}
	completer.completeFunc = func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
		return types.Message{}, nil
	}
	wrapped, err := assembly.WrapFor("agent-1", completer)
	if err != nil {
		t.Fatalf("WrapFor: %v", err)
	}
	if _, err := wrapped.Complete(context.Background(), nil, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(observations) != 1 || observations[0].Kind != ObservationAdmitted {
		t.Fatalf("观察事件 = %+v, want 一次 admitted", observations)
	}
	if observations[0].Key != "agent-1" {
		t.Errorf("观测 key = %q, want agent-1", observations[0].Key)
	}
}
