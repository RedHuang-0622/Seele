package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/api"
)

func TestOptionsFromEnvironment(t *testing.T) {
	t.Setenv("SMOKE_CONFIG", "accounts.yaml")
	t.Setenv("SEELE_SMOKE_BASE_URL", "https://example.invalid")
	t.Setenv("SEELE_SMOKE_API_KEY", "test-key")
	t.Setenv("SEELE_SMOKE_MODEL", "test-model")
	t.Setenv("SEELE_SMOKE_PROVIDER", "anthropic")
	options := optionsFromEnvironment()
	if options.ConfigPath != "accounts.yaml" || options.Model != "test-model" || options.Provider != api.ProviderAnthropic {
		t.Fatalf("options = %#v", options)
	}
	if options.Timeout != 120*time.Second {
		t.Fatalf("timeout = %s", options.Timeout)
	}
}

func TestNewChatClientFromDirectConfiguration(t *testing.T) {
	client, model, err := newChatClient(clientOptions{
		BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-model",
		Provider: api.ProviderAnthropic, Timeout: 17 * time.Second,
	})
	if err != nil {
		t.Fatalf("newChatClient() error = %v", err)
	}
	if model != "test-model" || client.Provider() != api.ProviderAnthropic || client.Cfg.Timeout != 17 {
		t.Fatalf("client = %#v, model = %q", client, model)
	}
}

func TestNewChatClientRejectsIncompleteDirectConfiguration(t *testing.T) {
	_, _, err := newChatClient(clientOptions{BaseURL: "https://example.invalid"})
	if err == nil || !strings.Contains(err.Error(), "provide -config") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewChatClientFromAccountConfig(t *testing.T) {
	path := writeConfig(t, `
llm_config:
  provider: openai
  max_tokens: 321
  timeout: 17
  temperature: 0.2
accounts:
  - name: primary
    provider: openai
    base_url: https://example.invalid
    api_key: test-key
    model: test-model
    priority: 1
`)
	client, model, err := newChatClient(clientOptions{ConfigPath: path, Timeout: time.Minute})
	if err != nil {
		t.Fatalf("newChatClient() error = %v", err)
	}
	if model != "test-model" || client.Provider() != api.ProviderOpenAI || client.ProviderFilter() != api.ProviderOpenAI {
		t.Fatalf("model=%q provider=%q filter=%q", model, client.Provider(), client.ProviderFilter())
	}
	if client.AccountPool() == nil || client.Cfg.MaxTokens != 321 || client.Cfg.Timeout != 17 {
		t.Fatalf("configured client = %#v", client)
	}
}

func TestConfiguredClientRejectsMissingOrEmptyConfig(t *testing.T) {
	_, _, err := newChatClient(clientOptions{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")})
	if err == nil || !strings.Contains(err.Error(), "load config") {
		t.Fatalf("missing config error = %v", err)
	}
	empty := writeConfig(t, "accounts: []\n")
	_, _, err = newChatClient(clientOptions{ConfigPath: empty})
	if err == nil || !strings.Contains(err.Error(), "contains no accounts") {
		t.Fatalf("empty config error = %v", err)
	}
}

func TestConfigHelpers(t *testing.T) {
	if got := effectiveTimeout(9, time.Minute); got != 9 {
		t.Fatalf("effectiveTimeout(account) = %d", got)
	}
	if got := effectiveTimeout(0, 23*time.Second); got != 23 {
		t.Fatalf("effectiveTimeout(fallback) = %d", got)
	}
	path := writeConfig(t, "accounts: []\n")
	if got := resolveConfigPath(path); got != path {
		t.Fatalf("resolveConfigPath(abs) = %q", got)
	}
	missing := "definitely-missing-smoke-config.yaml"
	if got := resolveConfigPath(missing); got != missing {
		t.Fatalf("resolveConfigPath(missing) = %q", got)
	}
}

func TestCompact(t *testing.T) {
	if got := compact("short", 10); got != "short" {
		t.Fatalf("compact(short) = %q", got)
	}
	if got := compact("a long response", 6); got != "a long..." {
		t.Fatalf("compact(long) = %q", got)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "accounts.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
