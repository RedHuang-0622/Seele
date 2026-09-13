package limits

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestNormalizeDerivesBurst(t *testing.T) {
	tests := []struct {
		name           string
		in             Params
		wantBurst      int
		wantTokenBurst int
	}{
		{name: "全零保持不限", in: Params{}, wantBurst: 0, wantTokenBurst: 0},
		{name: "请求速率派生十秒容量", in: Params{RequestsPerMin: 60}, wantBurst: 10, wantTokenBurst: 0},
		{name: "令牌速率低于下限时抬到下限", in: Params{TokensPerMin: 600}, wantBurst: 0, wantTokenBurst: minTokenBurst},
		{name: "令牌速率足够时按比例派生", in: Params{TokensPerMin: 120000}, wantBurst: 0, wantTokenBurst: 20000},
		{name: "已显式设置的容量不被覆盖", in: Params{RequestsPerMin: 60, Burst: 3, TokenBurst: 7}, wantBurst: 3, wantTokenBurst: 7},
		{name: "极低请求速率至少一个", in: Params{RequestsPerMin: 0.5}, wantBurst: 1, wantTokenBurst: 0},
		{name: "负容量不被静默修正", in: Params{RequestsPerMin: 60, Burst: -5}, wantBurst: -5, wantTokenBurst: 0},
		{name: "负队列预算不被静默修正", in: Params{QueueTimeout: Duration(-time.Second)}, wantBurst: 0, wantTokenBurst: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.in.Normalize()
			if got.Burst != test.wantBurst {
				t.Errorf("Burst = %d, want %d", got.Burst, test.wantBurst)
			}
			if got.TokenBurst != test.wantTokenBurst {
				t.Errorf("TokenBurst = %d, want %d", got.TokenBurst, test.wantTokenBurst)
			}
		})
	}
}

func TestValidateRejectsInvalidParams(t *testing.T) {
	tests := []struct {
		name    string
		in      Params
		wantErr bool
	}{
		{name: "默认参数合法", in: DefaultParams(), wantErr: false},
		{name: "空参数合法（全不限）", in: Params{}, wantErr: false},
		{name: "负并发非法", in: Params{MaxConcurrency: -1}, wantErr: true},
		{name: "负图片权重非法", in: Params{ImageWeight: -0.1}, wantErr: true},
		{name: "负请求速率非法", in: Params{RequestsPerMin: -1}, wantErr: true},
		{name: "负令牌速率非法", in: Params{TokensPerMin: -1}, wantErr: true},
		{name: "负重试次数非法", in: Params{Retry: RetryPolicy{MaxAttempts: -1}}, wantErr: true},
		{name: "负退避基数非法", in: Params{Retry: RetryPolicy{BaseDelay: Duration(-time.Second)}}, wantErr: true},
		{name: "抖动越界非法", in: Params{Retry: RetryPolicy{Jitter: 1.5}}, wantErr: true},
		{name: "退避基数大于上限非法", in: Params{Retry: RetryPolicy{BaseDelay: Duration(10 * time.Second), MaxDelay: Duration(time.Second)}}, wantErr: true},
		{name: "负请求桶容量非法", in: Params{RequestsPerMin: 60, Burst: -1}, wantErr: true},
		{name: "负令牌桶容量非法", in: Params{TokensPerMin: 600, TokenBurst: -1}, wantErr: true},
		{name: "负队列预算非法", in: Params{QueueTimeout: Duration(-time.Second)}, wantErr: true},
		{name: "边界零值合法", in: Params{MaxConcurrency: 0, RequestsPerMin: 0, TokensPerMin: 0}, wantErr: false},
		{name: "单次不重试合法", in: Params{Retry: RetryPolicy{MaxAttempts: 1}}, wantErr: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.in.Normalize().Validate()
			if test.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParamsRatesPerSecond(t *testing.T) {
	params := Params{RequestsPerMin: 120, TokensPerMin: 6000}
	if got := params.RequestsRate(); got != 2 {
		t.Errorf("RequestsRate = %v, want 2", got)
	}
	if got := params.TokensRate(); got != 100 {
		t.Errorf("TokensRate = %v, want 100", got)
	}
}

func TestDurationYAMLRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "时长字符串", raw: "30s", want: 30 * time.Second},
		{name: "复合时长", raw: "1m30s", want: 90 * time.Second},
		{name: "裸数字按秒", raw: "45", want: 45 * time.Second},
		{name: "零", raw: "0", want: 0},
		{name: "非法字符串", raw: "soon", wantErr: true},
		{name: "负数非法", raw: "-5", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got Duration
			err := yaml.Unmarshal([]byte(test.raw), &got)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %v", test.raw, got.Duration())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Duration() != test.want {
				t.Fatalf("got %v, want %v", got.Duration(), test.want)
			}
		})
	}
}

func TestParamsYAMLLoad(t *testing.T) {
	raw := `
enabled: true
max_concurrency: 3
image_weight: 0.25
requests_per_min: 30
tokens_per_min: 60000
queue_timeout: 5s
per_key: true
retry:
  max_attempts: 2
  base_delay: 250ms
  max_delay: 5s
  jitter: 0.1
`
	var params Params
	if err := yaml.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	params = params.Normalize()
	if err := params.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if params.MaxConcurrency != 3 || params.ImageWeight != 0.25 {
		t.Errorf("unexpected concurrency/image weight: %+v", params)
	}
	if params.QueueTimeout.Duration() != 5*time.Second {
		t.Errorf("QueueTimeout = %s", params.QueueTimeout.Duration())
	}
	if params.Retry.MaxAttempts != 2 || params.Retry.BaseDelay.Duration() != 250*time.Millisecond {
		t.Errorf("unexpected retry policy: %+v", params.Retry)
	}
	if params.Burst != 5 || params.TokenBurst != 10000 {
		t.Errorf("derived bursts = %d/%d, want 5/10000", params.Burst, params.TokenBurst)
	}
}

func TestMutators(t *testing.T) {
	base := DefaultParams()

	if got := base.WithEnabled(false); got.Enabled {
		t.Error("WithEnabled(false) did not disable")
	}
	if got := base.WithConcurrency(9); got.MaxConcurrency != 9 {
		t.Errorf("WithConcurrency = %d", got.MaxConcurrency)
	}
	if got := base.WithImageWeight(0); got.ImageWeight != 0 {
		t.Errorf("WithImageWeight = %v", got.ImageWeight)
	}
	if got := base.WithQueueTimeout(0); got.QueueTimeout.Duration() != 0 {
		t.Errorf("WithQueueTimeout = %s", got.QueueTimeout.Duration())
	}
	if got := base.WithPerKey(false); got.PerKey {
		t.Error("WithPerKey(false) did not clear")
	}

	reRated := base.WithRate(30, 6000)
	if reRated.Burst != 0 || reRated.TokenBurst != 0 {
		t.Errorf("WithRate should reset bursts, got %d/%d", reRated.Burst, reRated.TokenBurst)
	}
	derived := reRated.Normalize()
	if derived.Burst != 5 {
		t.Errorf("derived Burst = %d, want 5", derived.Burst)
	}
	if derived.TokenBurst != minTokenBurst {
		t.Errorf("derived TokenBurst = %d, want %d", derived.TokenBurst, minTokenBurst)
	}

	pinned := reRated.WithBurst(7, 9).Normalize()
	if pinned.Burst != 7 || pinned.TokenBurst != 9 {
		t.Errorf("WithBurst not honored: %d/%d", pinned.Burst, pinned.TokenBurst)
	}

	// 修改器必须返回值语义，不能改原参数。
	if base.MaxConcurrency != DefaultParams().MaxConcurrency {
		t.Errorf("mutator mutated the receiver: %+v", base)
	}
}
