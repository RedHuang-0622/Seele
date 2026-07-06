// 04_mcp/main.go
//
// MCP (Model Context Protocol) 集成演示。
//
// MCP 是 Anthropic 提出的开放协议，让 LLM 安全访问外部工具和数据源。
// Seele 通过 MCPProvider 支持两种传输模式：
//   - stdio：启动子进程，通过标准输入/输出通信
//   - sse：  通过 HTTP+SSE 连接远程 MCP Server
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. 安装 MCP server（任选其一）：
//      npx -y @modelcontextprotocol/server-filesystem /tmp
//      uvx mcp-server-fetch
//   3. go run .

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/tool"
	mcpprov "github.com/RedHuang-0622/Seele/agent/tool/mcp"
	"github.com/RedHuang-0622/Seele/config"
)

func main() {
	ctx := context.Background()

	llmCfg, err := config.LoadConfig("../config/config.yaml")
	if err != nil {
		log.Fatalf("LLM config load failed: %v", err)
	}
	engine, err := agent.New(agent.Options{
		LLMConfig: llmCfg,
	})
	if err != nil {
		log.Fatalf("engine init failed: %v", err)
	}
	defer engine.Shutdown()

	mcp := engine.MCP()
	if mcp == nil {
		log.Fatal("MCP provider 初始化失败（引擎可能已关闭）")
	}

	// ── 方式 1：stdio 模式（本地子进程） ────────────────────────────
	//
	// 适用场景：本地工具，如文件系统操作、代码分析、数据库管理
	// Command 是启动命令，Args 是参数，Env 可注入环境变量

	// filesystem server：读写本地文件
	err = mcp.Attach(ctx, mcpprov.ServerConfig{
		Name:      "filesystem",
		Transport: "stdio",
		Command:   "npx",
		Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
		Env:       []string{},
	})
	if err != nil {
		log.Printf("⚠ attach filesystem: %v (跳过，请确认 npx 已安装)", err)
	} else {
		fmt.Println("✓ filesystem MCP server 已连接")
	}

	// fetch server：HTTP 请求
	err = mcp.Attach(ctx, mcpprov.ServerConfig{
		Name:      "fetch",
		Transport: "stdio",
		Command:   "uvx",
		Args:      []string{"mcp-server-fetch"},
	})
	if err != nil {
		log.Printf("⚠ attach fetch: %v (跳过，请确认 uvx 已安装)", err)
	} else {
		fmt.Println("✓ fetch MCP server 已连接")
	}

	// ── 方式 2：SSE 模式（远程服务） ────────────────────────────────
	//
	// 适用场景：团队共享的工具服务、第三方 MCP 服务
	err = mcp.Attach(ctx, mcpprov.ServerConfig{
		Name:      "remote-tools",
		Transport: "sse",
		URL:       "http://your-mcp-server.example.com/sse",
	})
	if err != nil {
		log.Printf("⚠ attach remote: %v (跳过，需替换为实际 URL)", err)
	}

	// ── 查看所有可用工具 ────────────────────────────────────────────
	//
	// 多个 MCP Server 时，工具名自动加前缀：filesystem__read_file
	// 单个 MCP Server 时，工具名保持原样：read_file
	fmt.Println("\n=== 所有可用工具（Hub + MCP + Inline） ===")
	for _, t := range engine.Tools().Tools() {
		fmt.Printf("  • %-30s — %s\n", t.Function.Name, truncate(t.Function.Description, 50))
	}

	// ── 使用 Agent 调用 MCP 工具 ────────────────────────────────────
	sess := engine.NewSession("你是文件管理助手，可以读写文件和发起 HTTP 请求。", 8)

	reply, err := sess.Chat(ctx, "帮我读取 /tmp/hello.txt 的内容")
	if err != nil {
		log.Printf("chat error: %v", err)
	}
	fmt.Println("\n🤖 Agent:", reply)

	// ── 动态管理 ────────────────────────────────────────────────────

	// 卸载 MCP Server — 该 server 的所有工具立即从 LLM 可见列表移除
	mcp.Detach("fetch")
	fmt.Println("\n✓ fetch server 已卸载")

	// 刷新工具列表 — 当 MCP Server 运行中增减工具时调用
	if err := mcp.RefreshTools(ctx, "filesystem"); err != nil {
		log.Printf("refresh tools: %v", err)
	}
	fmt.Println("✓ filesystem 工具列表已刷新")

	// ── 查看当前连接的 Server ───────────────────────────────────────
	fmt.Println("\n=== 当前连接的 MCP Server ===")
	for _, name := range mcp.ServerNames() {
		fmt.Printf("  • %s\n", name)
	}

	_ = time.Second
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
