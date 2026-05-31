// quick_start/main.go
//
// 最简单的起点：启动 Engine，创建 Agent，发起一次对话。
// 运行前请先启动 weather skill：go run ./skills/weather/

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sukasukasuka123/Seele/sdk/api"
)

func main() {
	ctx := context.Background()

	// ── 1. 初始化 Engine ──────────────────────────────────────────
	// Engine 会自动完成：加载 registry → 启动 Hub → 创建 Runtime
	engine, err := api.New(api.Options{
		RegistryPath:    "config/registry.yaml",
		LLMConfigPath:   "config/config.yaml",
		ToolCallTimeOut: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("engine init failed: %v", err)
	}
	defer engine.Shutdown()

	// ── 2. 查看当前可用工具 ───────────────────────────────────────
	fmt.Println("=== 已注册的 Hub Skills ===")
	for _, s := range engine.Hub().Skills() {
		fmt.Printf("  %-20s [%s]\n", s.Name, s.Addr)
	}

	// ── 3. 创建 Agent 并对话 ──────────────────────────────────────
	agent := engine.NewSession("你是一个天气助手，可以查询城市天气。", 8)

	// 第一轮：LLM 会自动调用 weather tool
	reply, err := agent.Chat(ctx, "北京今天天气怎么样？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("\nAgent:", reply)

	// 第二轮：有记忆，知道上文说的是北京
	reply, err = agent.Chat(ctx, "那上海呢？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("Agent:", reply)
}
