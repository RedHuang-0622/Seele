package workplan

import (
	"strings"
	"time"
)

// =============================================================================
// sugar.go —— 公有语法糖层
// =============================================================================
//
// 设计原则：
//   每个公有方法只做两件事：
//     1. 构造 node 结构体（声明意图）
//     2. 调用 primitiveAddNode 注册节点
//   零执行逻辑，执行逻辑全部在 plan.go 的 primitive 方法里。
//
// 所有方法返回 *WorkPlan 支持链式调用。
//
// 用法示例见文件末尾 Example 注释。

// =============================================================================
// NodeOpt —— 节点级可选配置
// =============================================================================

// NodeOpt 是节点配置的函数选项，由 WithXxx 系列函数产生。
type NodeOpt func(*node)

// WithPrompt 覆盖本节点的系统提示词。
func WithPrompt(prompt string) NodeOpt {
	return func(n *node) { n.systemPrompt = prompt }
}

// WithTools 限制本节点只能调用指定工具（白名单）。
func WithTools(tools ...string) NodeOpt {
	return func(n *node) { n.toolFilter = tools }
}

// WithNext 显式指定下一个节点 ID，覆盖自动推导的顺序链。
// 常用于把两条分支重新汇合到同一个节点。
func WithNext(id string) NodeOpt {
	return func(n *node) { n.next = id }
}

func applyOpts(n *node, opts []NodeOpt) {
	for _, o := range opts {
		o(n)
	}
}

// =============================================================================
// 基础节点糖
// =============================================================================

// Auto 添加一个自动执行节点。
// Agent 完整跑一次 ReAct 循环（可能多轮 tool_call），自主决策直到任务完成。
//
//	id    节点 ID，留空自动生成
//	input 任务描述，支持 {{.PrevResult}} 和 {{.Vars.key}}
func (wp *WorkPlan) Auto(id, input string, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:    wp.resolveID(id, "auto"),
		kind:  kindAuto,
		input: input,
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// [workplangate] Approve 添加人工确认节点（Q-K-V 模型）。
// Agent 先生成执行计划，构建 Question 发送给用户，用户选择 K 后匹配 V 并执行。
//
//	options 展示给用户的选项列表（[]ChoiceOption），留空默认使用 DefaultApproveOptions()
func (wp *WorkPlan) Approve(id, input string, options []ChoiceOption, opts ...NodeOpt) *WorkPlan {
	if len(options) == 0 {
		options = DefaultApproveOptions()
	}
	n := &node{
		id:             wp.resolveID(id, "approve"),
		kind:           kindApprove,
		input:          input,
		approveOptions: options,
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// [workplangate] Gate 极简版 Approve，只展示 执行/终止 两个选项。
func (wp *WorkPlan) Gate(id, input string, opts ...NodeOpt) *WorkPlan {
	return wp.Approve(id, input, Choices("execute", "abort"), opts...)
}

// ── [workplangate] 选项构造工具 ──────────────────────────────────

// DefaultApproveOptions 返回默认三选项：执行 / 跳过 / 终止。
func DefaultApproveOptions() []ChoiceOption {
	return Choices("execute", "skip", "abort")
}

// DefaultGateOptions 返回极简二选项：执行 / 终止。
func DefaultGateOptions() []ChoiceOption {
	return Choices("execute", "abort")
}

// Choices 快捷构造 ChoiceOption 列表，按 key 自动匹配内置 label/description/style。
// 支持的内置 key: "execute" | "skip" | "abort" | "retry" | "confirm"
// 未识别的 key 使用 key 本身作为 label，style 为 "default"。
func Choices(keys ...string) []ChoiceOption {
	builtin := map[string]ChoiceOption{
		"execute": {Key: "execute", Label: "执行", Description: "按计划执行全部步骤", Style: "primary"},
		"skip":    {Key: "skip", Label: "跳过", Description: "跳过当前节点，继续后续流程", Style: "secondary"},
		"abort":   {Key: "abort", Label: "终止", Description: "终止整个工作流", Style: "danger"},
		"retry":   {Key: "retry", Label: "重试", Description: "重新执行当前节点", Style: "warning"},
		"confirm": {Key: "confirm", Label: "确认", Description: "确认并继续执行", Style: "primary"},
	}
	result := make([]ChoiceOption, len(keys))
	for i, k := range keys {
		if opt, ok := builtin[k]; ok {
			result[i] = opt
		} else {
			result[i] = ChoiceOption{Key: k, Label: k, Description: "", Style: "default"}
		}
	}
	return result
}

// WithApproveKV 为 Approve 节点设置自定义 K→V 映射表。
// 不调用时默认每个 K 的值等于其 key。
//
// 用法：
//
//	wp.Approve("部署", "确认部署方式", Choices("deploy", "canary", "abort"),
//	    WithApproveKV(map[string]any{
//	        "deploy":  "execute",
//	        "canary":  "partial",
//	        "abort":   "abort",
//	    }),
//	)
func WithApproveKV(kvs map[string]any) NodeOpt {
	return func(n *node) { n.approveKVS = kvs }
}

// WithApproveTimeout 为 Approve 节点设置审批超时时间。
func WithApproveTimeout(d time.Duration) NodeOpt {
	return func(n *node) { n.approveTimeout = d }
}

// Checkpoint 添加快照节点。
// 执行到此节点时自动保存当前输出，WorkPlanResult.Checkpoints[id] 可读取。
// 后续节点失败时可在业务层手动回滚到此快照（框架不自动回滚，由调用方决策）。
func (wp *WorkPlan) Checkpoint(id string, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:         wp.resolveID(id, "ckpt"),
		kind:       kindCheckpoint,
		checkpoint: &checkpointState{},
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// Emit 把当前输出写入命名变量，后续节点可通过 {{.Vars.key}} 引用。
// 适合"某个节点的结果需要在多个后续节点里复用"的场景。
//
//	key 变量名，全局唯一，重复写入会覆盖
func (wp *WorkPlan) Emit(id, key string, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:      wp.resolveID(id, "emit"),
		kind:    kindEmit,
		emitKey: key,
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// =============================================================================
// 条件分支糖
// =============================================================================

// If 添加二选一条件分支节点。
// 根据上一节点输出决定跳转到 trueID 还是 falseID。
//
// If 节点本身不执行 Agent，只做路由。
// trueID / falseID 必须是已经或即将注册的节点 ID。
//
//	cond      接收上一节点的纯文本输出，返回 true/false
//	trueID    条件为真时跳转的节点 ID
//	falseID   条件为假时跳转的节点 ID（空字符串表示结束）
func (wp *WorkPlan) If(id string, cond func(string) bool, trueID, falseID string, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:        wp.resolveID(id, "if"),
		kind:      kindIf,
		ifCond:    cond,
		ifTrueID:  trueID,
		ifFalseID: falseID,
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// Switch 添加多路条件分支节点。
// 按 cases 顺序匹配，命中第一个 Match 返回 true 的 case。
// 未命中任何 case 时，走 Default（如果有），否则结束。
//
// Switch 节点本身不执行 Agent，只做路由。
//
// 用法：
//
//	.Switch("route",
//	    Case(func(s string) bool { return strings.Contains(s, "成功") }, "notify_ok"),
//	    Case(func(s string) bool { return strings.Contains(s, "超时") }, "retry"),
//	    Default("escalate"),
//	)
func (wp *WorkPlan) Switch(id string, cases ...SwitchCase) *WorkPlan {
	n := &node{
		id:          wp.resolveID(id, "switch"),
		kind:        kindSwitch,
		switchCases: cases,
	}
	return wp.primitiveAddNode(n)
}

// Case 构造一个 Switch 的匹配分支。
func Case(match func(string) bool, nextID string) SwitchCase {
	return SwitchCase{Match: match, NextID: nextID}
}

// Default 构造 Switch 的默认分支（相当于 default:）。
func Default(nextID string) SwitchCase {
	return SwitchCase{Match: nil, NextID: nextID}
}

// Contains 快捷条件：输出包含指定子串时命中。
// 配合 Case / If 使用，避免写 lambda。
//
//	Case(Contains("成功"), "notify_ok")
func Contains(substr string) func(string) bool {
	return func(s string) bool {
		return len(s) > 0 && len(substr) > 0 &&
			strings.Contains(s, substr)
	}
}

// NotContains 快捷条件：输出不包含指定子串时命中。
func NotContains(substr string) func(string) bool {
	return func(s string) bool { return !Contains(substr)(s) }
}

// =============================================================================
// Loop 糖
// =============================================================================

// LoopOpt 是 Loop 节点的可选配置。
type LoopOpt func(*node)

// Until 设置退出条件：当循环体输出满足 cond 时退出。
func Until(cond func(result string) bool) LoopOpt {
	return func(n *node) { n.loopUntil = cond }
}

// MaxIter 设置最大迭代次数，超出后退出循环。
// 0 表示不限制（需要配合 Until 使用，否则死循环）。
func MaxIter(max int) LoopOpt {
	return func(n *node) { n.loopMaxIter = max }
}

// OnExhausted 设置超出 MaxIter 后跳转的节点 ID。
// 不设置时走 Loop 节点的默认 next。
func OnExhausted(nodeID string) LoopOpt {
	return func(n *node) { n.loopExhaustedID = nodeID }
}

// Loop 添加循环节点，返回关联的 Signal（活引用）。
//
// bodyID  循环体节点 ID（必须已注册）。
//
//	循环体每次迭代的输入 = 上次迭代的输出（通过 {{.PrevResult}} 传递）。
//	第一次迭代的输入 = Loop 节点接收到的上一节点输出。
//
// 返回值 *Signal 可在 Loop 注册之后、Run 之前调用 OnUpdate / Wait，
// 实现"实时响应每次迭代结果"的 Reactive 效果。
//
// 用法：
//
//	sig := wp.Loop("retry", "body_node",
//	    Until(Contains("成功")),
//	    MaxIter(5),
//	    OnExhausted("fail_handler"),
//	)
//	sig.OnUpdate(func(jsonVal string) {
//	    log.Println("本次迭代结果:", jsonVal)
//	})
func (wp *WorkPlan) Loop(id, bodyID string, opts ...LoopOpt) *Signal {
	sig := newSignal()
	n := &node{
		id:          wp.resolveID(id, "loop"),
		kind:        kindLoop,
		loopBodyID:  bodyID,
		loopSignal:  sig,
		loopMaxIter: 0, // 默认不限
	}
	for _, o := range opts {
		o(n)
	}
	wp.primitiveAddNode(n)
	return sig
}

// =============================================================================
// Fork / Join 糖
// =============================================================================

// Fork 添加并发分叉节点，同时启动多个独立 Agent。
// 每个 ForkBranch 运行一个完整的 ReAct 循环，互相独立。
// 所有分支完成后，结果以 JSON object 形式汇合传给下一节点。
//
// 和 Auto 里的并发 tool_call 的区别：
//   - tool_call 并发：同一个 Agent 同一轮推理，工具级并发
//   - Fork 并发：多个独立 Agent，各自完整推理，Agent 级并发
//
// 用法（软件工程场景）：
//
//	.Fork("开发阶段", []ForkBranch{
//	    {Label: "前端", SystemPrompt: "你是前端工程师", Input: "实现登录页面"},
//	    {Label: "后端", SystemPrompt: "你是后端工程师", Input: "实现登录接口"},
//	    {Label: "测试", SystemPrompt: "你是测试工程师", Input: "编写登录测试用例"},
//	})
func (wp *WorkPlan) Fork(id string, branches []ForkBranch, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:           wp.resolveID(id, "fork"),
		kind:         kindFork,
		forkBranches: branches,
	}
	applyOpts(n, opts)
	return wp.primitiveAddNode(n)
}

// =============================================================================
// Pipeline 糖 —— 多个 Auto 节点的快捷串联
// =============================================================================

// PipelineStep 是 Pipeline 中的一个步骤。
type PipelineStep struct {
	ID    string
	Input string
	Opts  []NodeOpt
}

// Step 构造 PipelineStep，配合 Pipeline 使用。
func Step(id, input string, opts ...NodeOpt) PipelineStep {
	return PipelineStep{ID: id, Input: input, Opts: opts}
}

// Pipeline 把多个 Auto 节点一行串起来。
// 等价于依次调用多个 Auto()，每个节点的输出自动成为下一个的输入。
//
// 用法：
//
//	.Pipeline(
//	    Step("fetch",   "获取日志"),
//	    Step("analyze", "分析日志：{{.PrevResult}}"),
//	    Step("report",  "根据分析结果生成报告：{{.PrevResult}}"),
//	)
func (wp *WorkPlan) Pipeline(steps ...PipelineStep) *WorkPlan {
	for _, s := range steps {
		wp.Auto(s.ID, s.Input, s.Opts...)
	}
	return wp
}

// =============================================================================
// Retry 糖 —— 失败自动重试的 Loop 包装
// =============================================================================

// Retry 是 Loop 的语义包装：失败时最多重试 n 次。
// 底层是 Loop + Until(success) + MaxIter(n)。
//
// successCond 判断"成功"的条件函数，返回 true 表示成功，退出重试。
// onExhausted 重试耗尽后跳转的节点 ID，空表示结束。
//
// 用法：
//
//	sig := wp.Retry("call_api", "call_api_body", 3,
//	    Contains("200 OK"),
//	    "fallback_handler",
//	)
func (wp *WorkPlan) Retry(id, bodyID string, maxRetry int, successCond func(string) bool, onExhausted string) *Signal {
	return wp.Loop(id, bodyID,
		Until(successCond),
		MaxIter(maxRetry),
		OnExhausted(onExhausted),
	)
}

// =============================================================================
// 内部工具
// =============================================================================

func (wp *WorkPlan) resolveID(id, prefix string) string {
	if id != "" {
		return id
	}
	return wp.primitiveAutoID(prefix)
}

// =============================================================================
// 完整用法示例（注释，不编译）
// =============================================================================

/*
func ExampleDevWorkflow(factory AgentFactory) {
	ctx := context.Background()

	wp := New(factory, nil, "你是一个软件工程团队协调助手。")

	// 1. 需求分析（单 Agent，自主执行）
	wp.Auto("需求分析", "分析以下需求并拆解任务：用户登录功能")

	// 2. 保存需求分析结果，后续节点复用
	wp.Emit("emit_req", "requirement")

	// 3. 三个角色并行开发（Fork）
	wp.Fork("并行开发", []ForkBranch{
		{
			Label:        "前端",
			SystemPrompt: "你是资深前端工程师",
			Input:        "根据需求实现登录页面：{{.Vars.requirement}}",
		},
		{
			Label:        "后端",
			SystemPrompt: "你是资深后端工程师",
			Input:        "根据需求实现登录接口：{{.Vars.requirement}}",
		},
		{
			Label:        "测试",
			SystemPrompt: "你是资深测试工程师",
			Input:        "根据需求编写测试用例：{{.Vars.requirement}}",
		},
	})

	// 4. 人工审查并发结果
	wp.Approve("代码审查",
		"请审查以下并行开发结果，确认是否可以合并：\n{{.PrevResult}}",
		[]string{"通过合并", "需要修改", "终止"},
	)

	// 5. 条件：审查通过→部署，需要修改→重新执行
	wp.Switch("审查结果",
		Case(Contains("通过"), "部署"),
		Case(Contains("修改"), "修改循环"),
		Default("结束"),
	)

	// 6. 修改循环（最多3轮，每轮结果实时打印）
	sig := wp.Loop("修改循环", "修改执行",
		Until(Contains("修改完成")),
		MaxIter(3),
		OnExhausted("人工介入"),
	)
	sig.OnUpdate(func(jsonVal string) {
		log.Println("[修改进度]", jsonVal)
	})

	// 修改执行体
	wp.Auto("修改执行", "根据审查意见修改代码：{{.PrevResult}}")

	// 7. 部署（需要人工确认，只允许调用 deploy 工具）
	wp.Gate("部署", "执行生产环境部署", WithTools("deploy_script"))

	// 8. 快照
	wp.Checkpoint("部署完成")

	// 9. 通知
	wp.Auto("结束", "发送部署完成通知")
	wp.Auto("人工介入", "发送告警：修改循环已耗尽，需要人工介入")

	result, err := wp.Run(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("完成，最终输出: %s", result.FinalOutputString())
	log.Printf("需求分析结果: %s", fromJSON(result.Vars["requirement"]))
}
*/
