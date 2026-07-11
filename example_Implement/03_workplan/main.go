// 03_workplan/main.go
//
// WorkPlan 工作流引擎完整演示。
//
// WorkPlan 是 Seele 的多 Agent 编排层，将 LLM 节点编织为有向图：
//   - 构建期：链式调用 Auto/If/Switch/Loop/Fork/Emit... 声明意图
//   - 执行期：Run() 遍历图，自动路由、暂停审批、汇合结果
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. go run .

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/engine"

	"github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
)

// =============================================================================
// EngineFactory：将 *agent.Agent 适配为 workplan.AgentFactory
// =============================================================================

type EngineFactory struct {
	engine *agent.Agent
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	return engine.New(f.engine, engine.WithSystemPrompt(systemPrompt))
}

// =============================================================================
// 示例 1：线性流程（Auto → Emit → Checkpoint → Auto）
// =============================================================================

func ExampleLinear(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是一个数据分析助手，擅长从数据中提取洞察。"))

	wp.Auto("获取数据", "分析以下销售数据并提取关键指标：Q1 营收 120 万，Q2 营收 155 万，Q3 营收 140 万，Q4 营收 180 万").
		Emit("保存分析", "sales_analysis").
		Auto("生成报告", "根据分析生成管理层报告摘要：{{.Vars.sales_analysis}}")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("📊 报告摘要:", truncate(result.FinalOutputString(), 120))
}

// =============================================================================
// 示例 2：条件分支（If / Switch）
// =============================================================================

func ExampleBranching(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是系统监控助手。"))

	// ── If：二路分支 ──────────────────────────────────────────────────
	wp.Auto("检测状态",
		"检测服务状态，回复中必须包含以下之一：「正常」「异常'").

		If("判断",
			workplan.Contains("异常"),
			"告警处理",
			"常规记录",
		).

		Auto("告警处理", "触发告警通知值班人员").
		Auto("常规记录", "记录正常状态到日志").
		Auto("通知", "发送处理完成通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("🔀 If 分支:", result.FinalOutputString())

	// ── Switch：多路分支 ─────────────────────────────────────────────
	wp2 := workplan.New(factory, workplan.WithDefaultPrompt("你是工单分发助手。"))

	wp2.Auto("分析工单",
		"分析以下工单内容，回复中必须包含优先级之一：「P0紧急」「P1高」「P2中」「P3低」：数据库主库宕机，影响全部在线业务").
		Switch("工单路由",
			workplan.Case(workplan.Contains("P0紧急"), "紧急处理"),
			workplan.Case(workplan.Contains("P1高"), "高优处理"),
			workplan.Case(workplan.Contains("P2中"), "常规处理"),
			workplan.Default("低优处理"),
		).
		Auto("紧急处理", "立即拉起应急响应").
		Auto("高优处理", "加入高优队列").
		Auto("常规处理", "加入常规队列").
		Auto("低优处理", "加入低优队列").
		Auto("结束", "已分发")

	result2, _ := wp2.Run(ctx)
	fmt.Println("🔀 Switch 分支:", result2.FinalOutputString())
}

// =============================================================================
// 示例 3：Loop + Signal
// =============================================================================

func ExampleLoop(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是故障修复助手，每次修复一个问题。"))

	wp.Auto("修复执行体",
		"根据上次修复结果继续修复：\n{{.PrevResult}}\n如果系统已完全恢复，在回复中包含「已完全恢复」。",
	)

	sig := wp.Loop("修复循环", "修复执行体",
		workplan.Until(workplan.Contains("已完全恢复")),
		workplan.MaxIter(5),
		workplan.OnExhausted("人工介入"),
	)

	sig.OnUpdate(func(jsonVal string) {
		log.Printf("[修复进度] 第 %d 次迭代完成: %s", sig.Iter(), jsonVal)
	})

	wp.Auto("人工介入", "生成人工介入告警，修复循环已耗尽")
	wp.Auto("完成通知", "发送修复完成通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("\n🔄 Loop 结果: 共迭代 %d 次\n", sig.Iter())
	fmt.Printf("   最终输出: %s\n", result.FinalOutputString())
}

// =============================================================================
// 示例 4：Fork 并发（Multi-Agent）
// =============================================================================

func ExampleFork(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是软件团队协调助手。"))

	wp.
		Auto("需求分析", "分析需求并拆解任务：用户登录功能，包含前端页面、后端接口、测试用例").
		Emit("保存需求", "requirement").

		Fork("并行开发", []workplan.ForkBranch{
			{
				Label:        "前端工程师",
				SystemPrompt: "你是资深前端工程师，专注于 React 和用户体验。",
				Input:        "实现登录页面，需求：{{.Vars.requirement}}",
			},
			{
				Label:        "后端工程师",
				SystemPrompt: "你是资深后端工程师，专注于 Go 和高并发。",
				Input:        "实现登录接口，需求：{{.Vars.requirement}}",
			},
			{
				Label:        "测试工程师",
				SystemPrompt: "你是资深测试工程师，专注于自动化测试。",
				Input:        "编写测试用例，需求：{{.Vars.requirement}}",
			},
		}, 3).

		Auto("汇总报告", "汇总各角色的产出，生成集成计划：\n{{.PrevResult}}")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n⚡ Fork 结果:")
	fmt.Printf("   需求分析: %s\n", truncate(result.Vars["requirement"], 80))
	for _, nr := range result.NodeResults {
		icon := "✓"
		if nr.Skipped {
			icon = "⏭"
		}
		fmt.Printf("   %s %-12s %-20s\n", icon, nr.Kind, nr.NodeID)
	}
}

// =============================================================================
// 示例 5：Approve（人工审批）
// =============================================================================

func ExampleApprove(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是部署审核助手。"))

	gate := workplan.NewCLIApprovalGate()

	wp.Auto("生成变更计划",
		"为以下变更生成详细的部署计划：将用户服务从 v1.2 升级到 v2.0，包含数据库迁移").
		Emit("保存计划", "deploy_plan").
		Approve("确认部署",
			"即将执行以下部署计划：{{.Vars.deploy_plan}}\n请确认是否执行。",
			gate,
		).
		Auto("完成通知", "部署已确认并执行完成")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n✅ 部署完成:", result.FinalOutputString())
}

// =============================================================================
// 示例 6：Pipeline 语法糖
// =============================================================================

func ExampleSugars(factory workplan.AgentFactory) {
	ctx := context.Background()

	// ── Pipeline ──────────────────────────────────────────────────────
	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是文档处理助手。"))
	wp.Pipeline(
		workplan.Step("提取内容", "从以下文档提取关键信息：见 Seele 框架完成重构，核心变更包括 ToolProvider 策略模式 + WorkPlan 图引擎"),
		workplan.Step("结构化", "将以上信息结构化为 JSON：\n{{.PrevResult}}"),
		workplan.Step("生成摘要", "基于结构化数据生成 50 字摘要：\n{{.PrevResult}}"),
	)
	result, _ := wp.Run(ctx)
	fmt.Println("📋 Pipeline:", result.FinalOutputString())
}

// =============================================================================
// 示例 7：图引擎检查
// =============================================================================

func ExampleGraphInspection(factory workplan.AgentFactory) {
	wp := workplan.New(factory, workplan.WithDefaultPrompt("你是助手。"))

	wp.Auto("分析", "分析数据").
		If("判断", workplan.Contains("紧急"), "紧急处理", "常规处理").
		Auto("紧急处理", "立即处理").
		Auto("常规处理", "排期处理").
		Auto("通知", "发送通知")

	fmt.Println("🔍 构建的图包含以下节点：")
	fmt.Println("   分析 → 判断 → [紧急处理 | 常规处理] → 通知")
}

// =============================================================================
// main

var configPath = flag.String("c", "../../config/account-anthropic.yaml", "config path")

func main() {
	flag.Parse()

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

	engine, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		HubStartupDelay: 10,
	})
	if err != nil {
		log.Fatalf("engine init failed: %v", err)
	}
	defer engine.Shutdown()

	chatClient := engine.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	if ls.Provider != "" {
		chatClient.SetProvider(ls.Provider)
	}

	factory := &EngineFactory{engine: engine}
	ExampleLinear(factory)
	// ExampleBranching(factory)
	// ExampleLoop(factory)
	// ExampleFork(factory)
	// ExampleApprove(factory)
	// ExampleSugars(factory)
	// ExampleGraphInspection(factory)

	fmt.Println("\n✅ 示例运行完毕")
}

// ── 工具函数 ──────────────────────────────────────────────────────────

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// 避免 import 未使用
var _ = strings.Contains
var _ = time.Now
