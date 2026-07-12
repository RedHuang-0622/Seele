package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempYAML writes content to a temp file and returns its path.
func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeTempYAML: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// LoadConfig
// ---------------------------------------------------------------------------

func TestLoadConfig_ValidYAML(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
  timeout: 30
  temperature: 0.5
`
	path := writeTempYAML(t, y)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "gpt-4" {
		t.Errorf("Model = %q", cfg.Model)
	}
	if cfg.APIKey != "sk-xxx" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Timeout != 30 {
		t.Errorf("Timeout = %d (want 30)", cfg.Timeout)
	}
	if cfg.Temperature != 0.5 {
		t.Errorf("Temperature = %f (want 0.5)", cfg.Temperature)
	}
}

func TestLoadConfig_DefaultsApplied(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d (want 4096)", cfg.MaxTokens)
	}
	if cfg.Timeout != 60 {
		t.Errorf("Timeout = %d (want 60)", cfg.Timeout)
	}
	if cfg.Temperature != 1.0 {
		t.Errorf("Temperature = %f (want 1.0)", cfg.Temperature)
	}
}

func TestLoadConfig_ExplicitZeroDefaults(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
  max_tokens: 0
  timeout: 0
  temperature: 0
`
	path := writeTempYAML(t, y)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d (want 4096)", cfg.MaxTokens)
	}
	if cfg.Timeout != 60 {
		t.Errorf("Timeout = %d (want 60)", cfg.Timeout)
	}
	if cfg.Temperature != 1.0 {
		t.Errorf("Temperature = %f (want 1.0)", cfg.Temperature)
	}
}

func TestLoadConfig_CustomMaxTokens(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
  max_tokens: 8192
  timeout: 120
  temperature: 0.3
`
	path := writeTempYAML(t, y)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d (want 8192)", cfg.MaxTokens)
	}
	if cfg.Timeout != 120 {
		t.Errorf("Timeout = %d (want 120)", cfg.Timeout)
	}
	if cfg.Temperature != 0.3 {
		t.Errorf("Temperature = %f (want 0.3)", cfg.Temperature)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	path := filepath.Join(os.TempDir(), "seele-test-nonexistent-XXXX.yaml")
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_MissingAiURL(t *testing.T) {
	y := `
llm:
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing ai_url")
	}
}

func TestLoadConfig_MissingAiName(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for missing ai_name")
	}
}

func TestLoadConfig_EmptyAiURL(t *testing.T) {
	y := `
llm:
  ai_url: ""
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty ai_url")
	}
}

func TestLoadConfig_EmptyAiName(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: ""
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for empty ai_name")
	}
}

func TestLoadConfig_ErrorPreservesPath(t *testing.T) {
	path := filepath.Join(os.TempDir(), "seele-test-nonexist-load-XXXX.yaml")
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestLoadConfig_BadYAML(t *testing.T) {
	path := writeTempYAML(t, `llm: [bad yaml:`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for bad YAML")
	}
}

// ---------------------------------------------------------------------------
// LoadAppConfig
// ---------------------------------------------------------------------------

func TestLoadAppConfig_ValidYAML(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
hub:
  addr: ":9090"
  startup_delay_ms: 200
registry:
  path: "./my-registry.yaml"
`
	path := writeTempYAML(t, y)
	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig: %v", err)
	}
	if cfg.LLM.BaseURL != "https://api.example.com" {
		t.Errorf("LLM.BaseURL = %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Model != "gpt-4" {
		t.Errorf("LLM.Model = %q", cfg.LLM.Model)
	}
	if cfg.Hub.Addr != ":9090" {
		t.Errorf("Hub.Addr = %q", cfg.Hub.Addr)
	}
	if cfg.Hub.StartupDelayMs != 200 {
		t.Errorf("Hub.StartupDelayMs = %d", cfg.Hub.StartupDelayMs)
	}
	if cfg.Registry.Path != "./my-registry.yaml" {
		t.Errorf("Registry.Path = %q", cfg.Registry.Path)
	}
}

func TestLoadAppConfig_DefaultsApplied(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig: %v", err)
	}
	if cfg.Hub.Addr != ":50051" {
		t.Errorf("Hub.Addr = %q (want :50051)", cfg.Hub.Addr)
	}
	if cfg.Hub.StartupDelayMs != 100 {
		t.Errorf("Hub.StartupDelayMs = %d (want 100)", cfg.Hub.StartupDelayMs)
	}
	if cfg.Registry.Path != "./config/registry.yaml" {
		t.Errorf("Registry.Path = %q (want ./config/registry.yaml)", cfg.Registry.Path)
	}
}

func TestLoadAppConfig_ExplicitZeroDefaults(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
hub:
  addr: ""
  startup_delay_ms: 0
registry:
  path: ""
`
	path := writeTempYAML(t, y)
	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig: %v", err)
	}
	if cfg.Hub.Addr != ":50051" {
		t.Errorf("Hub.Addr = %q (want :50051)", cfg.Hub.Addr)
	}
	if cfg.Hub.StartupDelayMs != 100 {
		t.Errorf("Hub.StartupDelayMs = %d (want 100)", cfg.Hub.StartupDelayMs)
	}
	if cfg.Registry.Path != "./config/registry.yaml" {
		t.Errorf("Registry.Path = %q (want ./config/registry.yaml)", cfg.Registry.Path)
	}
}

func TestLoadAppConfig_MissingFile(t *testing.T) {
	path := filepath.Join(os.TempDir(), "seele-test-missing-app-XXXX.yaml")
	_, err := LoadAppConfig(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadAppConfig_BadYAML(t *testing.T) {
	path := writeTempYAML(t, `hub: {bad: `)
	_, err := LoadAppConfig(path)
	if err == nil {
		t.Fatal("expected error for bad YAML")
	}
}

func TestLoadAppConfig_ErrorPreservesPath(t *testing.T) {
	path := filepath.Join(os.TempDir(), "seele-test-nonexist-XXXX.yaml")
	_, err := LoadAppConfig(path)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error should contain file path, got: %v", err)
	}
}

func TestLoadAppConfig_LLMWithoutHub(t *testing.T) {
	y := `
llm:
  ai_url: "https://api.example.com"
  ai_name: "gpt-4"
  ai_api_key: "sk-xxx"
`
	path := writeTempYAML(t, y)
	cfg, err := LoadAppConfig(path)
	if err != nil {
		t.Fatalf("LoadAppConfig: %v", err)
	}
	// Verify LLM data survives intact even with defaults
	if cfg.LLM.BaseURL != "https://api.example.com" {
		t.Errorf("LLM.BaseURL = %q", cfg.LLM.BaseURL)
	}
}
