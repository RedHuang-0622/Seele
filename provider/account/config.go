package account

import (
	"os"

	"gopkg.in/yaml.v3"
)

// AccountsConfig 对应 YAML 配置：
//
//	accounts:
//	  - name: main
//	    provider: openai
//	    base_url: https://api.openai.com/v1
//	    api_key: sk-xxx
//	    model: gpt-4
//	    priority: 1
type AccountsConfig struct {
	Accounts []AccountEntry `yaml:"accounts"`
}

// AccountEntry 是 YAML 中单个账号的条目。
type AccountEntry struct {
	Name     string `yaml:"name"`
	Provider string `yaml:"provider"`
	BaseURL  string `yaml:"base_url"`
	APIKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
	Priority int    `yaml:"priority"`
	MaxRPM   int    `yaml:"max_rpm"`
	Disabled bool   `yaml:"disabled"`
}

// LoadAccountsConfig 从 YAML 文件加载账号配置并构造 AccountPool。
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
		accounts = append(accounts, &Account{
			Name:     entry.Name,
			Provider: ProviderType(entry.Provider),
			BaseURL:  entry.BaseURL,
			APIKey:   entry.APIKey,
			Model:    entry.Model,
			Priority: entry.Priority,
			MaxRPM:   entry.MaxRPM,
			Disabled: entry.Disabled,
		})
	}
	return NewAccountPool(accounts...), nil
}
