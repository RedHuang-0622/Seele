package api

import (
	"os"

	"gopkg.in/yaml.v3"
)

// ── 账号级配置条目 ─────────────────────────────────────────────

// AccountEntry 是 YAML 中单个账号的条目。
type AccountEntry struct {
	Name        string  `yaml:"name"`
	Provider    string  `yaml:"provider"`
	BaseURL     string  `yaml:"base_url"`
	APIKey      string  `yaml:"api_key"`
	Model       string  `yaml:"model"`
	Priority    int     `yaml:"priority"`
	MaxRPM      int     `yaml:"max_rpm"`
	Disabled    bool    `yaml:"disabled"`
	MaxTokens   int     `yaml:"max_tokens,omitempty"`   // 覆盖全局
	Timeout     int     `yaml:"timeout,omitempty"`       // 覆盖全局
	Temperature float64 `yaml:"temperature,omitempty"`    // 覆盖全局
}

// ── 旧格式（仅 accounts 列表）───────────────────────────────────

// AccountsConfig 对应旧 YAML 格式（仅 accounts 列表）。
//
//	accounts:
//	  - name: main
//	    provider: openai
//	    ...
type AccountsConfig struct {
	Accounts []AccountEntry `yaml:"accounts"`
}

// LoadAccountsConfig 从 YAML 文件加载账号配置并构造 AccountPool（旧格式）。
func LoadAccountsConfig(path string) (*AccountPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadAccountsConfigBytes(data)
}

// LoadAccountsConfigBytes 从 YAML 字节数据加载账号配置并构造 AccountPool。
func LoadAccountsConfigBytes(data []byte) (*AccountPool, error) {
	var cfg AccountsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	accounts := make([]*Account, 0, len(cfg.Accounts))
	for _, entry := range cfg.Accounts {
		accounts = append(accounts, entryToAccount(entry))
	}
	return NewAccountPool(accounts...), nil
}

// ── 新格式（llm_config + accounts）─────────────────────────────

// LLMConfigEntry 对应 YAML 中 llm_config 段的共享默认值。
// Provider 决定整个 session 的消息格式（传输层策略 + 工具编码策略）。
// 设好后不应在 session 内切换，否则历史消息格式不一致。
type LLMConfigEntry struct {
	Provider    ProviderType `yaml:"provider"`    // 必填: "openai" | "anthropic"，锁死格式
	MaxTokens   int          `yaml:"max_tokens"`
	Timeout     int          `yaml:"timeout"`
	Temperature float64      `yaml:"temperature"`
}

// FullAccountsConfig 对应新版 YAML 格式：
//
//	llm_config:
//	  max_tokens: 4096
//	  timeout: 60
//	  temperature: 0.7
//
//	accounts:
//	  - name: openai-main
//	    provider: openai
//	    ...
type FullAccountsConfig struct {
	LLMConfig *LLMConfigEntry `yaml:"llm_config,omitempty"`
	Accounts  []AccountEntry  `yaml:"accounts"`
}

// LoadResult 是 LoadFullAccountsConfig 的返回值。
// 调用方通过 LLM 配置 + 账号池控制全部运行时行为。
type LoadResult struct {
	LLMDefaults LLMConfigEntry // 全局共享默认值
	Pool        *AccountPool   // 账号池
}

// LoadFullAccountsConfig 从 YAML 文件加载新格式配置。
// 返回 llm_config 共享默认值和按账号构造的 AccountPool。
func LoadFullAccountsConfig(path string) (*LoadResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadFullAccountsConfigBytes(data)
}

// LoadFullAccountsConfigBytes 从 YAML 字节数据加载新格式配置。
func LoadFullAccountsConfigBytes(data []byte) (*LoadResult, error) {
	var cfg FullAccountsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// 提取 llm_config 默认值（零值不覆盖）
	llmDef := LLMConfigEntry{}
	if cfg.LLMConfig != nil {
		llmDef = *cfg.LLMConfig
	}

	// 构造账号池
	accounts := make([]*Account, 0, len(cfg.Accounts))
	for _, entry := range cfg.Accounts {
		acct := entryToAccount(entry)
			if acct.Provider == "" { acct.Provider = llmDef.Provider }
		// 未在账号级设置时继承全局默认值
		if acct.MaxTokens <= 0 {
			acct.MaxTokens = llmDef.MaxTokens
		}
		if acct.Timeout <= 0 {
			acct.Timeout = llmDef.Timeout
		}
		if acct.Temperature <= 0 {
			acct.Temperature = llmDef.Temperature
		}
		accounts = append(accounts, acct)
	}

	return &LoadResult{
		LLMDefaults: llmDef,
		Pool:        NewAccountPool(accounts...),
	}, nil
}

// ── 通用辅助 ──────────────────────────────────────────────────────

// entryToAccount 将 YAML 条目转换为 Account 结构体。
func entryToAccount(e AccountEntry) *Account {
	return &Account{
		Name:        e.Name,
		Provider:    ProviderType(e.Provider),
		BaseURL:     e.BaseURL,
		APIKey:      e.APIKey,
		Model:       e.Model,
		Priority:    e.Priority,
		MaxRPM:      e.MaxRPM,
		Disabled:    e.Disabled,
		MaxTokens:   e.MaxTokens,
		Timeout:     e.Timeout,
		Temperature: e.Temperature,
	}
}
