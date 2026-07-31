package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/api"
)

func main() {
	defaults := optionsFromEnvironment()
	configPath := flag.String("config", defaults.ConfigPath, "account YAML; overrides direct endpoint flags")
	baseURL := flag.String("base-url", defaults.BaseURL, "OpenAI/Anthropic-compatible API base URL")
	apiKey := flag.String("api-key", defaults.APIKey, "API key; prefer SEELE_SMOKE_API_KEY")
	model := flag.String("model", defaults.Model, "model name")
	provider := flag.String("provider", string(defaults.Provider), "provider protocol: openai or anthropic")
	timeout := flag.Duration("timeout", defaults.Timeout, "overall smoke timeout")
	flag.Parse()

	options := clientOptions{
		ConfigPath: *configPath, BaseURL: *baseURL, APIKey: *apiKey,
		Model: *model, Provider: api.ProviderType(*provider), Timeout: *timeout,
	}
	client, modelName, err := newChatClient(options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	results, err := runSmokeSuite(ctx, client)
	if err != nil {
		fmt.Fprintln(os.Stderr, "REAL API SMOKE FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("REAL API SMOKE PASSED model=%s cases=%d duration_limit=%s\n", modelName, len(results), timeout.Round(time.Second))
	for _, result := range results {
		fmt.Printf("- %s: tool=%s args=%s result=%s reply=%s\n",
			result.Name, result.Tool, result.Arguments, result.ToolResult, compact(result.Reply, 120))
	}
}

func compact(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
