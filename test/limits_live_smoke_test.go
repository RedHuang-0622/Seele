//go:build livesmoke

// limits 的真实 API 冒烟与跨层联调（opt-in）。
//
// 覆盖：
//  1. 真实 provider 两轮调用：装配后的准入 + 真实 usage 结算（Estimated vs Actual）；
//  2. 速率限流在真实调用上可见（第二次请求确实被桶等待）；
//  3. 并发上限 1 + 两个并发真实请求：PeakInFlight 不超过上限且都成功；
//  4. 边界（零 API 成本）：槽位占满 + QueueTimeout=0 → 立即拒绝；已取消 ctx → 立即拒绝；
//  5. 边界（零 API 成本）：真实 HTTP 429 + Retry-After 走通 api 客户端与重试分类；
//  6. 边界（零 API 成本）：连接失败被判定为可重试且不泄漏准入槽位。
//
// 凭据从 seelex 的 accounts.yaml 读取，**不打印任何 key**：
//
//	$env:SEELEX_SMOKE_ACCOUNTS='G:\Program\go\seelex\config\accounts.yaml'
//	go -C G:\Program\go\Seele test -tags livesmoke ./test/... \
//	  -run TestLiveLimitsSmoke -count=1 -v
package test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	seelimits "github.com/RedHuang-0622/Seele/limits"
	"github.com/RedHuang-0622/Seele/types"
)

// smokeAccounts 只解析冒烟需要的字段；结构与 seelex 的 accounts.yaml 对齐。
type smokeAccounts struct {
	Roles map[string][]struct {
		Model   string `yaml:"model"`
		BaseURL string `yaml:"base_url"`
		APIKey  string `yaml:"api_key"`
	} `yaml:"roles"`
}

// loadSmokeAccount 读取真实账号配置。缺少环境变量时跳过，避免 CI 依赖外部服务。
func loadSmokeAccount(t *testing.T) (types.LLMConfig, string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("SEELEX_SMOKE_ACCOUNTS"))
	if path == "" {
		t.Skip("设置 SEELEX_SMOKE_ACCOUNTS 指向 accounts.yaml 才能运行真实 API 冒烟")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取账号配置失败: %v", err)
	}
	var parsed smokeAccounts
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("解析账号配置失败: %v", err)
	}
	var entry struct {
		Model   string `yaml:"model"`
		BaseURL string `yaml:"base_url"`
		APIKey  string `yaml:"api_key"`
	}
	found := false
	for _, role := range []string{"agent", "subagent", "goalplan"} {
		entries := parsed.Roles[role]
		if len(entries) == 0 {
			continue
		}
		if entries[0].APIKey != "" && entries[0].BaseURL != "" {
			entry.Model, entry.BaseURL, entry.APIKey = entries[0].Model, entries[0].BaseURL, entries[0].APIKey
			found = true
			break
		}
	}
	if !found {
		t.Fatal("accounts.yaml 中找不到带 api_key/base_url 的账号条目")
	}
	config := types.LLMConfig{
		BaseURL:     entry.BaseURL,
		APIKey:      entry.APIKey,
		Model:       entry.Model,
		MaxTokens:   256,
		Timeout:     60,
		Temperature: 0,
	}
	return config, entry.Model
}

func liveSmokeParams() seelimits.Params {
	params := seelimits.DefaultParams()
	params.MaxConcurrency = 1
	params.RequestsPerMin = 60
	params.TokensPerMin = 60000
	params.QueueTimeout = seelimits.Duration(20 * time.Second)
	params.Retry = seelimits.RetryPolicy{MaxAttempts: 1, BaseDelay: seelimits.Duration(time.Second)}
	params.PerKey = true
	return params
}

func livePrompt() []types.Message {
	content := "只回复 pong 两个字母，不要调用任何工具。"
	return []types.Message{{Role: "user", Content: &content}}
}

func TestLiveLimitsSmoke(t *testing.T) {
	config, model := loadSmokeAccount(t)
	t.Logf("真实 provider 冒烟：model=%s base_url=%s（凭据不打印）", model, config.BaseURL)

	t.Run("真实调用与用量结算", func(t *testing.T) {
		assembly, err := seelimits.Assemble(liveSmokeParams())
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		client := api.NewChatClient(config)
		wrapped, err := assembly.WrapFor("agent-1", client)
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		for round := 1; round <= 2; round++ {
			message, err := wrapped.Complete(ctx, livePrompt(), nil)
			if err != nil {
				t.Fatalf("第 %d 轮真实调用失败: %v", round, err)
			}
			if message.Content == nil || strings.TrimSpace(*message.Content) == "" {
				t.Fatalf("第 %d 轮返回空内容", round)
			}
			t.Logf("第 %d 轮回复: %q（usage=%+v）", round, strings.TrimSpace(*message.Content), message.Usage)
		}

		stats := assembly.Snapshot().Stats["agent-1"]
		t.Logf("装配统计: admitted=%d completed=%d estimated=%d actual=%d in_flight=%d peak=%d",
			stats.Admitted, stats.Completed, stats.Estimated, stats.Actual, stats.InFlight, stats.PeakInFlight)
		if stats.Admitted != 2 || stats.Completed != 2 {
			t.Errorf("Admitted/Completed = %d/%d, want 2/2", stats.Admitted, stats.Completed)
		}
		if stats.InFlight != 0 {
			t.Errorf("InFlight = %d, want 0（许可必须全部释放）", stats.InFlight)
		}
		if stats.Estimated <= 0 {
			t.Errorf("Estimated = %d, 估算器没有产生 token", stats.Estimated)
		}
		if stats.Actual <= 0 {
			t.Errorf("Actual = %d, 真实 usage 未结算", stats.Actual)
		}
	})

	t.Run("速率限流在真实调用上可见", func(t *testing.T) {
		params := liveSmokeParams()
		params.RequestsPerMin = 60 // 1 req/s
		params.Burst = 1
		assembly, err := seelimits.Assemble(params)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		wrapped, err := assembly.WrapFor("agent-1", api.NewChatClient(config))
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		if _, err := wrapped.Complete(ctx, livePrompt(), nil); err != nil {
			t.Fatalf("第一次调用失败: %v", err)
		}
		start := time.Now()
		if _, err := wrapped.Complete(ctx, livePrompt(), nil); err != nil {
			t.Fatalf("第二次调用失败: %v", err)
		}
		elapsed := time.Since(start)
		stats := assembly.Snapshot().Stats["agent-1"]
		t.Logf("第二次调用总耗时 %s，速率等待次数 %d", elapsed, stats.RateWaited)
		if stats.RateWaited == 0 {
			t.Error("rpm=60/burst=1 时第二次请求应当被速率桶等待")
		}
		if elapsed < 500*time.Millisecond {
			t.Errorf("第二次调用应至少等待 token 补充，实际 %s", elapsed)
		}
	})

	t.Run("并发上限在真实调用上生效", func(t *testing.T) {
		assembly, err := seelimits.Assemble(liveSmokeParams())
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		wrapped, err := assembly.WrapFor("agent-1", api.NewChatClient(config))
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		var waitGroup sync.WaitGroup
		replies := make([]string, 2)
		failures := make([]error, 2)
		for i := 0; i < 2; i++ {
			waitGroup.Add(1)
			go func(index int) {
				defer waitGroup.Done()
				message, err := wrapped.Complete(ctx, livePrompt(), nil)
				if err != nil {
					failures[index] = err
					return
				}
				if message.Content != nil {
					replies[index] = strings.TrimSpace(*message.Content)
				}
			}(i)
		}
		waitGroup.Wait()
		for i, err := range failures {
			if err != nil {
				t.Fatalf("并发第 %d 个请求失败: %v", i, err)
			}
		}
		stats := assembly.Snapshot().Stats["agent-1"]
		t.Logf("并发冒烟统计: peak_in_flight=%d queue_wait=%s replies=%q", stats.PeakInFlight, stats.QueueWait, replies)
		if stats.PeakInFlight > 1 {
			t.Errorf("PeakInFlight = %d，超过 MaxConcurrency=1", stats.PeakInFlight)
		}
		if stats.Admitted != 2 || stats.Completed != 2 {
			t.Errorf("Admitted/Completed = %d/%d, want 2/2", stats.Admitted, stats.Completed)
		}
	})

	// 以下两个子场景不消耗任何 API 额度。
	t.Run("边界_槽位占满立即拒绝", func(t *testing.T) {
		params := liveSmokeParams()
		params.QueueTimeout = 0
		assembly, err := seelimits.Assemble(params)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		gate, err := assembly.Gate("agent-1")
		if err != nil {
			t.Fatalf("Gate: %v", err)
		}
		held, err := gate.Acquire(context.Background(), seelimits.Cost{})
		if err != nil {
			t.Fatalf("占位失败: %v", err)
		}
		wrapped, err := assembly.WrapFor("agent-1", api.NewChatClient(config))
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}
		start := time.Now()
		_, err = wrapped.Complete(context.Background(), livePrompt(), nil)
		elapsed := time.Since(start)
		if !errors.Is(err, seelimits.ErrQueueTimeout) {
			t.Fatalf("err = %v, want ErrQueueTimeout", err)
		}
		if elapsed > 200*time.Millisecond {
			t.Errorf("QueueTimeout=0 必须立即失败，实际 %s", elapsed)
		}
		held.Release()

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := wrapped.Complete(canceled, livePrompt(), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("已取消上下文应返回 context.Canceled，实际 %v", err)
		}
	})

	t.Run("边界_真实HTTP429_RetryAfter", func(t *testing.T) {
		var hits int64
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			atomic.AddInt64(&hits, 1)
			writer.Header().Set("Retry-After", "1")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"message":"rate limited"}}`))
		}))
		defer server.Close()

		params := liveSmokeParams()
		params.Retry = seelimits.RetryPolicy{
			MaxAttempts: 2,
			BaseDelay:   seelimits.Duration(50 * time.Millisecond),
			MaxDelay:    seelimits.Duration(3 * time.Second),
		}
		assembly, err := seelimits.Assemble(params)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		badConfig := config
		badConfig.BaseURL = server.URL
		wrapped, err := assembly.WrapFor("agent-1", api.NewChatClient(badConfig))
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}

		start := time.Now()
		_, err = wrapped.Complete(context.Background(), livePrompt(), nil)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("持续 429 必须返回错误")
		}
		var statusError *types.HTTPStatusError
		if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("err = %v, 期望可分类的 429", err)
		}
		if got := atomic.LoadInt64(&hits); got != 2 {
			t.Errorf("服务端命中 %d 次, want 2（一次原始 + 一次重试）", got)
		}
		if elapsed < 900*time.Millisecond {
			t.Errorf("应尊重 Retry-After: 1，实际耗时 %s", elapsed)
		}
		stats := assembly.Snapshot().Stats["agent-1"]
		if stats.Retried != 1 {
			t.Errorf("Retried = %d, want 1", stats.Retried)
		}
		if stats.InFlight != 0 {
			t.Errorf("InFlight = %d, want 0", stats.InFlight)
		}
	})

	t.Run("边界_连接失败可重试且不泄漏槽位", func(t *testing.T) {
		params := liveSmokeParams()
		params.Retry = seelimits.RetryPolicy{MaxAttempts: 1}
		assembly, err := seelimits.Assemble(params)
		if err != nil {
			t.Fatalf("Assemble: %v", err)
		}
		badConfig := config
		badConfig.BaseURL = "http://127.0.0.1:1" // 未监听端口：立刻 connection refused
		wrapped, err := assembly.WrapFor("agent-1", api.NewChatClient(badConfig))
		if err != nil {
			t.Fatalf("WrapFor: %v", err)
		}

		_, err = wrapped.Complete(context.Background(), livePrompt(), nil)
		if err == nil {
			t.Fatal("连接失败必须返回错误")
		}
		if !seelimits.IsRetryable(err, params.Retry) {
			t.Errorf("连接失败应判定为可重试，实际 %v", err)
		}
		stats := assembly.Snapshot().Stats["agent-1"]
		if stats.InFlight != 0 {
			t.Errorf("失败路径必须释放槽位，InFlight = %d", stats.InFlight)
		}
		if stats.Completed != 1 {
			t.Errorf("Completed = %d, want 1（失败也要结束一次在途）", stats.Completed)
		}
	})
}
