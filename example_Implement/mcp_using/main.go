// mcp_using/main.go
//
// 演示如何挂载 MCP Server，让 Agent 同时使用 Hub Skill 和 MCP 工具。
//
// 前置条件（任选其一）：
//   - npx -y @modelcontextprotocol/server-filesystem /tmp
//   - uvx mcp-server-fetch
//   - npx -y @modelcontextprotocol/server-everything

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	provider "github.com/sukasukasuka123/Seele/provider"
	"github.com/sukasukasuka123/Seele/sdk/api"
)

func main() {
	ctx := context.Background()

	engine, err := api.New(api.Options{
		RegistryPath:  "config/registry.yaml",
		LLMConfigPath: "config/config.yaml",
	})
	if err != nil {
		log.Fatalf("engine init: %v", err)
	}
	defer engine.Shutdown()

	// ── 挂载 MCP Server（stdio 模式）────────────────────────────
	// 方式一：filesystem server（读写本地文件）
	err = engine.MCP().Attach(ctx, provider.MCPServerConfig{
		Name:      "filesystem",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
		// Env 格式是 "KEY=VALUE" 字符串切片，与 os.Environ() 一致
		Env: []string{},
	})
	if err != nil {
		log.Printf("attach filesystem server failed: %v (跳过)", err)
	} else {
		fmt.Println("✓ filesystem MCP server 已连接")
	}

	// 方式二：fetch server（HTTP 请求）
	err = engine.MCP().Attach(ctx, provider.MCPServerConfig{
		Name:      "fetch",
		Transport: "stdio",
		Command:   "uvx",
		Args:      []string{"mcp-server-fetch"},
	})
	if err != nil {
		log.Printf("attach fetch server failed: %v (跳过)", err)
	} else {
		fmt.Println("✓ fetch MCP server 已连接")
	}

	// 方式三：SSE 模式（远程 MCP Server）
	err = engine.MCP().Attach(ctx, provider.MCPServerConfig{
		Name:      "remote-tools",
		Transport: "sse",
		URL:       "http://your-mcp-server.example.com/sse",
	})
	if err != nil {
		log.Printf("attach remote server failed: %v (跳过)", err)
	}

	// ── 查看所有可用工具（Hub + MCP 合并）───────────────────────
	// 多个 MCP Server 时，工具名自动加前缀：filesystem__read_file
	// 单个 MCP Server 时，工具名保持原样：read_file
	fmt.Println("\n=== 所有可用工具 ===")
	agent := engine.NewSession("你是文件管理助手，可以读写文件和发起 HTTP 请求。", 8)

	// ── 使用示例 ─────────────────────────────────────────────────
	// LLM 会自动选择合适的工具（Hub skill 或 MCP tool）
	reply, err := agent.Chat(ctx, "帮我读取 /tmp/hello.txt 的内容")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("\nAgent:", reply)

	// ── 动态卸载 MCP Server ───────────────────────────────────────
	// 卸载后该 server 的工具立即从 LLM 可见列表移除
	engine.MCP().Detach("fetch")
	fmt.Println("\n✓ fetch server 已卸载")

	// ── 动态刷新工具列表 ─────────────────────────────────────────
	// 当 MCP Server 在运行中增减工具时，调用此方法更新缓存
	if err := engine.MCP().RefreshTools(ctx, "filesystem"); err != nil {
		log.Printf("refresh tools: %v", err)
	}

	_ = time.Second // 避免 import 未使用报错
}
