// 01_hello_seele/main.go
//
// Seele 框架最简入门：创建 Agent、注册内联工具、发起对话。
//
// 无需启动任何外部服务 —— 工具是纯 Go 函数，零网络开销。
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. go run .

package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/RedHuang-0622/Seele/config"
	"github.com/RedHuang-0622/Seele/core/agent"
	"github.com/RedHuang-0622/Seele/provider"
)

// =============================================================================
// 声明工具参数 struct → SchemaOf 自动生成 JSON Schema
// =============================================================================
//
// 标签说明（全部可选）：
//   json:"name"              → Schema 属性名
//   json:"name,omitempty"    → 非必填字段
//   desc:"说明"               → description（LLM 据此决定传参）
//   enum:"A,B,C"             → 枚举约束（string 字段）
//   default:"值"             → 默认值

type TimeInput struct {
	Timezone string `json:"timezone,omitempty" desc:"IANA 时区，如 Asia/Shanghai，默认 Asia/Shanghai" default:"Asia/Shanghai"`
}

type CalcInput struct {
	Expression string `json:"expression" desc:"数学表达式，如 2+3*4"`
}

func main() {
	ctx := context.Background()

	// ── 1. 初始化 Engine ────────────────────────────────────────────
	llmCfg, err := config.LoadConfig("../config/config.yaml")
	if err != nil {
		log.Fatalf("LLM config load failed: %v", err)
	}
	engine, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("engine init failed: %v\n请确认 config/config.yaml 中的 ai_api_key 已正确填写", err)
	}
	defer engine.Shutdown()

	// ── 2. 注册内联工具：struct → SchemaOf 一行搞定 ────────────────

	engine.RegisterInlineTool(
		"get_current_time",
		"获取当前日期和时间，支持指定时区",
		provider.SchemaOf(TimeInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			loc, _ := time.LoadLocation("Asia/Shanghai")
			return fmt.Sprintf(`"当前时间: %s"`, time.Now().In(loc).Format("2006-01-02 15:04:05 MST")), nil
		},
	)

	engine.RegisterInlineTool(
		"calculator",
		"执行基本四则运算",
		provider.SchemaOf(CalcInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			return fmt.Sprintf(`"收到表达式: %s（示例：结果=42）"`, argsJSON), nil
		},
	)

	// ── 3. 查看已注册的工具 ────────────────────────────────────────
	fmt.Println("=== 已注册工具 ===")
	for _, t := range engine.Tools().Tools() {
		fmt.Printf("  • %s — %s\n", t.Function.Name, truncate(t.Function.Description, 60))
	}

	// ── 4. 创建 Session 并对话 ──────────────────────────────────────
	sess := engine.NewSession("你是一个有用的助手，可以查询时间和进行简单计算。", 8)

	reply, err := sess.Chat(ctx, "现在几点了？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("\n🤖 Agent:", reply)

	reply, err = sess.Chat(ctx, "帮我算一下 (15 + 27) * 3 等于多少？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("🤖 Agent:", reply)

	// ── 5. QuickChat：一次性对话 ────────────────────────────────────
	reply, err = engine.QuickChat(ctx, "你是一个简洁的助手。", "用一句话介绍 Go 语言。")
	if err != nil {
		log.Fatalf("quickchat error: %v", err)
	}
	fmt.Println("\n📝 QuickChat:", reply)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
