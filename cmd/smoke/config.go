package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/types"
)

type clientOptions struct {
	ConfigPath string
	BaseURL    string
	APIKey     string
	Model      string
	Provider   api.ProviderType
	Timeout    time.Duration
}

func optionsFromEnvironment() clientOptions {
	return clientOptions{
		ConfigPath: os.Getenv("SMOKE_CONFIG"),
		BaseURL:    os.Getenv("SEELE_SMOKE_BASE_URL"),
		APIKey:     os.Getenv("SEELE_SMOKE_API_KEY"),
		Model:      os.Getenv("SEELE_SMOKE_MODEL"),
		Provider:   api.ProviderType(os.Getenv("SEELE_SMOKE_PROVIDER")),
		Timeout:    120 * time.Second,
	}
}

func newChatClient(options clientOptions) (*api.ChatClient, string, error) {
	if options.Timeout <= 0 {
		options.Timeout = 120 * time.Second
	}
	if strings.TrimSpace(options.ConfigPath) != "" {
		return newConfiguredClient(options)
	}
	if options.BaseURL == "" || options.APIKey == "" || options.Model == "" {
		return nil, "", fmt.Errorf("smoke: provide -config or SEELE_SMOKE_BASE_URL, SEELE_SMOKE_API_KEY, and SEELE_SMOKE_MODEL")
	}
	provider := options.Provider
	if provider == "" {
		provider = api.ProviderOpenAI
	}
	client := api.NewChatClient(types.LLMConfig{
		BaseURL: options.BaseURL,
		APIKey:  options.APIKey,
		Model:   options.Model,
		Timeout: int(options.Timeout.Seconds()),
	}).SetProvider(provider)
	return client, options.Model, nil
}

func newConfiguredClient(options clientOptions) (*api.ChatClient, string, error) {
	path := resolveConfigPath(options.ConfigPath)
	loaded, err := api.LoadFullAccountsConfig(path)
	if err != nil {
		return nil, "", fmt.Errorf("smoke: load config %q: %w", path, err)
	}
	accounts := loaded.Pool.All()
	if len(accounts) == 0 {
		return nil, "", fmt.Errorf("smoke: config %q contains no accounts", path)
	}
	first := accounts[0]
	client := api.NewChatClient(types.LLMConfig{
		BaseURL:     first.BaseURL,
		APIKey:      first.APIKey,
		Model:       first.Model,
		MaxTokens:   loaded.LLMDefaults.MaxTokens,
		Timeout:     effectiveTimeout(first.Timeout, options.Timeout),
		Temperature: loaded.LLMDefaults.Temperature,
	}).WithAccountPool(loaded.Pool)
	provider := loaded.LLMDefaults.Provider
	if options.Provider != "" {
		provider = options.Provider
	}
	if provider != "" {
		client.SetProvider(provider).SetProviderFilter(provider)
	}
	return client, first.Model, nil
}

func effectiveTimeout(accountTimeout int, fallback time.Duration) int {
	if accountTimeout > 0 {
		return accountTimeout
	}
	return int(fallback.Seconds())
}

func resolveConfigPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	fromPackage := filepath.Join("..", "..", path)
	if _, err := os.Stat(fromPackage); err == nil {
		return fromPackage
	}
	return path
}
