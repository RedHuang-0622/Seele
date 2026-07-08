// 04_mcp/main.go
//
// MCP (Model Context Protocol) 集成演示。
//
// 运行前：
//   1. 安装 MCP server（任选其一）：
//      npx -y @modelcontextprotocol/server-filesystem /tmp
//      uvx mcp-server-fetch
//   2. go run . -c ../../config/account-anthropic.yaml

package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	mcpprov "github.com/RedHuang-0622/Seele/agent/core/tool/mcp"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var configPath = flag.String("c", "../../config/account-anthropic.yaml", "config path (account-anthropic.yaml / account-anthropic.yaml)")

func main() {
	flag.Parse()
	ctx := context.Background()

	result, err := api.LoadFullAccountsConfig(*configPath)
	if err != nil {
		log.Fatalf("load config %s: %v", *configPath, err)
	}
	ls := result.LLMDefaults
	pool := result.Pool
	first := pool.All()[0]

	llmCfg := types.LLMConfig{
		BaseURL:     first.BaseURL,
		APIKey:      first.APIKey,
		Model:       first.Model,
		MaxTokens:   ls.MaxTokens,
		Timeout:     ls.Timeout,
		Temperature: ls.Temperature,
	}

	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		HubStartupDelay: 10,
	})
	if err != nil {
		log.Fatalf("agent init failed: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	mcp := agt.MCP()
	if mcp == nil {
		log.Fatal("MCP provider init failed")
	}

	// ── 方式 1：stdio 模式（本地子进程） ────────────────────────────
	err = mcp.Attach(ctx, mcpprov.ServerConfig{
		Name:    "filesystem",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
	})
	if err != nil {
		log.Printf("filesystem MCP attach failed (skip): %v", err)
	}

	// ── 方式 2：sse 模式（远程服务） ───────────────────────────────
	err = mcp.Attach(ctx, mcpprov.ServerConfig{
		Name: "fetch",
		URL:  "http://localhost:8080/sse",
	})
	if err != nil {
		log.Printf("fetch MCP attach failed (skip): %v", err)
	}

	eng := engine.New(agt, engine.WithSystemPrompt("你是一个可以使用外部工具的助手。"))

	fmt.Println("=== 已注册工具 ===")
	for _, t := range agt.Tools().Tools() {
		fmt.Printf("  %s — %s\n", t.Function.Name, t.Function.Description)
	}

	reply, err := eng.Chat(ctx, "你好，可以访问哪些外部工具？")
	if err != nil {
		log.Printf("chat error: %v", err)
	} else {
		fmt.Println("\n🤖:", reply)
	}
}
