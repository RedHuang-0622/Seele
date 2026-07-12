package api

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

const validAccountsYAML = `
accounts:
  - name: main
    provider: openai
    base_url: https://api.openai.com
    api_key: sk-test123
    model: gpt-4
    priority: 1
    max_rpm: 60
`

const validFullYAML = `
llm_config:
  max_tokens: 4096
  timeout: 120
  temperature: 0.7
  provider: openai

accounts:
  - name: openai-main
    provider: openai
    base_url: https://api.openai.com
    api_key: sk-test
    model: gpt-4
    priority: 1
    max_rpm: 60
  - name: anthropic-main
    provider: anthropic
    base_url: https://api.anthropic.com
    api_key: sk-ant-test
    model: claude-3-opus
    priority: 2
    max_rpm: 30
`

const validFullYAMLInherit = `
llm_config:
  max_tokens: 2048
  timeout: 60
  temperature: 0.5

accounts:
  - name: inheriting
    provider: openai
    base_url: https://api.openai.com
    api_key: sk-test
`

func TestLoadAccountsConfigWithValidYAML(t *testing.T) {
	f, err := os.CreateTemp("", "accounts_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(validAccountsYAML); err != nil {
		t.Fatal(err)
	}
	f.Close()

	pool, err := LoadAccountsConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("pool should not be nil")
	}
	all := pool.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 account, got %d", len(all))
	}
	if all[0].Name != "main" {
		t.Errorf("expected name 'main', got %q", all[0].Name)
	}
	if all[0].Provider != ProviderOpenAI {
		t.Errorf("expected ProviderOpenAI, got %q", all[0].Provider)
	}
	if all[0].BaseURL != "https://api.openai.com" {
		t.Errorf("unexpected BaseURL: %q", all[0].BaseURL)
	}
	if all[0].APIKey != "sk-test123" {
		t.Errorf("unexpected APIKey: %q", all[0].APIKey)
	}
	if all[0].Model != "gpt-4" {
		t.Errorf("unexpected Model: %q", all[0].Model)
	}
	if all[0].Priority != 1 {
		t.Errorf("expected Priority 1, got %d", all[0].Priority)
	}
	if all[0].MaxRPM != 60 {
		t.Errorf("expected MaxRPM 60, got %d", all[0].MaxRPM)
	}
}

func TestLoadAccountsConfigWithMissingFile(t *testing.T) {
	pool, err := LoadAccountsConfig("nonexistent_file.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if pool != nil {
		t.Errorf("expected nil pool on error, got %v", pool)
	}
}

func TestLoadAccountsConfigBytesWithInvalidYAML(t *testing.T) {
	pool, err := LoadAccountsConfigBytes([]byte("invalid yaml: [unclosed"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if pool != nil {
		t.Errorf("expected nil pool on error, got %v", pool)
	}
}

func TestLoadAccountsConfigBytesEmptyList(t *testing.T) {
	pool, err := LoadAccountsConfigBytes([]byte("accounts:"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("pool should not be nil")
	}
	all := pool.All()
	if len(all) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(all))
	}
}

func TestLoadFullAccountsConfigWithValidYAML(t *testing.T) {
	f, err := os.CreateTemp("", "full_accounts_*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(validFullYAML); err != nil {
		t.Fatal(err)
	}
	f.Close()

	result, err := LoadFullAccountsConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// Check llm defaults
	if result.LLMDefaults.Provider != ProviderOpenAI {
		t.Errorf("expected provider 'openai', got %q", result.LLMDefaults.Provider)
	}
	if result.LLMDefaults.MaxTokens != 4096 {
		t.Errorf("expected MaxTokens 4096, got %d", result.LLMDefaults.MaxTokens)
	}
	if result.LLMDefaults.Timeout != 120 {
		t.Errorf("expected Timeout 120, got %d", result.LLMDefaults.Timeout)
	}
	if result.LLMDefaults.Temperature != 0.7 {
		t.Errorf("expected Temperature 0.7, got %f", result.LLMDefaults.Temperature)
	}
	// Check pool
	all := result.Pool.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(all))
	}
	if all[0].Name != "openai-main" {
		t.Errorf("expected first account 'openai-main', got %q", all[0].Name)
	}
	if all[1].Name != "anthropic-main" {
		t.Errorf("expected second account 'anthropic-main', got %q", all[1].Name)
	}
}

func TestLoadFullAccountsConfigMissingFile(t *testing.T) {
	result, err := LoadFullAccountsConfig("nonexistent_full.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestLoadFullAccountsConfigBytesWithAccountInheritingLLMDefaults(t *testing.T) {
	result, err := LoadFullAccountsConfigBytes([]byte(validFullYAMLInherit))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result should not be nil")
	}
	// llm defaults
	if result.LLMDefaults.MaxTokens != 2048 {
		t.Errorf("expected MaxTokens 2048, got %d", result.LLMDefaults.MaxTokens)
	}
	if result.LLMDefaults.Timeout != 60 {
		t.Errorf("expected Timeout 60, got %d", result.LLMDefaults.Timeout)
	}
	if result.LLMDefaults.Temperature != 0.5 {
		t.Errorf("expected Temperature 0.5, got %f", result.LLMDefaults.Temperature)
	}

	// Account should inherit the defaults
	all := result.Pool.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 account, got %d", len(all))
	}
	acct := all[0]
	if acct.MaxTokens != 2048 {
		t.Errorf("expected inherited MaxTokens 2048, got %d", acct.MaxTokens)
	}
	if acct.Timeout != 60 {
		t.Errorf("expected inherited Timeout 60, got %d", acct.Timeout)
	}
	if acct.Temperature != 0.5 {
		t.Errorf("expected inherited Temperature 0.5, got %f", acct.Temperature)
	}
	if acct.Provider != ProviderOpenAI {
		t.Errorf("expected inherited Provider 'openai', got %q", acct.Provider)
	}
}

func TestLoadFullAccountsConfigBytesWithAccountOverridingLLMDefaults(t *testing.T) {
	yaml := `
llm_config:
  max_tokens: 1024
  timeout: 30
  temperature: 0.3

accounts:
  - name: overrider
    provider: openai
    base_url: https://api.openai.com
    api_key: sk-test
    max_tokens: 2048
    timeout: 120
    temperature: 0.7
`
	result, err := LoadFullAccountsConfigBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	acct := result.Pool.All()[0]
	if acct.MaxTokens != 2048 {
		t.Errorf("expected overridden MaxTokens 2048, got %d", acct.MaxTokens)
	}
	if acct.Timeout != 120 {
		t.Errorf("expected overridden Timeout 120, got %d", acct.Timeout)
	}
	if acct.Temperature != 0.7 {
		t.Errorf("expected overridden Temperature 0.7, got %f", acct.Temperature)
	}
}

func TestLoadFullAccountsConfigBytesWithNoLLMConfig(t *testing.T) {
	yaml := `
accounts:
  - name: standalone
    provider: openai
    base_url: https://api.openai.com
    api_key: sk-test
    model: gpt-4
`
	result, err := LoadFullAccountsConfigBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// LLM defaults should be zero-valued
	if result.LLMDefaults.MaxTokens != 0 {
		t.Errorf("expected zero MaxTokens, got %d", result.LLMDefaults.MaxTokens)
	}
	// Account should have its own values preserved
	acct := result.Pool.All()[0]
	if acct.Provider != ProviderOpenAI {
		t.Errorf("expected ProviderOpenAI, got %q", acct.Provider)
	}
	if acct.Model != "gpt-4" {
		t.Errorf("expected Model 'gpt-4', got %q", acct.Model)
	}
	// Account-level zero values should stay zero (no llm_config to inherit)
	if acct.MaxTokens != 0 {
		t.Errorf("expected MaxTokens 0, got %d", acct.MaxTokens)
	}
}

func TestLoadFullAccountsConfigBytesInvalidYAML(t *testing.T) {
	result, err := LoadFullAccountsConfigBytes([]byte("invalid: [yaml"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if result != nil {
		t.Errorf("expected nil result on error, got %v", result)
	}
}

func TestEntryToAccountConvertsCorrectly(t *testing.T) {
	e := AccountEntry{
		Name:        "test-account",
		Provider:    "anthropic",
		BaseURL:     "https://api.anthropic.com",
		APIKey:      "sk-ant-abc123",
		Model:       "claude-3-opus",
		Priority:    5,
		MaxRPM:      100,
		Disabled:    true,
		MaxTokens:   8192,
		Timeout:     90,
		Temperature: 0.8,
	}
	acct := entryToAccount(e)
	if acct.Name != "test-account" {
		t.Errorf("expected Name 'test-account', got %q", acct.Name)
	}
	if acct.Provider != ProviderAnthropic {
		t.Errorf("expected Provider 'anthropic', got %q", acct.Provider)
	}
	if acct.BaseURL != "https://api.anthropic.com" {
		t.Errorf("unexpected BaseURL: %q", acct.BaseURL)
	}
	if acct.APIKey != "sk-ant-abc123" {
		t.Errorf("unexpected APIKey: %q", acct.APIKey)
	}
	if acct.Model != "claude-3-opus" {
		t.Errorf("unexpected Model: %q", acct.Model)
	}
	if acct.Priority != 5 {
		t.Errorf("expected Priority 5, got %d", acct.Priority)
	}
	if acct.MaxRPM != 100 {
		t.Errorf("expected MaxRPM 100, got %d", acct.MaxRPM)
	}
	if !acct.Disabled {
		t.Error("expected Disabled true")
	}
	if acct.MaxTokens != 8192 {
		t.Errorf("expected MaxTokens 8192, got %d", acct.MaxTokens)
	}
	if acct.Timeout != 90 {
		t.Errorf("expected Timeout 90, got %d", acct.Timeout)
	}
	if acct.Temperature != 0.8 {
		t.Errorf("expected Temperature 0.8, got %f", acct.Temperature)
	}
}

func TestAccountEntryYAMLRoundTrip(t *testing.T) {
	input := `name: roundtrip
provider: openai
base_url: https://test.com
api_key: sk-roundtrip
model: gpt-4
priority: 3
max_rpm: 50
disabled: false
max_tokens: 4096
timeout: 60
temperature: 0.5
`
	var entry AccountEntry
	if err := yaml.Unmarshal([]byte(input), &entry); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if entry.Name != "roundtrip" {
		t.Errorf("expected 'roundtrip', got %q", entry.Name)
	}
	if entry.Provider != "openai" {
		t.Errorf("expected 'openai', got %q", entry.Provider)
	}
	if entry.BaseURL != "https://test.com" {
		t.Errorf("expected 'https://test.com', got %q", entry.BaseURL)
	}
	if entry.APIKey != "sk-roundtrip" {
		t.Errorf("unexpected APIKey: %q", entry.APIKey)
	}
	if entry.Model != "gpt-4" {
		t.Errorf("unexpected Model: %q", entry.Model)
	}
	if entry.Priority != 3 {
		t.Errorf("expected Priority 3, got %d", entry.Priority)
	}
	if entry.MaxRPM != 50 {
		t.Errorf("expected MaxRPM 50, got %d", entry.MaxRPM)
	}
	if entry.MaxTokens != 4096 {
		t.Errorf("expected 4096, got %d", entry.MaxTokens)
	}
	if entry.Timeout != 60 {
		t.Errorf("expected Timeout 60, got %d", entry.Timeout)
	}
	if entry.Temperature != 0.5 {
		t.Errorf("expected Temperature 0.5, got %f", entry.Temperature)
	}
}
