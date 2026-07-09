// 07_tracer/main.go
//
// 可观测性 Trace Tree 演示。
//
// 演示：
//   - 注入 SimpleTracer，查看完整追踪树
//   - 纯文本回复、工具调用、错误三种场景的追踪结构
//   - 使用 ExportTrace() 导出 JSON 树
//
// 运行：
//
//	go run . -c ../../config/account-openai.yaml
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
	"github.com/RedHuang-0622/Seele/contexts/tracer"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

var configPath = flag.String("c", "../../config/account-openai.yaml", "config path")

func main() {
	flag.Parse()
	ctx := context.Background()

	// ── 1. 加载配置 ─────────────────────────────────────────────────────
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
		MaxTokens:   1024,
		Timeout:     60,
		Temperature: 0.7,
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

	// ── 2. 注册一个测试工具 ─────────────────────────────────────────────
	agt.RegisterTool(
		"current_time",
		"返回当前时间",
		map[string]any{"type": "object", "properties": map[string]any{}},
		func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`{"time":%q}`, time.Now().Format("15:04:05")), nil
		},
	)

	// ── 3. 创建 Engine，注入 SimpleTracer ───────────────────────────────
	// 默认 NoopTracer 零开销；传入 SimpleTracer 后 Chat 结束可导出追踪树
	tr := tracer.NewSimpleTracer()
	eng := engine.New(agt,
		engine.WithTracer(tr),
		engine.WithSystemPrompt("你是一个助手。可以调用 current_time 查看当前时间。"))

	strategy := api.GetProviderStrategy(string(ls.Provider))
	fmt.Println("=== Trace Tree 可观测性演示 ===")
	fmt.Printf("  协议: %s (%s)\n\n", ls.Provider, first.BaseURL+strategy.Endpoint())

	// ══════════════════════════════════════════════════════════════════
	// 场景 A：纯文本回复
	// ══════════════════════════════════════════════════════════════════
	fmt.Println("─── 场景 A: 纯文本回复 ───")
	replyA, err := eng.Chat(ctx, "你好，请简单说一句话。")
	if err != nil {
		log.Fatalf("Chat A: %v", err)
	}
	fmt.Printf("  回复: %s\n\n", truncate(replyA, 120))

	treeA := eng.ExportTrace()
	fmt.Println("  追踪树 (JSON):")
	fmt.Println(treeA.String())
	fmt.Println()

	// ══════════════════════════════════════════════════════════════════
	// 场景 B：工具调用（time）
	// ══════════════════════════════════════════════════════════════════
	fmt.Println("─── 场景 B: 工具调用 ───")
	replyB, err := eng.Chat(ctx, "现在几点了？调用 current_time 工具。")
	if err != nil {
		log.Fatalf("Chat B: %v", err)
	}
	fmt.Printf("  回复: %s\n\n", truncate(replyB, 120))

	treeB := eng.ExportTrace()
	fmt.Println("  追踪树 (JSON):")
	fmt.Println(treeB.String())
	fmt.Println()

	// ── 统计数据 ──────────────────────────────────────────────────────
	fmt.Println("─── 统计摘要 ───")
	printSummary(treeA, "场景A")
	printSummary(treeB, "场景B")

	fmt.Println("\nDone. 每次 Chat 后 ExportTrace() 返回完整追踪树。")
	fmt.Println("注入 tracer.NewSimpleTracer() 即可启用，默认 NoopTracer 零开销。")
}

func printSummary(tree *tracer.Tree, label string) {
	if tree == nil || tree.Root == nil {
		return
	}
	r := tree.Root
	tokenCount := 0
	for _, c := range r.Children {
		if c.Kind == tracer.SpanLLMCall {
			if t, ok := c.Attrs["total_tokens"]; ok {
				fmt.Sscanf(t, "%d", &tokenCount)
			}
		}
	}
	fmt.Printf("  %s: %d children, %s total %d tok, status=%s\n",
		label, len(r.Children), fmtDuration(r.Duration), tokenCount, r.Status)
}

func fmtDuration(d time.Duration) string {
	ms := d.Milliseconds()
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
