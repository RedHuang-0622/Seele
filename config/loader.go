package config

import (
	"fmt"
	"os"

	types "github.com/sukasukasuka123/Seele/types"
	"gopkg.in/yaml.v3"
)

// LoadConfig 从 YAML 文件加载 LLMConfig。
//
// config.yaml 期望格式：
//
//	llm:
//	  ai_url:     "https://..."
//	  ai_name:    "qwen-plus"
//	  ai_api_key: "sk-xxx"
//	  timeout:    60
//	  temperature: 1.0
func LoadConfig(path string) (types.LLMConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.LLMConfig{}, fmt.Errorf("LoadConfig: read %q: %w", path, err)
	}

	var app types.AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return types.LLMConfig{}, fmt.Errorf("LoadConfig: parse %q: %w", path, err)
	}

	if app.LLM.BaseURL == "" {
		return types.LLMConfig{}, fmt.Errorf("LoadConfig: llm.ai_url is required")
	}
	if app.LLM.Model == "" {
		return types.LLMConfig{}, fmt.Errorf("LoadConfig: llm.ai_name is required")
	}

	// 默认值（与 LoadAppConfig 行为一致）
	if app.LLM.MaxTokens <= 0 {
		app.LLM.MaxTokens = 4096
	}
	if app.LLM.Timeout <= 0 {
		app.LLM.Timeout = 60
	}
	if app.LLM.Temperature == 0 {
		app.LLM.Temperature = 1.0
	}

	return app.LLM, nil
}

// LoadAppConfig 加载完整的 AppConfig（含 Hub、Registry 配置）。
// 供 cmd/main.go 等需要读取全部配置的入口使用。
//
// config.yaml 期望格式：
//
//	llm:
//	  ai_url:     "https://..."
//	  ai_name:    "qwen-plus"
//	  ai_api_key: "sk-xxx"
//
//	hub:
//	  addr: ":50051"
//	  startup_delay_ms: 100
//
//	registry:
//	  path: "./config/registry.yaml"
func LoadAppConfig(path string) (types.AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.AppConfig{}, fmt.Errorf("LoadAppConfig: read %q: %w", path, err)
	}

	var app types.AppConfig
	if err := yaml.Unmarshal(data, &app); err != nil {
		return types.AppConfig{}, fmt.Errorf("LoadAppConfig: parse %q: %w", path, err)
	}

	// 默认值
	if app.Hub.Addr == "" {
		app.Hub.Addr = ":50051"
	}
	if app.Hub.StartupDelayMs <= 0 {
		app.Hub.StartupDelayMs = 100
	}
	if app.Registry.Path == "" {
		app.Registry.Path = "./config/registry.yaml"
	}

	return app, nil
}
