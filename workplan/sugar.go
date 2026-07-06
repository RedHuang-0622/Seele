package workplan

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// =============================================================================
// sugar.go —— 公有语法糖层
// =============================================================================
//
// 设计原则：
//   每个公有方法只做两件事：
//     1. 构造对应 Runner（声明意图）
//     2. 调用 graph.AddNode / graph.AddEdge 注册到图引擎
//   零执行逻辑，执行逻辑全部在 runner.go 的 Run 方法里。
//
// 所有方法返回 *WorkPlan 支持链式调用。

// =============================================================================
// NodeOpt —— 节点级可选配置
// =============================================================================

type NodeOpt func(*node)

func WithPrompt(prompt string) NodeOpt {
	return func(n *node) { n.systemPrompt = prompt }
}
func WithTools(tools ...string) NodeOpt {
	return func(n *node) { n.toolFilter = tools }
}
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

// Auto 添加一个自动执行节点（完整 Agent ReAct 循环 + 工具调用）。
//
// 内部使用 AgentStrategy + strategyRunner，行为与之前完全一致。
// 保留 autoRunner 的后向兼容路径，确保现有代码不受影响。
func (wp *WorkPlan) Auto(id, input string, opts ...NodeOpt) *WorkPlan {
	n := &node{id: wp.resolveID(id, "auto"), kind: kindAuto, input: input}
	applyOpts(n, opts)

	runner := &strategyRunner{
		id: n.id,
		strategy: NewAgentStrategy(wp.factory, n.systemPrompt, n.toolFilter...),
	}
	return wp.registerNode(n, runner)
}

// =============================================================================
// 策略模式糖方法（v0.5 新增）
// =============================================================================

// Method 注册一个 Go 函数节点（纯本地计算，零 LLM 调用）。
//
//	fn 接收渲染后的 input 文本，返回任意结果。
//	适用于数据转换、业务规则校验、条件计算等场景。
//
// 示例：
//
//	wp.Method("计算", func(ctx context.Context, input string) (string, error) {
//	    return fmt.Sprintf(`"结果是: %s"`, strings.ToUpper(input)), nil
//	})
func (wp *WorkPlan) Method(id string, fn func(ctx context.Context, input string) (string, error), opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:       wp.resolveID(id, "method"),
		kind:     kindMethod,
		strategy: NewMethodStrategy(fn),
	}
	applyOpts(n, opts)
	runner := &strategyRunner{id: n.id, strategy: n.strategy}
	return wp.registerNode(n, runner)
}

// LLM 注册一个纯 LLM 节点（无工具调用，无 ReAct 循环）。
//
//	适用于翻译、摘要、分类等不需要工具的纯文本生成任务。
//
// 示例：
//
//	wp.LLM("翻译", "将以下内容翻译为英文：{{.PrevResult}}")
func (wp *WorkPlan) LLM(id, input string, opts ...NodeOpt) *WorkPlan {
	n := &node{id: wp.resolveID(id, "llm"), kind: kindLLM, input: input}
	applyOpts(n, opts)
	runner := &strategyRunner{
		id: n.id,
		strategy: NewLLMStrategy(wp.factory, n.systemPrompt),
	}
	return wp.registerNode(n, runner)
}

// Strategy 注册一个自定义策略节点。
//
//	用户可以自定义实现 NodeStrategy 接口，注入任意执行逻辑。
//
// 示例：
//
//	type MyStrategy struct{}
//	func (s *MyStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) {
//	    return `"custom result"`, nil
//	}
//	wp.Strategy("my-node", &MyStrategy{}, workplan.WithPrompt("custom prompt"))
func (wp *WorkPlan) Strategy(id string, strategy NodeStrategy, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:       wp.resolveID(id, "strategy"),
		kind:     kindStrategy,
		strategy: strategy,
	}
	applyOpts(n, opts)
	runner := &strategyRunner{id: n.id, strategy: n.strategy}
	return wp.registerNode(n, runner)
}

// Approve 添加人工确认节点。
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

	runner := &approveRunner{
		id:             n.id,
		systemPrompt:   n.systemPrompt,
		input:          n.input,
		options:        n.approveOptions,
		kvs:            n.approveKVS,
		timeout:        n.approveTimeout,
		factory:        wp.factory,
		defaultPrompt:  wp.defaultPrompt,

		wp:             wp,
	}
	return wp.registerNode(n, runner)
}

// Gate 极简版 Approve。
func (wp *WorkPlan) Gate(id, input string, opts ...NodeOpt) *WorkPlan {
	return wp.Approve(id, input, Choices("execute", "abort"), opts...)
}

// Choices 快捷构造 ChoiceOption 列表。
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

func DefaultApproveOptions() []ChoiceOption { return Choices("execute", "skip", "abort") }
func DefaultGateOptions() []ChoiceOption    { return Choices("execute", "abort") }
func WithApproveKV(kvs map[string]any) NodeOpt {
	return func(n *node) { n.approveKVS = kvs }
}
func WithApproveTimeout(d time.Duration) NodeOpt {
	return func(n *node) { n.approveTimeout = d }
}

// Checkpoint 添加快照节点。
func (wp *WorkPlan) Checkpoint(id string, opts ...NodeOpt) *WorkPlan {
	n := &node{id: wp.resolveID(id, "ckpt"), kind: kindCheckpoint}
	applyOpts(n, opts)

	runner := &checkpointRunner{id: n.id}
	return wp.registerNode(n, runner)
}

// Emit 把当前输出写入命名变量。
func (wp *WorkPlan) Emit(id, key string, opts ...NodeOpt) *WorkPlan {
	n := &node{id: wp.resolveID(id, "emit"), kind: kindEmit, emitKey: key}
	applyOpts(n, opts)

	runner := &emitRunner{id: n.id, key: n.emitKey}
	return wp.registerNode(n, runner)
}

// =============================================================================
// registerNode —— 统一的节点注册 + 自动连边
// =============================================================================

// registerNode 是 primitiveAddNode 的替代：注册 Runner 到 graph，自动推导线性边。
func (wp *WorkPlan) registerNode(n *node, runner NodeRunner) *WorkPlan {
	nodeID := runner.ID()
	wp.graph.AddNode(runner)

	if wp.entryID == "" {
		wp.entryID = nodeID
	}

	// 自动推导线性边：上一个节点不是控制节点时，自动连边
	if wp.lastNodeID != "" {
		lastNode := wp.nodeIndex[wp.lastNodeID]
		if lastNode != nil &&
			lastNode.kind != kindIf &&
			lastNode.kind != kindSwitch {
			// 检查是否已有显式 next（WithNext option）
			if lastNode.next != "" {
				wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: lastNode.next})
			} else {
				wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: nodeID})
			}
		}
	}

	// 维护索引
	wp.nodeIndex[nodeID] = n
	wp.lastNodeID = nodeID
	return wp
}

// =============================================================================
// 条件分支糖
// =============================================================================

func (wp *WorkPlan) If(id string, cond func(string) bool, trueID, falseID string, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:       wp.resolveID(id, "if"),
		kind:     kindIf,
		ifCond:   cond,
		ifTrueID: trueID,
		ifFalseID: falseID,
	}
	applyOpts(n, opts)

	nodeID := n.id
	runner := &controlRunner{id: nodeID, kind: "if"}
	wp.graph.AddNode(runner)

	if wp.entryID == "" {
		wp.entryID = nodeID
	}
	// 从上一个节点连到 if 节点（除非上一个也是控制节点）
	if wp.lastNodeID != "" {
		lastNode := wp.nodeIndex[wp.lastNodeID]
		if lastNode != nil && lastNode.kind != kindIf && lastNode.kind != kindSwitch {
			wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: nodeID})
		}
	}

	// 两条条件边
	wp.graph.AddEdge(Edge{
		From: nodeID, To: trueID, Priority: 0, Label: "true",
		Condition: func(ec *ExecutionContext) bool { return cond(fromJSON(ec.PrevOutput)) },
	})
	if falseID != "" {
		wp.graph.AddEdge(Edge{
			From: nodeID, To: falseID, Priority: 1, Label: "false",
			Condition: func(ec *ExecutionContext) bool { return !cond(fromJSON(ec.PrevOutput)) },
		})
	}

	wp.nodeIndex[nodeID] = n
	wp.lastNodeID = nodeID
	return wp
}

// Switch 添加多路条件分支节点。
func (wp *WorkPlan) Switch(id string, cases ...SwitchCase) *WorkPlan {
	n := &node{
		id:          wp.resolveID(id, "switch"),
		kind:        kindSwitch,
		switchCases: cases,
	}

	nodeID := n.id
	runner := &controlRunner{id: nodeID, kind: "switch"}
	wp.graph.AddNode(runner)

	if wp.entryID == "" {
		wp.entryID = nodeID
	}
	if wp.lastNodeID != "" {
		lastNode := wp.nodeIndex[wp.lastNodeID]
		if lastNode != nil && lastNode.kind != kindIf && lastNode.kind != kindSwitch {
			wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: nodeID})
		}
	}

	for i, c := range cases {
		c := c // 捕获循环变量（Go 闭包语义：for range 复用同一变量地址）
		wp.graph.AddEdge(Edge{
			From: nodeID, To: c.NextID, Priority: i, Label: "case",
			Condition: func(ec *ExecutionContext) bool {
				if c.Match == nil {
					return true // default 分支
				}
				return c.Match(fromJSON(ec.PrevOutput))
			},
		})
	}

	wp.nodeIndex[nodeID] = n
	wp.lastNodeID = nodeID
	return wp
}

// Case / Default / Contains / NotContains 保持不变
func Case(match func(string) bool, nextID string) SwitchCase {
	return SwitchCase{Match: match, NextID: nextID}
}
func Default(nextID string) SwitchCase {
	return SwitchCase{Match: nil, NextID: nextID}
}
func Contains(substr string) func(string) bool {
	return func(s string) bool { return len(s) > 0 && len(substr) > 0 && strings.Contains(s, substr) }
}
func NotContains(substr string) func(string) bool {
	return func(s string) bool { return !Contains(substr)(s) }
}

// =============================================================================
// Loop 糖
// =============================================================================

type LoopOpt func(*node)
func Until(cond func(result string) bool) LoopOpt { return func(n *node) { n.loopUntil = cond } }
func MaxIter(max int) LoopOpt                     { return func(n *node) { n.loopMaxIter = max } }
func OnExhausted(nodeID string) LoopOpt           { return func(n *node) { n.loopExhaustedID = nodeID } }

func (wp *WorkPlan) Loop(id, bodyID string, opts ...LoopOpt) *Signal {
	sig := newSignal()
	n := &node{
		id:         wp.resolveID(id, "loop"),
		kind:       kindLoop,
		loopBodyID: bodyID,
		loopSignal: sig,
		loopMaxIter: 0,
	}
	for _, o := range opts {
		o(n)
	}

	// 从 nodeIndex 中找循环体的 node，用于获取其配置
	bodyNode := wp.nodeIndex[bodyID]

	nodeID := n.id
	runner := &loopRunner{
		id:            nodeID,
		until:         n.loopUntil,
		maxIter:       n.loopMaxIter,
		signal:        sig,
		bodyPrompt:    ternaryStr(bodyNode != nil, bodyNode.systemPrompt, ""),
		bodyInput:     ternaryStr(bodyNode != nil, bodyNode.input, ""),
		factory:       wp.factory,
		defaultPrompt: wp.defaultPrompt,

	}
	wp.graph.AddNode(runner)

	if wp.entryID == "" {
		wp.entryID = nodeID
	}
	if wp.lastNodeID != "" {
		lastNode := wp.nodeIndex[wp.lastNodeID]
		if lastNode != nil && lastNode.kind != kindIf && lastNode.kind != kindSwitch {
			wp.graph.AddEdge(Edge{From: wp.lastNodeID, To: nodeID})
		}
	}

	// Loop 耗尽后的 exhausted 出口
	if n.loopExhaustedID != "" {
		wp.graph.AddEdge(Edge{
			From: nodeID, To: n.loopExhaustedID, Priority: 1, Label: "exhausted",
			Condition: func(ec *ExecutionContext) bool {
				return sig.Iter() >= n.loopMaxIter
			},
		})
	}
	// Loop 正常结束（直到 until 满足或 no exhausted）走默认 next

	wp.nodeIndex[nodeID] = n
	wp.lastNodeID = nodeID
	return sig
}

// =============================================================================
// Fork 糖
// =============================================================================

func (wp *WorkPlan) Fork(id string, branches []ForkBranch, opts ...NodeOpt) *WorkPlan {
	n := &node{
		id:           wp.resolveID(id, "fork"),
		kind:         kindFork,
		forkBranches: branches,
	}
	applyOpts(n, opts)

	runner := &forkRunner{
		id:            n.id,
		branches:      n.forkBranches,
		factory:       wp.factory,
		defaultPrompt: wp.defaultPrompt,
		maxConcurrent: wp.maxForkConcurrency,
	}
	return wp.registerNode(n, runner)
}

// =============================================================================
// Pipeline / Retry 糖
// =============================================================================

type PipelineStep struct {
	ID    string
	Input string
	Opts  []NodeOpt
}

func Step(id, input string, opts ...NodeOpt) PipelineStep {
	return PipelineStep{ID: id, Input: input, Opts: opts}
}

func (wp *WorkPlan) Pipeline(steps ...PipelineStep) *WorkPlan {
	for _, s := range steps {
		wp.Auto(s.ID, s.Input, s.Opts...)
	}
	return wp
}

func (wp *WorkPlan) Retry(id, bodyID string, maxRetry int, successCond func(string) bool, onExhausted string) *Signal {
	return wp.Loop(id, bodyID, Until(successCond), MaxIter(maxRetry), OnExhausted(onExhausted))
}

// =============================================================================
// 内部工具
// =============================================================================

func (wp *WorkPlan) resolveID(id, prefix string) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("%s_%d", prefix, len(wp.nodes)+1)
}

func ternaryStr(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
