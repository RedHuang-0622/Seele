package limits

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestStatusOf(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantOK     bool
	}{
		{name: "nil", err: nil, wantOK: false},
		{name: "typed 错误", err: statusError(429, 0), wantStatus: 429, wantOK: true},
		{name: "包装后的 typed 错误", err: ioError(statusError(503, 0)), wantStatus: 503, wantOK: true},
		{name: "字符串回退", err: errors.New("HTTP 400: bad request"), wantStatus: 400, wantOK: true},
		{name: "前缀包含", err: errors.New("ChatClient: HTTP 500: boom"), wantStatus: 500, wantOK: true},
		{name: "无状态码", err: errors.New("connection reset by peer"), wantOK: false},
		{name: "HTTP 后无数字", err: errors.New("HTTP :"), wantOK: false},
		{name: "HTTP 后有空格的数字", err: errors.New("HTTP 429: rate limited"), wantStatus: 429, wantOK: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, ok := StatusOf(test.err)
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v (err=%v)", ok, test.wantOK, test.err)
			}
			if ok && status != test.wantStatus {
				t.Fatalf("status = %d, want %d", status, test.wantStatus)
			}
		})
	}
}

func TestRetryAfterOf(t *testing.T) {
	if _, ok := RetryAfterOf(statusError(429, 3*time.Second)); !ok {
		t.Fatal("typed 错误的 RetryAfter 应可读出")
	}
	if delay, ok := RetryAfterOf(statusError(429, 0)); ok || delay != 0 {
		t.Fatalf("没有 Retry-After 时不应报告 ok，got %v/%v", delay, ok)
	}
	if _, ok := RetryAfterOf(errors.New("HTTP 429: rate limited")); ok {
		t.Fatal("字符串错误没有 Retry-After")
	}
}

func TestIsRetryable(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 3}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "429", err: statusError(429, 0), want: true},
		{name: "500", err: statusError(500, 0), want: true},
		{name: "502", err: statusError(502, 0), want: true},
		{name: "503", err: statusError(503, 0), want: true},
		{name: "504", err: statusError(504, 0), want: true},
		{name: "400 不重试", err: statusError(400, 0), want: false},
		{name: "401 不重试", err: statusError(401, 0), want: false},
		{name: "流式超时字符串", err: errors.New("HTTP stream: context deadline exceeded"), want: false},
		{name: "连接重置", err: errors.New("read tcp: connection reset by peer"), want: true},
		{name: "意外 EOF", err: io.ErrUnexpectedEOF, want: true},
		{name: "rate limit 文案", err: errors.New("rate limit reached"), want: true},
		{name: "业务错误", err: errors.New("invalid tool schema"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetryable(test.err, policy); got != test.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestIsRetryableRespectsCallerCancellation(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 3}
	if IsRetryable(context.Canceled, policy) {
		t.Error("调用方取消不应可重试")
	}
	if IsRetryable(context.DeadlineExceeded, policy) {
		t.Error("调用方超时不应可重试")
	}
	if IsRetryable(ioError(context.Canceled), policy) {
		t.Error("包装后的取消仍不应可重试")
	}
}

func TestIsRetryableStatusOverride(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 3, RetryableStatuses: []int{418}}
	if !IsRetryable(statusError(418, 0), policy) {
		t.Error("显式状态集合应生效")
	}
	if IsRetryable(statusError(429, 0), policy) {
		t.Error("显式状态集合应覆盖默认集合")
	}
}

func TestRetryDelayBoundaries(t *testing.T) {
	base := RetryPolicy{MaxAttempts: 3, BaseDelay: Duration(100 * time.Millisecond), MaxDelay: Duration(time.Second)}

	tests := []struct {
		name      string
		attempt   int
		policy    RetryPolicy
		err       error
		random    float64
		want      time.Duration
		wantRetry bool
	}{
		{name: "单次尝试不重试", attempt: 1, policy: RetryPolicy{MaxAttempts: 1}, err: statusError(429, 0), wantRetry: false},
		{name: "达到上限不重试", attempt: 3, policy: base, err: statusError(429, 0), wantRetry: false},
		{name: "第一次失败指数一倍", attempt: 1, policy: base, err: statusError(429, 0), want: 100 * time.Millisecond, wantRetry: true},
		{name: "第二次失败指数两倍", attempt: 2, policy: base, err: statusError(429, 0), want: 200 * time.Millisecond, wantRetry: true},
		{name: "超过上限被夹住", attempt: 4, policy: RetryPolicy{MaxAttempts: 6, BaseDelay: Duration(400 * time.Millisecond), MaxDelay: Duration(time.Second)}, err: statusError(503, 0), want: time.Second, wantRetry: true},
		{name: "不可重试错误返回 false", attempt: 1, policy: base, err: statusError(400, 0), wantRetry: false},
		{name: "未设置退避基数时使用默认", attempt: 1, policy: RetryPolicy{MaxAttempts: 2}, err: statusError(429, 0), want: DefaultBaseDelay, wantRetry: true},
		{name: "尝试次数为 0 视为不重试", attempt: 0, policy: base, err: statusError(429, 0), wantRetry: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			delay, retry := RetryDelay(test.attempt, test.policy, test.err, test.random)
			if retry != test.wantRetry {
				t.Fatalf("retry = %v, want %v (delay=%v)", retry, test.wantRetry, delay)
			}
			if retry && delay != test.want {
				t.Fatalf("delay = %v, want %v", delay, test.want)
			}
		})
	}
}

func TestRetryDelayJitterBounds(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   Duration(time.Second),
		MaxDelay:    Duration(10 * time.Second),
		Jitter:      0.5,
	}

	low, ok := RetryDelay(1, policy, statusError(429, 0), 0)
	if !ok {
		t.Fatal("应当可重试")
	}
	if low != 500*time.Millisecond {
		t.Errorf("下界抖动 = %v, want 500ms", low)
	}

	high, _ := RetryDelay(1, policy, statusError(429, 0), 0.999)
	if high <= 1400*time.Millisecond || high > 1500*time.Millisecond {
		t.Errorf("上界抖动 = %v, 期望约 1.5s", high)
	}

	// 越界随机数必须被夹住而不是产生负退避。
	negative, _ := RetryDelay(1, policy, statusError(429, 0), -10)
	if negative < 0 {
		t.Errorf("负退避不可接受: %v", negative)
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 2,
		BaseDelay:   Duration(10 * time.Millisecond),
		MaxDelay:    Duration(2 * time.Second),
	}
	delay, ok := RetryDelay(1, policy, statusError(429, 1500*time.Millisecond), 0.5)
	if !ok {
		t.Fatal("应当可重试")
	}
	if delay != 1500*time.Millisecond {
		t.Errorf("delay = %v, want 1.5s（尊重 Retry-After）", delay)
	}

	// Retry-After 超过 MaxDelay 时被夹住。
	clamped, _ := RetryDelay(1, policy, statusError(429, time.Hour), 0.5)
	if clamped != 2*time.Second {
		t.Errorf("clamped = %v, want 2s", clamped)
	}

	// 显式忽略 Retry-After 时回到指数退避。
	policy.IgnoreRetryAfter = true
	ignored, _ := RetryDelay(1, policy, statusError(429, 1500*time.Millisecond), 0.5)
	if ignored != 10*time.Millisecond {
		t.Errorf("ignored = %v, want 10ms", ignored)
	}
}

// ioError 用 %w 包装错误，验证 errors.As/errors.Is 在链路里仍然成立。
func ioError(err error) error {
	return &wrappedError{inner: err}
}

type wrappedError struct{ inner error }

func (w *wrappedError) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedError) Unwrap() error { return w.inner }
