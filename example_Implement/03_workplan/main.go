// 03_workplan/main.go
//
// WorkPlan 工作流引擎完整演示。
//
// WorkPlan 是 Seele 的多 Agent 编排层，将 LLM 节点编织为有向图：
//   - 构建期：链式调用 Auto/If/Switch/Loop/Fork/Emit... 声明意图
//   - 执行期：Run() 遍历图，自动路由、暂停审批、汇合结果
//
// v0.3 重构后，底层是 Graph + Edge + NodeRunner 图引擎，
// 上层糖方法 API 完全兼容，无需改动业务代码。
//
// 运行前：
//   1. 编辑 ../config/config.yaml，填入你的 LLM API Key
//   2. go run .

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/config"
	"github.com/RedHuang-0622/Seele/agent"
	seelectx "github.com/RedHuang-0622/Seele/contexts"
	"github.com/RedHuang-0622/Seele/workplan"
)

// =============================================================================
// EngineFactory：将 *agent.Agent 适配为 workplan.AgentFactory
// =============================================================================
//
// WorkPlan 通过 AgentFactory 接口创建 Agent，不直接依赖 Engine 具体类型。
// 这层薄适配是解耦的关键：测试时可以注入 MockFactory。

type EngineFactory struct {
	engine *agent.Agent
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	return seelectx.New(f.engine.LLM(), f.engine.Tools(), systemPrompt, seelectx.SessionConfig{MaxLoops: 10})
}

// =============================================================================
// 示例 1：线性流程（Auto → Emit → Checkpoint → Auto）
// =============================================================================
//
// 最基础的 WorkPlan：三步走，适合理解基本概念。
//
// 概念：
//   Auto      — Agent 节点，执行 LLM 推理循环（可自动调用工具）
//   Emit      — 将当前输出保存到命名变量，后续节点用 {{.Vars.key}} 引用
//   Checkpoint — 保存快照，失败时可用于回溯

func ExampleLinear(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是一个数据分析助手，擅长从数据中提取洞察。")

	wp.Auto("获取数据", "分析以下销售数据并提取关键指标：Q1 营收 120 万，Q2 营收 155 万，Q3 营收 140 万，Q4 营收 180 万").
		Emit("保存分析", "sales_analysis").             // 保存分析结果，后续节点可引用
		Checkpoint("分析快照").                            // 出错时可从此处回溯
		Auto("生成报告", "根据分析生成管理层报告摘要：{{.Vars.sales_analysis}}")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("📊 报告摘要:", truncate(result.FinalOutputString(), 120))
	fmt.Printf("   Checkpoint 数量: %d\n", len(result.Checkpoints))
}

// =============================================================================
// 示例 2：条件分支（If / Switch）
// =============================================================================
//
// If 和 Switch 是控制节点，它们不执行 LLM，只做路由。
// 条件函数接收上一节点的纯文本输出（fromJSON 去掉 JSON 引号包裹）。

func ExampleBranching(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是系统监控助手。")

	// ── If：二路分支 ──────────────────────────────────────────────────
	// 注意：trueID / falseID 必须是后面注册的节点 ID
	wp.Auto("检测状态",
		"检测服务状态，回复中必须包含以下之一：「正常」「异常」").

		If("判断",
			workplan.Contains("异常"), // 快捷条件：输出包含"异常"→走"告警处理"
			"告警处理",                // trueID
			"常规记录",                // falseID
		).

		// true 分支
		Auto("告警处理", "触发告警通知值班人员",
			workplan.WithNext("通知"), // 执行完跳到"通知"，跳过"常规记录"
		).

		// false 分支
		Auto("常规记录", "记录正常状态到日志").

		// 汇合点
		Auto("通知", "发送处理完成通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("🔀 If 分支:", result.FinalOutputString())

	// ── Switch：多路分支 ─────────────────────────────────────────────
	// cases 按注册顺序匹配，Default(...) 兜底
	wp2 := workplan.New(factory, nil, "你是工单分发助手。")

	wp2.Auto("分析工单",
		"分析以下工单内容，回复中必须包含优先级之一：「P0紧急」「P1高」「P2中」「P3低」：数据库主库宕机，影响全部在线业务").
		Switch("工单路由",
			workplan.Case(workplan.Contains("P0紧急"), "紧急处理"),
			workplan.Case(workplan.Contains("P1高"), "高优处理"),
			workplan.Case(workplan.Contains("P2中"), "常规处理"),
			workplan.Default("低优处理"),
		).
		Auto("紧急处理", "立即拉起应急响应", workplan.WithNext("结束")).
		Auto("高优处理", "加入高优队列", workplan.WithNext("结束")).
		Auto("常规处理", "加入常规队列", workplan.WithNext("结束")).
		Auto("低优处理", "加入低优队列", workplan.WithNext("结束")).
		Auto("结束", "已分发")

	result2, _ := wp2.Run(ctx)
	fmt.Println("🔀 Switch 分支:", result2.FinalOutputString())
}

// =============================================================================
// 示例 3：Loop + Signal（核心特性）
// =============================================================================
//
// Loop 是最特殊的节点 —— 它返回一个 *Signal 活引用。
//
// Signal 不是"等 Loop 结束后拿的死值"，而是"实时更新的活引用"：
//   - Loop 每次迭代完成后，内部调用 signal.set(result)
//   - 如果你注册了 OnUpdate 回调，每次迭代完成就立刻触发
//   - 适合实时推送进度（WebSocket、SSE、日志）
//
// 退出条件：
//   - Until(cond) — cond 返回 true 时正常退出
//   - MaxIter(n)  — 迭代 n 次后强制退出，走 OnExhausted 指定节点
//   - ctx 取消    — 通过 context 外部终止

func ExampleLoop(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是故障修复助手，每次修复一个问题。")

	// 注册循环体（Loop 引用它的 ID）
	wp.Auto("修复执行体",
		"根据上次修复结果继续修复：\n{{.PrevResult}}\n如果系统已完全恢复，在回复中包含「已完全恢复」。",
	)

	// 注册 Loop 节点
	sig := wp.Loop("修复循环", "修复执行体",
		workplan.Until(workplan.Contains("已完全恢复")), // 正常退出条件
		workplan.MaxIter(5),                             // 最多 5 次
		workplan.OnExhausted("人工介入"),                  // 耗尽后跳转
	)

	// 注册 OnUpdate：每次迭代完成立刻触发（不等 Loop 结束）
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
//
// Fork 启动多个独立 Agent，每个有自己的推理循环和历史。
// 所有分支完成后，结果汇合为 JSON object：
//
//	{
//	  "前端工程师": "实现了登录页面...",
//	  "后端工程师": "实现了登录接口..."
//	}
//
// 与 Auto 内部的 tool_call 并发的区别：
//   Auto 内：同一个 Agent，工具级并发
//   Fork：  多个独立 Agent，Agent 级并发

func ExampleFork(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是软件团队协调助手。")

	wp.
		// Step 1：需求分析
		Auto("需求分析", "分析需求并拆解任务：用户登录功能，包含前端页面、后端接口、测试用例").

		// Step 2：保存需求到命名变量
		Emit("保存需求", "requirement").

		// Step 3：三个角色并行工作，各自有独立的 SystemPrompt
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
		}).

		// Step 4：汇总
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
// 示例 5：Approve / Gate（人工审批）
// =============================================================================
//
// Approve 节点在执行前暂停，等待人工决策。
// Gate 是 Approve 的极简版：只有「执行」和「终止」两个选项。
//
// 执行流程：
//   1. Run() 执行到 Approve 节点 → 生成计划 → 暂停
//   2. 返回 StateAwaitingApproval 状态 + PendingQuestion
//   3. 调用方展示 Question，等待用户选择
//   4. 调用 SetDecision(key) + Resume() 继续执行

func ExampleApprove(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是部署审核助手。")

	wp.Auto("生成变更计划",
		"为以下变更生成详细的部署计划：将用户服务从 v1.2 升级到 v2.0，包含数据库迁移").
		Emit("保存计划", "deploy_plan").

		// Gate：极简二选一
		Gate("确认部署",
			"即将执行以下部署计划：{{.Vars.deploy_plan}}\n请确认是否执行。",
		).
		Auto("完成通知", "部署已确认并执行完成")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	if wp.ExecState() == workplan.StateAwaitingApproval {
		q := wp.PendingQuestion()
		fmt.Println("\n🛑 等待审批:")
		fmt.Printf("   Q: %s\n", q.Content)
		fmt.Printf("   选项: %v\n", q.Options)
		// 生产环境中，这里会等待用户输入，然后：
		//   wp.SetDecision("execute")  // 或 "abort"
		//   result, err = wp.Resume(ctx)
	}

	_ = result
}

// =============================================================================
// 示例 6：Pipeline / Retry 语法糖
// =============================================================================
//
// Pipeline：将多个 Auto 串成流水线，等价于多次 Auto 调用但更简洁。
// Retry：Loop + Until + MaxIter + OnExhausted 的组合糖，语义为"重试直到成功"。

func ExampleSugars(factory workplan.AgentFactory) {
	ctx := context.Background()

	// ── Pipeline ──────────────────────────────────────────────────────
	wp := workplan.New(factory, nil, "你是文档处理助手。")
	wp.Pipeline(
		workplan.Step("提取内容", "从以下文档提取关键信息：见 Seele 框架完成重构，核心变更包括 ToolProvider 策略模式 + WorkPlan 图引擎"),
		workplan.Step("结构化", "将以上信息结构化为 JSON：\n{{.PrevResult}}"),
		workplan.Step("生成摘要", "基于结构化数据生成 50 字摘要：\n{{.PrevResult}}"),
	)
	result, _ := wp.Run(ctx)
	fmt.Println("📋 Pipeline:", result.FinalOutputString())

	// ── Retry ─────────────────────────────────────────────────────────
	wp2 := workplan.New(factory, nil, "你是 API 调用助手。")
	wp2.Auto("调用API", "调用外部 API，如果成功在回复中包含「200 OK」，失败包含「ERROR」")

	sig := wp2.Retry("API重试", "调用API", 3,
		workplan.Contains("200 OK"), // 成功条件
		"降级处理",                   // 重试耗尽后跳转
	)
	sig.OnUpdate(func(v string) {
		log.Printf("重试 #%d: %s", sig.Iter(), v)
	})

	wp2.Auto("处理结果", "处理API返回数据：{{.PrevResult}}")
	wp2.Auto("降级处理", "API调用失败，使用缓存数据降级响应")

	result2, _ := wp2.Run(ctx)
	fmt.Println("🔄 Retry:", result2.FinalOutputString())
}

// =============================================================================
// 示例 7：图引擎检查（v0.3 新增）
// =============================================================================
//
// 重构后的 WorkPlan 底层是 Graph + Edge + NodeRunner。
// 虽然糖方法 API 不变，但你可以检查构建出的图结构用于调试。

func ExampleGraphInspection(factory workplan.AgentFactory) {
	wp := workplan.New(factory, nil, "你是助手。")

	// 构建一个带分支的 WorkPlan
	wp.Auto("分析", "分析数据").
		If("判断", workplan.Contains("紧急"), "紧急处理", "常规处理").
		Auto("紧急处理", "立即处理", workplan.WithNext("通知")).
		Auto("常规处理", "排期处理", workplan.WithNext("通知")).
		Auto("通知", "发送通知")

	// 通过 sugar 层无法直接访问 graph，但可以通过反射或测试验证。
	// 以下展示通过 NodeResults 理解图结构：
	fmt.Println("🔍 构建的图包含以下节点：")
	// 注：graph.NodeIDs() 目前未通过 sugar 暴露，生产场景可通过
	// workplan.New().Auto()... 链式调用构建后在测试中 graph.Validate() 验证。
	fmt.Println("   分析 → 判断 → [紧急处理 | 常规处理] → 通知")
	fmt.Println("   Edge: 判断 -(true)→ 紧急处理")
	fmt.Println("   Edge: 判断 -(false)→ 常规处理")
}

// =============================================================================
// main
// =============================================================================

func main() {
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

	factory := &EngineFactory{engine: engine}

	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║   Seele WorkPlan 工作流引擎演示          ║")
	fmt.Println("╚══════════════════════════════════════════╝")

	// 选择要运行的示例（取消注释即可）：
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
