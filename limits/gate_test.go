package limits

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
	"go.uber.org/goleak"
)

// TestMain 同时检查整个 limits 包不泄漏 goroutine：准入必须只靠 ctx 与
// 时间桶退出，不允许留下后台协程。
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func mustGate(t *testing.T, params Params) *Gate {
	t.Helper()
	gate, err := NewGate("test", params)
	if err != nil {
		t.Fatalf("NewGate(%+v): %v", params, err)
	}
	return gate
}

// permitResult 用于在 goroutine 里观察一次准入结果。
type permitResult struct {
	permit *Permit
	err    error
	after  time.Duration
}

func acquireAsync(ctx context.Context, gate *Gate, cost Cost) <-chan permitResult {
	done := make(chan permitResult, 1)
	go func() {
		start := time.Now()
		permit, err := gate.Acquire(ctx, cost)
		done <- permitResult{permit: permit, err: err, after: time.Since(start)}
	}()
	return done
}

func waitResult(t *testing.T, done <-chan permitResult, timeout time.Duration) permitResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(timeout):
		t.Fatalf("准入在 %s 内没有返回", timeout)
		return permitResult{}
	}
}

func assertBlocked(t *testing.T, done <-chan permitResult, within time.Duration) {
	t.Helper()
	select {
	case result := <-done:
		t.Fatalf("期望被阻塞，实际立即返回 err=%v", result.err)
	case <-time.After(within):
	}
}

func TestGateDisabledAdmitsEverything(t *testing.T) {
	gate := mustGate(t, Params{Enabled: false, MaxConcurrency: 1})

	permits := make([]*Permit, 0, 3)
	for i := 0; i < 3; i++ {
		permit, err := gate.Acquire(context.Background(), Cost{InputTokens: 100000})
		if err != nil {
			t.Fatalf("关闭时限流不应拒绝：%v", err)
		}
		permits = append(permits, permit)
	}
	// 关闭时不计并发槽位，因此第三个仍应成功。
	stats := gate.Stats()
	if stats.Admitted != 0 {
		t.Errorf("关闭时 Admitted = %d, want 0（完全旁路）", stats.Admitted)
	}
	for _, permit := range permits {
		permit.Release()
	}
	if inFlight := gate.Stats().InFlight; inFlight != 0 {
		t.Errorf("释放后 InFlight = %d, want 0", inFlight)
	}
}

func TestGateConcurrencyCapSerializes(t *testing.T) {
	gate := mustGate(t, Params{
		Enabled:        true,
		MaxConcurrency: 1,
		QueueTimeout:   Duration(2 * time.Second),
	})

	first, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	done := acquireAsync(context.Background(), gate, Cost{})
	assertBlocked(t, done, 60*time.Millisecond)

	first.Release()
	result := waitResult(t, done, time.Second)
	if result.err != nil {
		t.Fatalf("释放槽位后应被准入，实际 %v", result.err)
	}
	result.permit.Release()

	stats := gate.Stats()
	if stats.PeakInFlight > 1 {
		t.Errorf("PeakInFlight = %d, 超过上限 1", stats.PeakInFlight)
	}
	if stats.Admitted != 2 || stats.Completed != 2 {
		t.Errorf("Admitted/Completed = %d/%d, want 2/2", stats.Admitted, stats.Completed)
	}
	if stats.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", stats.InFlight)
	}
}

func TestGateConcurrencyZeroIsUnlimited(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 0, QueueTimeout: Duration(20 * time.Millisecond)})

	permits := make([]*Permit, 0, 8)
	for i := 0; i < 8; i++ {
		permit, err := gate.Acquire(context.Background(), Cost{})
		if err != nil {
			t.Fatalf("并发 0 表示不限，第 %d 次却失败：%v", i+1, err)
		}
		permits = append(permits, permit)
	}
	if inFlight := gate.Stats().InFlight; inFlight != 8 {
		t.Errorf("InFlight = %d, want 8", inFlight)
	}
	for _, permit := range permits {
		permit.Release()
	}
}

func TestGateQueueTimeoutImmediate(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: 0})

	held, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("held acquire: %v", err)
	}
	defer held.Release()

	start := time.Now()
	_, err = gate.Acquire(context.Background(), Cost{})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want ErrQueueTimeout", err)
	}
	if !errors.Is(err, ErrConcurrencyExceeded) {
		t.Fatalf("err = %v, 应同时可用 errors.Is 匹配 ErrConcurrencyExceeded", err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("QueueTimeout=0 必须立即失败，实际等待 %s", elapsed)
	}
	if timeouts := gate.Stats().QueueTimeouts; timeouts != 1 {
		t.Errorf("QueueTimeouts = %d, want 1", timeouts)
	}
}

func TestGateQueueTimeoutBounded(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: Duration(80 * time.Millisecond)})

	held, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("held acquire: %v", err)
	}
	defer held.Release()

	result := waitResult(t, acquireAsync(context.Background(), gate, Cost{}), 2*time.Second)
	if !errors.Is(result.err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want ErrQueueTimeout", result.err)
	}
	if result.after < 60*time.Millisecond || result.after > time.Second {
		t.Errorf("排队时长 = %s，期望约 80ms", result.after)
	}
	stats := gate.Stats()
	if stats.QueueWaited == 0 {
		t.Error("QueueWaited 应记录排队次数")
	}
}

func TestGateCallerCancellationPropagates(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: Duration(5 * time.Second)})

	held, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("held acquire: %v", err)
	}
	defer held.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := acquireAsync(ctx, gate, Cost{})
	time.Sleep(30 * time.Millisecond)
	cancel()

	result := waitResult(t, done, time.Second)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", result.err)
	}
	if errors.Is(result.err, ErrQueueTimeout) {
		t.Fatalf("调用方取消不应被报成排队超时：%v", result.err)
	}
	stats := gate.Stats()
	if stats.QueueTimeouts != 0 {
		t.Errorf("QueueTimeouts = %d, want 0", stats.QueueTimeouts)
	}
	if stats.Rejected != 1 {
		t.Errorf("Rejected = %d, want 1", stats.Rejected)
	}
}

func TestGateWeightedImageCost(t *testing.T) {
	params := Params{Enabled: true, MaxConcurrency: 1, ImageWeight: 0.5, QueueTimeout: 0}
	gate := mustGate(t, params)

	// 一张图 = 1 + 0.5 = 1.5 权重，超过上限 1，必须快速拒绝。
	_, err := gate.Acquire(context.Background(), Cost{Images: 1})
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("err = %v, want ErrQueueTimeout", err)
	}

	params.MaxConcurrency = 2
	gate = mustGate(t, params)
	withImage, err := gate.Acquire(context.Background(), Cost{Images: 1})
	if err != nil {
		t.Fatalf("上限 2 时应允许 1.5 权重：%v", err)
	}
	_, err = gate.Acquire(context.Background(), Cost{})
	if !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("1.5 + 1.0 超过上限 2，应拒绝，实际 %v", err)
	}
	withImage.Release()
	plain, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("释放后应可准入：%v", err)
	}
	plain.Release()

	// ImageWeight = 0 表示图片不额外占权重。
	gate = mustGate(t, Params{Enabled: true, MaxConcurrency: 1, ImageWeight: 0, QueueTimeout: 0})
	zeroWeight, err := gate.Acquire(context.Background(), Cost{Images: 4})
	if err != nil {
		t.Fatalf("ImageWeight=0 时图片不应占权重：%v", err)
	}
	zeroWeight.Release()
}

func TestGateRequestRateWaits(t *testing.T) {
	// 600 rpm = 10 req/s，容量 1 → 第二次必须等到下一个 token。
	gate := mustGate(t, Params{
		Enabled:        true,
		RequestsPerMin: 600,
		Burst:          1,
		QueueTimeout:   Duration(2 * time.Second),
	})

	first, err := gate.Acquire(context.Background(), Cost{Requests: 1})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first.Release()

	result := waitResult(t, acquireAsync(context.Background(), gate, Cost{Requests: 1}), 2*time.Second)
	if result.err != nil {
		t.Fatalf("速率等待后应成功：%v", result.err)
	}
	result.permit.Release()
	if result.after < 60*time.Millisecond {
		t.Errorf("第二次应在速率桶上等待约 100ms，实际 %s", result.after)
	}
	if waited := gate.Stats().RateWaited; waited == 0 {
		t.Error("RateWaited 应记录速率等待")
	}
}

func TestGateTokenRateWaits(t *testing.T) {
	// 600 tpm = 10 token/s，容量 10 → 第二次 10 token 需等约 1s。
	gate := mustGate(t, Params{
		Enabled:        true,
		TokensPerMin:   600,
		TokenBurst:     10,
		QueueTimeout:   Duration(3 * time.Second),
		MaxConcurrency: 0,
	})

	first, err := gate.Acquire(context.Background(), Cost{InputTokens: 10})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first.Release()

	result := waitResult(t, acquireAsync(context.Background(), gate, Cost{InputTokens: 10}), 3*time.Second)
	if result.err != nil {
		t.Fatalf("令牌等待后应成功：%v", result.err)
	}
	result.permit.Release()
	if result.after < 800*time.Millisecond {
		t.Errorf("应在令牌桶上等待约 1s，实际 %s", result.after)
	}
}

func TestGateOversizedTokensAbsorbed(t *testing.T) {
	gate := mustGate(t, Params{
		Enabled:        true,
		TokensPerMin:   60,
		TokenBurst:     10,
		QueueTimeout:   0,
		MaxConcurrency: 0,
	})

	// 单次估算远超桶容量：必须立刻放行并记为超桶，而不是永久排队。
	permit, err := gate.Acquire(context.Background(), Cost{InputTokens: 1000})
	if err != nil {
		t.Fatalf("超容量请求应被吸收：%v", err)
	}
	permit.Release()
	if oversized := gate.Stats().Oversized; oversized < 1 {
		t.Errorf("Oversized = %d, want >= 1", oversized)
	}
}

func TestGateRuntimeSetters(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 2, QueueTimeout: 0})

	held, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("held: %v", err)
	}
	second, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	// 运行中缩容到 1：在途不受影响，新的准入被拒。
	if err := gate.SetConcurrency(1); err != nil {
		t.Fatalf("SetConcurrency: %v", err)
	}
	if _, err := gate.Acquire(context.Background(), Cost{}); !errors.Is(err, ErrQueueTimeout) {
		t.Fatalf("缩容后应拒绝新的准入，实际 %v", err)
	}

	// 扩容后立即重新准入（等待者必须被唤醒）。
	if err := gate.SetConcurrency(4); err != nil {
		t.Fatalf("SetConcurrency: %v", err)
	}
	third, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("扩容后应准入：%v", err)
	}
	third.Release()
	held.Release()
	second.Release()

	// 无效参数必须被拒绝且不改变现状。
	if err := gate.SetConcurrency(-1); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("SetConcurrency(-1) err = %v, want ErrInvalidParams", err)
	}
	if got := gate.Params().MaxConcurrency; got != 4 {
		t.Errorf("无效设置不应生效，MaxConcurrency = %d", got)
	}
	if err := gate.SetRate(-1, 0); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("SetRate(-1,0) err = %v", err)
	}
	if err := gate.SetImageWeight(0.5); err != nil {
		t.Fatalf("SetImageWeight: %v", err)
	}
	if err := gate.SetQueueTimeout(1500 * time.Millisecond); err != nil {
		t.Fatalf("SetQueueTimeout: %v", err)
	}
	if err := gate.SetBurst(3, 300); err != nil {
		t.Fatalf("SetBurst: %v", err)
	}
	if err := gate.SetRetry(RetryPolicy{MaxAttempts: 5}); err != nil {
		t.Fatalf("SetRetry: %v", err)
	}
	if err := gate.SetEnabled(false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if _, err := gate.Acquire(context.Background(), Cost{}); err != nil {
		t.Fatalf("关闭后应放行：%v", err)
	}
	params := gate.Params()
	if params.ImageWeight != 0.5 || params.QueueTimeout.Duration() != 1500*time.Millisecond || params.Burst != 3 {
		t.Errorf("参数未生效: %+v", params)
	}
	if params.Retry.MaxAttempts != 5 {
		t.Errorf("Retry.MaxAttempts = %d, want 5", params.Retry.MaxAttempts)
	}
}

func TestGateSettleReconcilesTokens(t *testing.T) {
	gate := mustGate(t, Params{
		Enabled:        true,
		TokensPerMin:   6000,
		TokenBurst:     1000,
		MaxConcurrency: 0,
	})

	permit, err := gate.Acquire(context.Background(), Cost{InputTokens: 100})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	levelAfterCharge := gate.Stats().TokenLevel

	// 实付少于估算：差额退回桶。
	permit.Settle(types.Usage{PromptTokens: 50, CompletionTokens: 10})
	permit.Release()
	if refunded := gate.Stats().TokenLevel; refunded <= levelAfterCharge {
		t.Errorf("实付低于估算时应退还 token：charge 后 %v，结算后 %v", levelAfterCharge, refunded)
	}
	if actual := gate.Stats().Actual; actual != 60 {
		t.Errorf("Actual = %d, want 60", actual)
	}

	// 实付远超估算：按桶可用额度扣，扣不动记债务。
	permit, err = gate.Acquire(context.Background(), Cost{InputTokens: 10})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	permit.Settle(types.Usage{TotalTokens: 100000})
	permit.Release()
	stats := gate.Stats()
	if stats.TokenDebt <= 0 {
		t.Errorf("TokenDebt = %d, want > 0", stats.TokenDebt)
	}
	if stats.TokenLevel < 0 {
		t.Errorf("TokenLevel 不应为负：%v", stats.TokenLevel)
	}
}

func TestGateSettleIsIdempotent(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, TokensPerMin: 6000, TokenBurst: 1000})
	permit, err := gate.Acquire(context.Background(), Cost{InputTokens: 100})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	permit.Settle(types.Usage{TotalTokens: 40})
	permit.Settle(types.Usage{TotalTokens: 4000})
	permit.Release()
	if actual := gate.Stats().Actual; actual != 40 {
		t.Errorf("重复 Settle 只应生效一次，Actual = %d, want 40", actual)
	}
}

func TestGatePermitReleaseIdempotent(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true, MaxConcurrency: 1, QueueTimeout: 0})
	permit, err := gate.Acquire(context.Background(), Cost{})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	permit.Release()
	permit.Release()
	stats := gate.Stats()
	if stats.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", stats.InFlight)
	}
	if stats.Completed != 1 {
		t.Errorf("Completed = %d, want 1（幂等）", stats.Completed)
	}
	if !permit.Released() {
		t.Error("Released() 应为 true")
	}
}

func TestGateRejectsInvalidCost(t *testing.T) {
	gate := mustGate(t, Params{Enabled: true})
	for name, cost := range map[string]Cost{
		"负请求数": {Requests: -1},
		"负输入":  {InputTokens: -1},
		"负输出":  {OutputTokens: -1},
		"负图片数": {Images: -1},
	} {
		if _, err := gate.Acquire(context.Background(), cost); !errors.Is(err, ErrInvalidParams) {
			t.Errorf("%s: err = %v, want ErrInvalidParams", name, err)
		}
	}
}

func TestGateConcurrentStressRespectsCap(t *testing.T) {
	gate := mustGate(t, Params{
		Enabled:        true,
		MaxConcurrency: 4,
		QueueTimeout:   Duration(5 * time.Second),
	})

	const workers = 40
	var waitGroup sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			permit, err := gate.Acquire(context.Background(), Cost{})
			if err != nil {
				errs <- err
				return
			}
			time.Sleep(time.Millisecond)
			permit.Release()
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发准入失败：%v", err)
	}

	stats := gate.Stats()
	if stats.PeakInFlight > 4 {
		t.Errorf("PeakInFlight = %d, 超过上限 4", stats.PeakInFlight)
	}
	if stats.Admitted != workers || stats.Completed != workers {
		t.Errorf("Admitted/Completed = %d/%d, want %d", stats.Admitted, stats.Completed, workers)
	}
	if stats.InFlight != 0 {
		t.Errorf("InFlight = %d, want 0", stats.InFlight)
	}
}
