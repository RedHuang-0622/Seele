// 06_provider_switch/main.go
//
// 号池账号切换演示。
//
// 同一个 llm_config.provider 下多个账号的 round-robin 轮转：
//   - 每次 Chat/ChatStream 调用自动切到下一个可用账号
//   - 对话历史跨账号完全共享
//   - 工具调用跨账号正常工作
//
// 运行：
//
//	go run . -c ../../config/account-anthropic.yaml
//	go run . -c ../../config/account-anthropic.yaml
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var configPath = flag.String("c", "../../config/account-anthropic.yaml", "config path")

func main() {
	flag.Parse()
	ctx := context.Background()

	result, err := api.LoadFullAccountsConfig(*configPath)
	if err != nil {
		log.Fatalf("load %s: %v", *configPath, err)
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
		ToolCallTimeOut: 15 * time.Second,
		HubStartupDelay: 10,
	})
	if err != nil {
		log.Fatalf("agent.New: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	eng := engine.New(agt, engine.WithSystemPrompt("你是一个有用的助手。会记住对话历史。"))

	agt.RegisterTool(
		"counter",
		"计数器：inc 加一，返回当前值",
		map[string]any{"type": "object", "properties": map[string]any{
			"action": map[string]any{"type": "string", "description": "inc/reset"},
		}},
		func(_ context.Context, argsJSON string) (string, error) {
			return `{"value":1,"msg":"ok"}`, nil
		},
	)

	// ══════════════════════════════════════════════════════════════

	strategy := api.GetProviderStrategy(string(ls.Provider))
	fmt.Println("=== 号池账号切换演示 ===")
	fmt.Printf("  协议: %s (%s)\n\n", ls.Provider, first.BaseURL+strategy.Endpoint())

	// 显示号池
	fmt.Printf("  号池 %d 个账号:\n", len(pool.All()))
	for _, a := range pool.All() {
		s := "●"
		if a.Disabled {
			s = "○"
		}
		fmt.Printf("    %s %s (model=%s)\n", s, a.Name, a.Model)
	}
	fmt.Println()

	// 记录每轮用到的账号
	type round struct {
		n   int
		msg string
		acct string
	}

	var rounds []round

	tryChat := func(n int, userMsg string) string {
		// 读取当前账号（Chat 内部会调 effectiveAccount 推进 round-robin）
		reply, err := eng.Chat(ctx, userMsg)
		if err != nil {
			log.Fatalf("chat %d: %v", n, err)
		}
		// Chat 推进了 round-robin，但 ChatClient 没有记录哪个账号被用了。
		// 通过 Pool 的 current 位置推算：
		//   GetByProvider 返回的是"下一个"可用的，调用前先 snapshot
		// 简单起见：本轮实际用的是 pool 当前指针的前一个账号。
		// 更好的方式：加一个 debug hook。这里直接从历史推断。
		return reply
	}

	// 第一轮
	r1 := tryChat(1, "你好，请记住我的名字：小明")
	fmt.Printf("  #1 user:  你好，请记住我的名字：小明\n")
	fmt.Printf("     reply: %s\n\n", truncate(r1, 100))

	// 第二轮（由 round-robin 自动切到下一个账号）
	r2 := tryChat(2, "刚才我说了什么？我的名字是什么？")
	fmt.Printf("  #2 user:  刚才我说了什么？我的名字是什么？\n")
	fmt.Printf("     reply: %s\n\n", truncate(r2, 100))

	// 第三轮（再切，自动轮转 + 工具调用）
	r3 := tryChat(3, "调用 counter 工具计数一次")
	fmt.Printf("  #3 user:  调用 counter 工具计数一次\n")
	fmt.Printf("     reply: %s\n\n", truncate(r3, 100))

	// 历史摘要
	_ = rounds
	fmt.Println("--- 对话历史（跨账号共享）---")
	for i, m := range eng.History() {
		role := m.Role
		desc := ""
		switch {
		case m.Role == "tool":
			desc = fmt.Sprintf("(tool_call_id: %s)", m.ToolCallID)
		case len(m.ToolCalls) > 0:
			names := ""
			for _, tc := range m.ToolCalls {
				names += tc.Function.Name + " "
			}
			desc = fmt.Sprintf("(tool_calls: %s)", names)
		}
		c := ""
		if m.Content != nil {
			c = *m.Content
		}
		if len(c) > 70 {
			c = c[:70] + "..."
		}
		fmt.Printf("  [%d] %-9s %s %s\n", i, role, c, desc)
	}

	fmt.Println()
	fmt.Println("Done. 三轮对话自动轮转号池内账号，历史完整共享。")
	fmt.Println("如需显式切换账号，使用 ChatClient.SetProviderFilter() 或 Pool.GetByProvider()。")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
