// cmd/repl/main.go
//
// Seele 交互式 REPL — 支持 /switch 切换号池 Provider。
//
// 运行：
//
//	go run ./cmd/repl/ -c ../config/config.yaml -a ../config/accounts.yaml
//
// 命令：
//
//	/switch openai     切换到 OpenAI 号池
//	/switch anthropic  切换到 Anthropic 号池
//	/switch            查看当前 Provider
//	/history           查看对话历史
//	/clear             清空历史
//	/help              帮助
//	/exit              退出
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/config"
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/engine"
)

var (
	configPath  = flag.String("c", "../config/config.yaml", "LLM 配置")
	accountPath = flag.String("a", "", "号池账号 YAML（可选，多 Provider 用）")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	llmCfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	opts := agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 10 * time.Second,
		HubStartupDelay: 10,
	}
	if *accountPath != "" {
		opts.ProviderAccountPath = *accountPath
	}

	agt, err := agent.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Agent 初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer agt.Shutdown()

	// ChatClient 和号池
	chatClient := agt.LLM().(*api.ChatClient)
	pool := chatClient.AccountPool()

	// 可观测性追踪器（默认开启，每次 Chat 后可通过 /trace 查看）
	tr := tracer.NewSimpleTracer()

	eng := engine.New(agt,
		engine.WithTracer(tr),
		engine.WithSystemPrompt("你是一个有用的 AI 助手，支持多 Provider 切换。"))

	// 注册一个简单的工具
	agt.RegisterTool(
		"get_time",
		"获取当前时间",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`"当前时间: %s"`, time.Now().Format("2006-01-02 15:04:05")), nil
		},
	)

	// ── 欢迎 ──────────────────────────────────────────────────────
	fmt.Printf("\n  🤖  Seele REPL — 多 Provider 对话终端\n")
	fmt.Println(strings.Repeat("─", 50))
	fmt.Print("  输入 /help 查看命令\n\n")

	if pool != nil {
		for _, a := range pool.All() {
			s := "●"
			if a.Disabled {
				s = "○"
			}
			fmt.Printf("  %s [%s] %s → %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
	}
	pf := chatClient.ProviderFilter()
	fmt.Printf("  🔄 当前: %s\n\n", providerLabel(pf))

	// ── 主循环 ────────────────────────────────────────────────────
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("  🗣  > ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		if strings.HasPrefix(input, "/") {
			doCommand(input, chatClient, eng)
			continue
		}

		start := time.Now()
		reply, err := eng.Chat(ctx, input)
		elapsed := time.Since(start).Seconds()
		if err != nil {
			fmt.Printf("  ❌ %v\n", err)
			continue
		}

		// 统计 token（从追踪树提取）
		totalTokens := "?"
		if tree := eng.ExportTrace(); tree != nil && tree.Root != nil {
			for _, c := range tree.Root.Children {
				if c.Kind == tracer.SpanLLMCall {
					if t, ok := c.Attrs["total_tokens"]; ok {
						totalTokens = t
						break
					}
				}
			}
		}
		pf := chatClient.ProviderFilter()
		fmt.Printf("  🤖 [%s %s %.1fs] %s\n", providerLabel(pf), totalTokens+"tok", elapsed, reply)
	}
}

func doCommand(cmd string, cc *api.ChatClient, eng *engine.Engine) {
	parts := strings.Fields(cmd)
	switch strings.ToLower(parts[0]) {

	case "/exit", "/quit":
		fmt.Println("  👋 再见！")
		os.Exit(0)

	case "/help":
		fmt.Println("  ─── 命令 ───")
		fmt.Println("  /switch           查看当前 Provider")
		fmt.Println("  /switch openai    切换到 OpenAI")
		fmt.Println("  /switch anthropic 切换到 Anthropic")
		fmt.Println("  /trace            显示上次对话的追踪树（token 明细、耗时、调用链）")
		fmt.Println("  /history          查看历史")
		fmt.Println("  /clear            清空历史")
		fmt.Println("  /pool             号池状态")
		fmt.Println("  /exit             退出")

	case "/switch":
		if len(parts) < 2 {
			fmt.Printf("  🔄 当前: %s\n", providerLabel(cc.ProviderFilter()))
			fmt.Println("  用法: /switch openai 或 /switch anthropic")
			return
		}
		target := strings.ToLower(parts[1])
		switch target {
		case "openai":
			cc.SetProviderFilter(api.ProviderOpenAI)
			fmt.Println("  🔄 → OpenAI")
		case "anthropic":
			cc.SetProviderFilter(api.ProviderAnthropic)
			fmt.Println("  🔄 → Anthropic")
		default:
			fmt.Printf("  ❌ 不支持的 Provider: %s\n", target)
		}

	case "/trace":
		tree := eng.ExportTrace()
		if tree == nil || tree.Root == nil {
			fmt.Println("  📊 暂无追踪数据（先发一条消息）")
			return
		}
		fmt.Println("  📊 追踪树:")
		for _, line := range strings.Split(tree.String(), "\n") {
			fmt.Println("  " + line)
		}

	case "/history":
		hist := eng.History()
		if len(hist) == 0 {
			fmt.Println("  📝 空")
			return
		}
		fmt.Printf("  📝 %d 条\n", len(hist))
		for i, m := range hist {
			c := ""
			if m.Content != nil {
				c = *m.Content
			}
			if len(c) > 150 {
				c = c[:150] + "..."
			}
			if len(m.ToolCalls) > 0 {
				c = "[tool: " + m.ToolCalls[0].Function.Name + "]"
			}
			fmt.Printf("  [%d] %-9s %s\n", i, m.Role, c)
		}

	case "/clear":
		eng.ClearHistory()
		fmt.Println("  🗑  已清空")

	case "/pool":
		pool := cc.AccountPool()
		if pool == nil {
			fmt.Println("  📡 单账号模式")
			return
		}
		for _, a := range pool.All() {
			s := "●"
			if a.Disabled {
				s = "○"
			}
			fmt.Printf("  %s [%s] %s → %s\n", s, a.Provider, a.Name, a.BaseURL)
		}
		fmt.Printf("  🔄 当前筛选: %s\n", providerLabel(cc.ProviderFilter()))

	default:
		fmt.Printf("  ❌ 未知命令: %s\n", parts[0])
	}
}

func providerLabel(p api.ProviderType) string {
	if p == "" {
		return "round-robin"
	}
	return string(p)
}
