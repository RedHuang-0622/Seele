// =============================================================================
// workplan 包讲解 + 使用示例
// 文件：example_ops_workflow/main.go
// 场景：智能运维 Agent —— 告警接入 → 日志分析 → 人工确认 → 修复执行
// =============================================================================

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	seeleapi "github.com/sukasukasuka123/Seele/sdk/api"
	"github.com/sukasukasuka123/Seele/workplan"
)

// ========== 把 Engine 适配为 AgentFactory ==========
//
// WorkPlan 只依赖 AgentFactory 接口，不直接导入 Engine。
// 这里做一个薄包装，让 Engine 满足接口。
//
// 【为什么要这层适配？】
// plan.go 里定义了：
//
//   type AgentFactory interface {
//       NewAgent(systemPrompt string) Agent
//   }
//
// Engine.NewAgent 返回的是 *Seele.Agent（具体类型），
// 而 workplan.Agent 是接口（只有 Chat 方法）。
// Go 的接口是结构型的，*Seele.Agent 天然满足 workplan.Agent，
// 但 Engine 本身不满足 AgentFactory（返回值类型不同）。
// 用 EngineFactory 包一层解决这个问题。

type EngineFactory struct {
	engine *seeleapi.Engine
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	// Seele.Agent 实现了 Chat(ctx, input) (string, error)，满足 workplan.Agent
	return f.engine.NewSession(systemPrompt, 10)
}

// =============================================================================
// 示例一：最简单的线性流程
// Auto → Checkpoint → Auto
// 适合理解基本用法
// =============================================================================

func ExampleLinear(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(
		factory,
		nil, // gate=nil 自动使用 CLIApprovalGate
		"你是一个运维助手，擅长分析系统问题。",
	)

	// 三步线性流程，Auto 节点之间 next 自动推导，不需要手动指定
	wp.Auto("获取日志", "查询最近1小时的错误日志").
		Checkpoint("日志快照"). // 保存这一步的结果，出错可以从这里回溯
		Auto("生成报告", "根据以下日志生成问题摘要报告：\n{{.PrevResult}}")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("报告：", result.FinalOutputString())
	// result.Checkpoints["日志快照"] 可以拿到日志快照
}

// =============================================================================
// 示例二：If 分支
// 根据日志分析结果决定走告警还是正常流程
//
// 【If 节点说明】
// If 节点本身不执行任何 Agent，只做路由。
// 它读取上一节点的纯文本输出，传给 cond 函数，
// 根据返回值跳转 trueID 或 falseID。
// =============================================================================

func ExampleIf(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是运维助手。")

	wp.Auto("分析日志", "分析系统日志，如果有严重错误，在回复中包含「严重错误」字样。").

		// If 节点：条件为真跳"紧急处理"，为假跳"常规记录"
		// 注意：trueID / falseID 必须是后面会注册的节点 ID
		If("判断严重性",
			workplan.Contains("严重错误"), // 快捷条件函数，等价于 func(s string) bool { return strings.Contains(s,"严重错误") }
			"紧急处理",                    // trueID
			"常规记录",                    // falseID
		).

		// trueID 分支：紧急处理
		Auto("紧急处理", "立即生成紧急告警并通知值班人员：{{.PrevResult}}",
			workplan.WithNext("结束"), // 执行完跳到"结束"，跳过"常规记录"
		).

		// falseID 分支：常规记录
		Auto("常规记录", "将以下日志归档到周报：{{.PrevResult}}").

		// 两个分支最终汇合到"结束"
		Auto("结束", "发送处理完成通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.FinalOutputString())
}

// =============================================================================
// 示例三：Switch 多路分支
// 比 If 更适合"多个互斥条件"的场景
//
// 【Switch 节点说明】
// cases 按顺序匹配，第一个 Match 返回 true 的分支执行。
// Default(...) 相当于 switch 的 default:，放在最后。
// Switch 节点本身也不执行 Agent，纯路由。
// =============================================================================

func ExampleSwitch(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是运维助手。")

	wp.Auto("检测状态",
		"检测服务状态，回复中必须包含以下之一：「正常」「警告」「故障」「超时」").
		Switch("状态路由",
			workplan.Case(workplan.Contains("故障"), "故障处理"),
			workplan.Case(workplan.Contains("超时"), "超时重试"),
			workplan.Case(workplan.Contains("警告"), "警告记录"),
			workplan.Default("正常记录"), // 没有匹配到上面任何一个时走这里
		).
		Auto("故障处理", "触发故障应急预案",
			workplan.WithNext("通知")).
		Auto("超时重试", "重试失败的请求",
			workplan.WithNext("通知")).
		Auto("警告记录", "记录警告到监控系统",
			workplan.WithNext("通知")).
		Auto("正常记录", "记录正常状态",
			workplan.WithNext("通知")).
		Auto("通知", "发送处理结果通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.FinalOutputString())
}

// =============================================================================
// 示例四：Loop + Signal（核心特性）
// 修复脚本反复执行，直到服务恢复正常，或超过3次退出
//
// 【Loop 节点说明】
// Loop 是这个框架里最特殊的节点，因为它返回一个 Signal。
//
// Signal 是"活引用"，不是"死值"：
//   - Loop 每次迭代结束后调用 signal.set(result)
//   - 如果你注册了 OnUpdate 回调，每次迭代完成就立刻触发
//   - Wait() 阻塞直到整个 Loop 结束
//
// 为什么需要 Signal 而不是等 Loop 结束再拿结果？
// 场景：Loop 在做"逐步修复"，每次迭代修复一个问题。
// 下游的告警系统不需要等全部修复完才发通知，
// 每次迭代修复了一个就立刻发一条通知——这就是 Signal 的意义。
//
// 【Loop 的执行流程】
//   iter=0: input = 上一节点输出（initJSON）
//   iter=1: input = iter=0 的输出（current）
//   iter=2: input = iter=1 的输出
//   ...
// 每次迭代的输出成为下次的输入，通过 {{.PrevResult}} 传递。
// =============================================================================

func ExampleLoop(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是运维修复助手。")

	// 先注册循环体节点（Loop 引用它的 ID，所以要先存在）
	wp.Auto("修复执行体",
		// 每次迭代拿上次结果继续修复
		"根据上次修复结果继续处理，直到服务完全恢复：\n{{.PrevResult}}",
		workplan.WithTools("run_script", "check_service"), // 只允许这两个工具
	)

	// 注册 Loop 节点，引用上面的循环体
	// Loop 返回 Signal，在 Run() 之前注册回调
	sig := wp.Loop("修复循环", "修复执行体",
		workplan.Until(workplan.Contains("恢复正常")), // 退出条件
		workplan.MaxIter(3),          // 最多3次
		workplan.OnExhausted("人工介入"), // 超出次数跳这里
	)

	// 【关键】在 Run 之前注册 OnUpdate 回调
	// 每次迭代完成，不等 Loop 结束，立刻触发
	sig.OnUpdate(func(jsonVal string) {
		// jsonVal 是 JSON 字符串，fromJSON 去掉引号拿纯文本
		log.Printf("[实时进度] 第 %d 次迭代完成: %s", sig.Iter(), jsonVal)
		// 这里可以接入 WebSocket 推送给前端
	})

	wp.Auto("人工介入", "生成人工介入告警，修复循环已耗尽",
		workplan.WithNext(""), // 结束
	)

	wp.Auto("完成通知", "发送修复完成通知")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Run 结束后 Signal 也已经 close，可以安全 Get
	fmt.Println("最终修复结果：", sig.GetString())
	fmt.Println("共迭代次数：", sig.Iter())
	fmt.Println("WorkPlan 输出：", result.FinalOutputString())
}

// =============================================================================
// 示例五：Fork 并发（Multi-Agent）
// 三个角色同时工作，结果汇合后人工审查
//
// 【Fork 节点说明】
// Fork 启动多个独立 Agent，每个有自己的历史和推理循环。
// 所有分支完成后，结果汇合为一个 JSON object 传给下一节点：
//   {
//     "前端工程师": "实现了登录页面...",
//     "后端工程师": "实现了登录接口...",
//     "测试工程师": "编写了12个测试用例..."
//   }
//
// 下一个节点（通常是 Auto）收到这个 JSON object 作为输入，
// LLM 可以直接读取并综合分析。
//
// 【和 Auto 里并发 tool_call 的区别】
// Auto 内部：同一个 Agent，同一轮推理，同时发出多个 tool_call
//            → 工具级并发，结果直接进同一个 history
// Fork：     多个独立 Agent，各自完整推理
//            → Agent 级并发，history 互不干扰，最后 Join 汇合
// =============================================================================

func ExampleFork(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是软件团队协调助手。")

	wp.
		// Step 1：需求分析
		Auto("需求分析", "分析需求并拆解任务：用户登录功能，包含前端页面、后端接口、测试用例").

		// Step 2：把需求结果保存到命名变量，后续 Fork 里每个分支都能引用
		Emit("保存需求", "requirement").

		// Step 3：三个角色并行工作
		// 每个 ForkBranch 有独立的 SystemPrompt 和 Input
		// Input 里可以用 {{.Vars.requirement}} 引用上面 Emit 保存的内容
		Fork("并行开发", []workplan.ForkBranch{
			{
				Label:        "前端工程师",
				SystemPrompt: "你是资深前端工程师，专注于 React 和用户体验。",
				Input:        "实现登录页面，需求如下：{{.Vars.requirement}}",
			},
			{
				Label:        "后端工程师",
				SystemPrompt: "你是资深后端工程师，专注于 Go 和高并发。",
				Input:        "实现登录接口，需求如下：{{.Vars.requirement}}",
			},
			{
				Label:        "测试工程师",
				SystemPrompt: "你是资深测试工程师，专注于自动化测试。",
				Input:        "编写登录功能测试用例，需求如下：{{.Vars.requirement}}",
			},
		}).

		// Step 4：人工审查（Approve 节点）
		// 上一步 Fork 的输出是 JSON object，Approve 会先让 Agent 生成审查计划，
		// 然后展示给人，人确认后才真正执行
		Approve("代码审查",
			"审查以下并行开发产出，评估是否可以进入集成阶段：\n{{.PrevResult}}",
			[]workplan.ChoiceOption{
				{Key: "approve", Label: "通过，进入集成", Style: "primary"},
				{Key: "revise", Label: "需要修改", Style: "warning"},
				{Key: "abort", Label: "终止", Style: "danger"},
			},
		).

		// Step 5：根据审查结果分支
		Switch("审查结果",
			workplan.Case(workplan.Contains("通过"), "集成测试"),
			workplan.Case(workplan.Contains("修改"), "修改通知"),
			workplan.Default("终止流程"),
		).
		Auto("集成测试", "执行集成测试并生成报告").
		Auto("修改通知", "通知各角色根据审查意见修改").
		Auto("终止流程", "记录终止原因并通知相关人员")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// 打印每个节点的执行情况
	for _, nr := range result.NodeResults {
		status := "Y"
		if nr.Skipped {
			status = "S"
		}
		if nr.Aborted {
			status = "N"
		}
		fmt.Printf("%s [%s] %s (%.1fs)\n",
			status, nr.Kind, nr.NodeID,
			nr.EndedAt.Sub(nr.StartedAt).Seconds(),
		)
	}

	// 读取 Emit 保存的需求变量
	fmt.Println("\n需求分析结果：", result.Vars["requirement"])
}

// =============================================================================
// 示例六：完整运维场景
// 组合所有特性：Pipeline + Loop + Approve + Switch + Fork + Signal
// =============================================================================

func ExampleFullOpsWorkflow(factory workplan.AgentFactory) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	wp := workplan.New(
		factory,
		nil, // 命令行确认，生产环境换成 WebApprovalGate
		"你是智能运维系统，负责处理线上告警和执行修复操作。",
	)

	// ── 阶段一：信息收集（Pipeline 串联三步）────────────────────
	wp.Pipeline(
		workplan.Step("接收告警", "解析告警：CPU使用率持续超过95%，持续时间15分钟"),
		workplan.Step("收集指标", "查询最近30分钟的系统指标：CPU、内存、IO、网络\n告警上下文：{{.PrevResult}}"),
		workplan.Step("分析根因", "根据以下指标分析根本原因，给出可能的原因列表：\n{{.PrevResult}}"),
	)

	// ── 保存根因分析结果供后续节点复用 ──────────────────────────
	wp.Emit("保存根因", "root_cause")

	// ── 阶段二：判断严重程度 ─────────────────────────────────────
	wp.If("严重程度判断",
		func(s string) bool {
			return strings.Contains(s, "P0") || strings.Contains(s, "严重")
		},
		"紧急响应", // P0/严重 → 立即拉起 Fork 并行处置
		"标准响应", // 其他 → 标准单线程处置
	)

	// ── 紧急响应：多 Agent 并行处置 ─────────────────────────────
	wp.Fork("紧急响应", []workplan.ForkBranch{
		{
			Label:        "修复执行",
			SystemPrompt: "你是运维执行专家，只执行被明确批准的操作。",
			Input:        "根据根因分析执行紧急修复：{{.Vars.root_cause}}",
		},
		{
			Label:        "影响评估",
			SystemPrompt: "你是业务影响评估专家。",
			Input:        "评估当前故障的业务影响范围：{{.Vars.root_cause}}",
		},
		{
			Label:        "通知准备",
			SystemPrompt: "你是运维通知专家。",
			Input:        "准备故障通知文案（面向业务方）：{{.Vars.root_cause}}",
		},
	}, workplan.WithNext("汇总报告"))

	// ── 标准响应：循环修复 ───────────────────────────────────────
	wp.Auto("标准响应循环体",
		"执行一步修复操作，基于上次结果继续：\n{{.PrevResult}}",
		workplan.WithTools("run_script", "check_service", "restart_service"),
	)

	sig := wp.Loop("标准响应", "标准响应循环体",
		workplan.Until(workplan.Contains("已恢复")),
		workplan.MaxIter(5),
		workplan.OnExhausted("升级处置"),
	)
	// 每次迭代实时推送进度（生产环境接 WebSocket）
	sig.OnUpdate(func(jsonVal string) {
		log.Printf("[修复进度] iter=%d result=%s", sig.Iter(), jsonVal)
	})

	wp.Auto("升级处置",
		"标准修复循环耗尽，生成升级报告并通知 oncall",
		workplan.WithNext("汇总报告"),
	)

	// ── 阶段三：汇总 + 人工确认 + 部署 ─────────────────────────
	wp.
		Auto("汇总报告", "汇总所有处置结果，生成事后报告：\n{{.PrevResult}}").

		// Checkpoint：汇总后保存快照，如果后续操作失败可以从这里回溯
		Checkpoint("报告快照").

		// Gate：极简版 Approve，只有执行/终止，用于高危操作
		Gate("确认部署",
			"根据报告执行修复后的配置变更和服务重启",
			workplan.WithTools("deploy_config", "restart_service"),
			workplan.WithPrompt("你是部署专家，只执行被明确授权的部署操作，不做任何额外变更。"),
		).
		Auto("完成归档", "将事件归档到知识库，更新 Runbook")

	// ── 执行 ─────────────────────────────────────────────────────
	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatalf("WorkPlan 执行失败: %v", err)
	}

	// ── 结果处理 ──────────────────────────────────────────────────
	fmt.Printf("\n========== 执行摘要 ==========\n")
	fmt.Printf("总耗时: %v\n", result.TotalElapsed)
	if result.Aborted {
		fmt.Printf("⚠ 流程中止: %s\n", result.AbortReason)
	}

	fmt.Printf("\n节点执行记录:\n")
	for _, nr := range result.NodeResults {
		icon := map[bool]string{true: "⏭", false: "✓"}[nr.Skipped]
		if nr.Aborted {
			icon = "✗"
		}
		if nr.Err != nil {
			icon = "💥"
		}
		fmt.Printf("  %s %-12s %-20s %.2fs\n",
			icon, nr.Kind, nr.NodeID,
			nr.EndedAt.Sub(nr.StartedAt).Seconds(),
		)
	}

	if snap, ok := result.Checkpoints["报告快照"]; ok {
		fmt.Printf("\n事后报告快照:\n%s\n", snap)
	}

	fmt.Printf("\n最终输出: %s\n", result.FinalOutputString())
}

// =============================================================================
// 示例七：Retry 语法糖
// Retry 是 Loop 的语义包装，专门用于"失败重试"场景
// =============================================================================

func ExampleRetry(factory workplan.AgentFactory) {
	ctx := context.Background()

	wp := workplan.New(factory, nil, "你是 API 调用助手。")

	// 注册重试的循环体
	wp.Auto("调用外部API",
		"调用外部 API 获取数据，如果成功在回复中包含「200 OK」，失败包含「ERROR」",
	)

	// Retry 是 Loop + Until + MaxIter + OnExhausted 的组合糖
	// 最多重试3次，成功条件是回复包含"200 OK"，失败跳"降级处理"
	sig := wp.Retry("API重试", "调用外部API", 3,
		workplan.Contains("200 OK"),
		"降级处理",
	)

	sig.OnUpdate(func(v string) {
		log.Println("重试进度:", sig.Iter(), v)
	})

	wp.Auto("处理结果", "处理API返回数据：{{.PrevResult}}")
	wp.Auto("降级处理", "API调用失败，使用缓存数据降级响应")

	result, _ := wp.Run(ctx)
	fmt.Println(result.FinalOutputString())
}

// =============================================================================
// main：接入真实 Engine 的完整示例
// =============================================================================

func main() {
	engine, err := seeleapi.New(seeleapi.Options{
		RegistryPath:  "config/registry.yaml",
		LLMConfigPath: "config/config.yaml",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer engine.Shutdown()

	factory := &EngineFactory{engine: engine}

	// 选择要运行的示例
	ExampleFullOpsWorkflow(factory)
}
