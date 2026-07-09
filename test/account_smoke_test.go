// test/account_smoke_test.go
//
// 号池策略冒烟测试：验证 account-openai.yaml / account-anthropic.yaml
// 的加载、provider 继承、配置合并均正确。
//
// 运行：go test -v -run TestAccountSmoke ./test/
package test

import (
	"testing"

	"github.com/RedHuang-0622/Seele/agent/core/api"
)

func TestAccountSmoke_OpenAI(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-openai.yaml")
	if err != nil {
		t.Skipf("account-openai.yaml 不存在（跳过）: %v", err)
	}

	// llm_config
	if result.LLMDefaults.Provider != api.ProviderOpenAI {
		t.Errorf("llm_config.provider = %q, 期望 %q", result.LLMDefaults.Provider, api.ProviderOpenAI)
	}
	if result.LLMDefaults.MaxTokens <= 0 {
		t.Errorf("llm_config.max_tokens = %d, 期望 > 0", result.LLMDefaults.MaxTokens)
	}
	if result.LLMDefaults.Timeout <= 0 {
		t.Errorf("llm_config.timeout = %d, 期望 > 0", result.LLMDefaults.Timeout)
	}
	t.Logf("llm_config: provider=%s max_tokens=%d timeout=%d temp=%.1f",
		result.LLMDefaults.Provider, result.LLMDefaults.MaxTokens, result.LLMDefaults.Timeout, result.LLMDefaults.Temperature)

	// 账号
	accounts := result.Pool.All()
	if len(accounts) == 0 {
		t.Fatal("号池为空")
	}
	t.Logf("账号数: %d", len(accounts))
	for _, a := range accounts {
		t.Logf("  [%s] %s → %s (model=%s)", a.Provider, a.Name, a.BaseURL, a.Model)
		// per-account provider 应继承自 llm_config
		if a.Provider != api.ProviderOpenAI {
			t.Errorf("账号 %q 的 provider = %q, 期望继承 llm_config 的 %q", a.Name, a.Provider, api.ProviderOpenAI)
		}
		if a.BaseURL == "" {
			t.Errorf("账号 %q 的 base_url 为空", a.Name)
		}
	}
}

func TestAccountSmoke_Anthropic(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-anthropic.yaml")
	if err != nil {
		t.Skipf("account-anthropic.yaml 不存在（跳过）: %v", err)
	}

	// llm_config
	if result.LLMDefaults.Provider != api.ProviderAnthropic {
		t.Errorf("llm_config.provider = %q, 期望 %q", result.LLMDefaults.Provider, api.ProviderAnthropic)
	}
	t.Logf("llm_config: provider=%s max_tokens=%d timeout=%d temp=%.1f",
		result.LLMDefaults.Provider, result.LLMDefaults.MaxTokens, result.LLMDefaults.Timeout, result.LLMDefaults.Temperature)

	// 账号
	accounts := result.Pool.All()
	if len(accounts) == 0 {
		t.Fatal("号池为空")
	}
	t.Logf("账号数: %d", len(accounts))
	for _, a := range accounts {
		t.Logf("  [%s] %s → %s (model=%s)", a.Provider, a.Name, a.BaseURL, a.Model)
		if a.Provider != api.ProviderAnthropic {
			t.Errorf("账号 %q 的 provider = %q, 期望继承 llm_config 的 %q", a.Name, a.Provider, api.ProviderAnthropic)
		}
	}
}

func TestAccountSmoke_ProviderInheritance(t *testing.T) {
	// 显式测试 per-account provider 为空时继承 llm_config
	yaml := `
llm_config:
  provider: openai
accounts:
  - name: test-acct
    base_url: https://example.com
    api_key: sk-test
    model: test-model
    priority: 1
`
	result, err := api.LoadFullAccountsConfigBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("LoadFullAccountsConfigBytes 失败: %v", err)
	}

	accounts := result.Pool.All()
	if len(accounts) != 1 {
		t.Fatalf("期望 1 个账号, 得到 %d", len(accounts))
	}
	a := accounts[0]
	if a.Provider != api.ProviderOpenAI {
		t.Errorf("账号 provider = %q, 期望从 llm_config 继承 %q", a.Provider, api.ProviderOpenAI)
	}

	// 再次验证 anthropic 继承
	yaml2 := `
llm_config:
  provider: anthropic
accounts:
  - name: test-acct2
    base_url: https://example.com
    api_key: sk-test2
    model: test-model2
    priority: 1
`
	result2, _ := api.LoadFullAccountsConfigBytes([]byte(yaml2))
	a2 := result2.Pool.All()[0]
	if a2.Provider != api.ProviderAnthropic {
		t.Errorf("anthropic 账号 provider = %q, 期望继承 %q", a2.Provider, api.ProviderAnthropic)
	}
}

func TestAccountSmoke_LLMConfigAccountOverride(t *testing.T) {
	// 账号级覆盖 llm_config
	yaml := `
llm_config:
  provider: openai
  max_tokens: 4096
  timeout: 60
  temperature: 0.7
accounts:
  - name: custom
    base_url: https://example.com
    api_key: sk-test
    model: test-model
    priority: 1
    max_tokens: 8192
    temperature: 0.5
`
	result, err := api.LoadFullAccountsConfigBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}

	a := result.Pool.All()[0]
	if a.MaxTokens != 8192 {
		t.Errorf("账号 max_tokens = %d, 期望 8192（覆盖全局）", a.MaxTokens)
	}
	if a.Temperature != 0.5 {
		t.Errorf("账号 temperature = %.1f, 期望 0.5（覆盖全局）", a.Temperature)
	}
	if a.Timeout != 60 {
		t.Errorf("账号 timeout = %d, 期望 60（继承全局）", a.Timeout)
	}
}

func TestAccountSmoke_RegisteredStrategies(t *testing.T) {
	names := api.ProviderStrategyNames()
	t.Logf("已注册 ProviderStrategy: %v", names)

	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}

	if !found["openai"] {
		t.Error("openai ProviderStrategy 未注册")
	}
	if !found["anthropic"] {
		// anthropic strategy 在 example_Implement 的 init() 中注册，
		// 测试环境中可能不存在（除非 example 包被导入）
		t.Log("注意: anthropic ProviderStrategy 未在测试包注册（仅 example 中注册）")
	}
}
