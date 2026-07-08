// 06_provider_switch/main.go
//
// Seele 号池多 Provider 切换演示（真实 API）：
//
//	1. /switch openai     -> "你好，请记住我的名字：小明"
//	2. /switch anthropic  -> "刚才我说了什么？"（需历史上下文）
//	3. /switch openai     -> "刚刚你回复了什么内容？"（追忆对话）
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. 创建 ../config/accounts.yaml（可选，多 Provider 切换用，见末尾格式）
//   3. go run .
//
// 无 accounts.yaml 时不执行切换演示，只展示单 Provider 基础对话。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/config"
	"github.com/RedHuang-0622/Seele/engine"
)

func main() {
	ctx := context.Background()

	// ── 1. 加载 LLM 配置 ────────────────────────────────────────────
	llmCfg, err := config.LoadConfig("../config/config.yaml")
	if err != nil {
		log.Fatalf("config load failed: %v\n请编辑 ../config/config.yaml 填入 API Key", err)
	}
	if llmCfg.APIKey == "" || llmCfg.APIKey == "sk-your-key-here" {
		log.Fatalf("config.yaml 中 ai_api_key 未配置，请先填入你的 API Key")
	}

	// ── 2. 加载号池配置（可选）──────────────────────────────────────
	accountPath := "../config/accounts.yaml"
	accountsExist := fileExists(accountPath)

	opts := agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 15 * time.Second,
		HubStartupDelay: 10,
	}
	if accountsExist {
		opts.ProviderAccountPath = accountPath
	}

	// ── 3. 创建 Agent ──────────────────────────────────────────────
	agt, err := agent.New(opts)
	if err != nil {
		log.Fatalf("agent init failed: %v", err)
	}
	defer agt.Shutdown()

	// ── 4. 获取 ChatClient，注入号池 ──────────────────────────────
	chatClient := agt.LLM().(*api.ChatClient)
	pool := chatClient.AccountPool()

	// ── 5. Engine ──────────────────────────────────────────────────
	eng := engine.New(agt, engine.WithSystemPrompt(
		"你是一个多 Provider 助手，会记住对话历史。当前回答中请标明你的身份（如 GPT 或 Claude）。",
	))

	// ── 6. 注册工具 ────────────────────────────────────────────────
	agt.RegisterTool(
		"get_time",
		"获取当前时间",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`"当前时间: %s"`, time.Now().Format("2006-01-02 15:04:05")), nil
		},
	)

	// ── 7. 显示号池信息 ──────────────────────────────────────────
	fmt.Println("=== Seele 多 Provider 切换演示 ===")
	fmt.Println()

	if pool != nil {
		fmt.Printf("📡 号池: %d 个账号\n", len(pool.All()))
		for _, a := range pool.All() {
			s := "●"
			if a.Disabled {
				s = "○"
			}
			url := shorten(a.BaseURL, 40)
			fmt.Printf("   %s [%s] %s → %s\n", s, a.Provider, a.Name, url)
		}
	} else {
		fmt.Printf("📡 单账号: %s\n", llmCfg.BaseURL)
	}

	// 检测是否有多个 Provider
	hasOpenAI := false
	hasAnthropic := false
	if pool != nil {
		for _, a := range pool.All() {
			if a.Provider == api.ProviderOpenAI && !a.Disabled {
				hasOpenAI = true
			}
			if a.Provider == api.ProviderAnthropic && !a.Disabled {
				hasAnthropic = true
			}
		}
	}
	canSwitch := hasOpenAI && hasAnthropic

	if !canSwitch {
		fmt.Println()
		fmt.Println("⚠️  未检测到多 Provider 配置，仅演示单 Provider 基础对话。")
		fmt.Println("   要实现多 Provider 切换，请创建 ../config/accounts.yaml，格式：")
		fmt.Println(`
  accounts:
    - name: openai-main
      provider: openai
      base_url: https://api.openai.com/v1
      api_key: sk-xxx
      model: gpt-4
      priority: 1
    - name: anthropic-main
      provider: anthropic
      base_url: https://api.anthropic.com
      api_key: sk-ant-xxx
      model: claude-3-5-sonnet-20241022
      priority: 2`)
		fmt.Println()
		// 单 Provider 演示
		demoSimpleChat(eng)
		return
	}

	// ── 8. 多 Provider 切换演示 ─────────────────────────────────
	fmt.Println()
	fmt.Println("✅ 检测到多 Provider 号池，开始切换演示")
	fmt.Println()

	// Step 1: OpenAI
	fmt.Println("─── Step 1: /switch openai ───")
	chatClient.SetProviderFilter(api.ProviderOpenAI)
	r1, err := eng.Chat(ctx, "你好，请记住我的名字：小明")
	printReply("OpenAI", r1, err)

	// Step 2: Anthropic
	fmt.Println("─── Step 2: /switch anthropic ───")
	chatClient.SetProviderFilter(api.ProviderAnthropic)
	r2, err := eng.Chat(ctx, "刚才我说了什么？我的名字是什么？")
	printReply("Anthropic", r2, err)

	// Step 3: 回到 OpenAI 追问
	fmt.Println("─── Step 3: /switch openai（追忆）───")
	chatClient.SetProviderFilter(api.ProviderOpenAI)
	r3, err := eng.Chat(ctx, "刚刚 Anthropic 回复了什么内容？把整个对话总结一下。")
	printReply("OpenAI", r3, err)

	// 历史
	fmt.Println("─── 完整对话历史 ───")
	for i, m := range eng.History() {
		c := ""
		if m.Content != nil {
			c = *m.Content
		}
		if len(c) > 120 {
			c = c[:120] + "..."
		}
		if len(m.ToolCalls) > 0 {
			c = "[tool_call: " + m.ToolCalls[0].Function.Name + "]"
		}
		fmt.Printf("  [%d] %-9s %s\n", i, m.Role, c)
	}
	fmt.Println()
	fmt.Println("✅ 要点：对话历史跨 Provider 完全共享，/switch 只切换 API 路由，不丢上下文。")
}

func demoSimpleChat(eng *engine.Engine) {
	ctx := context.Background()

	fmt.Println("─── 基础对话 ───")
	r1, err := eng.Chat(ctx, "你好！请简单介绍一下你自己。")
	printReply("Agent", r1, err)

	r2, err := eng.Chat(ctx, "现在几点了？")
	printReply("Agent", r2, err)
}

func printReply(label string, content string, err error) {
	if err != nil {
		fmt.Printf("  ❌ [%s] %v\n\n", label, err)
	} else {
		fmt.Printf("  🤖 [%s] %s\n\n", label, content)
	}
}

// ── 辅助 ──────────────────────────────────────────────────────────

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

