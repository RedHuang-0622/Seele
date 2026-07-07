// 01_hello_seele/main.go
//
// Seele 框架最简入门：创建 Agent → 注入 Engine → 发起对话。
//
// 架构流程：
//
//	agent (装配 config + tools)
//	  └── engine.New(agent) (注入 agent，内部管理 contexts)
//	        └── eng.Chat(ctx, "hello") (唯一对话入口，内部 ReAct loop)
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

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/config"
	"github.com/RedHuang-0622/Seele/agent/core/tool"
)

// =============================================================================
// 声明工具参数 struct → SchemaOf 自动生成 JSON Schema
// =============================================================================

type TimeInput struct {
	Timezone string `json:"timezone,omitempty" desc:"IANA 时区，如 Asia/Shanghai，默认 Asia/Shanghai" default:"Asia/Shanghai"`
}

type CalcInput struct {
	Expression string `json:"expression" desc:"数学表达式，如 2+3*4"`
}

func main() {
	ctx := context.Background()

	// ── 1. Agent：装配 LLM 配置 + 工具 ────────────────────────────
	llmCfg, err := config.LoadConfig("../config/config.yaml")
	if err != nil {
		log.Fatalf("LLM config load failed: %v", err)
	}
	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		ToolCallTimeOut: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("agent init failed: %v\n请确认 config/config.yaml 中的 ai_api_key 已正确填写", err)
	}
	defer agt.Shutdown()

	// ── 2. 注册内联工具 ────────────────────────────────────────────
	agt.RegisterInlineTool(
		"get_current_time",
		"获取当前日期和时间，支持指定时区",
		tool.SchemaOf(TimeInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			loc, _ := time.LoadLocation("Asia/Shanghai")
			return fmt.Sprintf(`"当前时间: %s"`, time.Now().In(loc).Format("2006-01-02 15:04:05 MST")), nil
		},
	)

	agt.RegisterInlineTool(
		"calculator",
		"执行基本四则运算",
		tool.SchemaOf(CalcInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			return fmt.Sprintf(`"收到表达式: %s（示例：结果=42）"`, argsJSON), nil
		},
	)

	// ── 3. 查看已注册的工具 ────────────────────────────────────────
	fmt.Println("=== 已注册工具 ===")
	for _, t := range agt.Tools().Tools() {
		fmt.Printf("  • %s — %s\n", t.Function.Name, truncate(t.Function.Description, 60))
	}

	// ── 4. Engine：注入 Agent，内部接管 contexts ───────────────────
	eng := engine.New(agt, engine.WithSystemPrompt("你是一个有用的助手，可以查询时间和进行简单计算。"))

	// 多轮对话（engine 内部维护 history）
	reply, err := eng.Chat(ctx, "现在几点了？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("\n🤖 Agent:", reply)

	reply, err = eng.Chat(ctx, "帮我算一下 (15 + 27) * 3 等于多少？")
	if err != nil {
		log.Fatalf("chat error: %v", err)
	}
	fmt.Println("🤖 Agent:", reply)

	// ── 5. 一次性对话：用完即弃 ────────────────────────────────────
	reply, err = engine.New(agt, engine.WithSystemPrompt("你是一个简洁的助手。")).Chat(ctx, "用一句话介绍 Go 语言。")
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
