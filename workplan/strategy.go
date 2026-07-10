package workplan

import "context"

// =============================================================================
// NodeStrategy —— 图节点的执行策略接口
// =============================================================================
//
// 设计意图：
//   NodeStrategy 将"节点执行什么"从固定的 Runner 实现中抽离，
//   让节点通过组合不同的策略获得不同的执行行为：
//     - MethodStrategy  —— 执行 Go 函数（纯本地计算）
//     - LLMStrategy     —— 只调用 LLM，无工具
//     - AgentStrategy   —— 完整 ReAct 循环 + 工具调用
//
// 用户自定义策略：
//
//	type MyStrategy struct{}
//	func (s *MyStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
//	    return `"custom result"`, nil
//	}
//	wp := workplan.New(factory, nil, "prompt")
//	wp.Strategy("my-node", &MyStrategy{})
//
// 注意：
//   策略实现不感知图拓扑，只关心"输入 → 输出"的转换。
//   流程控制（If/Switch/Loop/Fork/Approve 等）不是执行策略，
//   它们在 runner.go 中以独立的 Runner 实现保留。

// NodeStrategy 是图节点的执行策略接口。
type NodeStrategy interface {
	// Execute 执行策略逻辑。
	//   ctx: 上下文（取消/超时）
	//   ec:  图执行上下文（含 PrevOutput、Vars 等运行时状态）
	// 返回: JSON 字符串
	Execute(ctx context.Context, ec *ExecutionContext) (string, error)
}

// DeprecatedNodeStrategy 是旧版 NodeStrategy 的桥接接口。
// 用于兼容外部还在使用旧签名 `Execute(ctx, input, ec)` 的自定义策略。
// 适配器见 AdapterDeprecatedStrategy。
type DeprecatedNodeStrategy interface {
	Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}

// =============================================================================
// MethodStrategy —— Go 函数节点
// =============================================================================

// MethodStrategy 执行一个纯 Go 函数，不涉及任何 LLM 调用。
// 适用于本地计算、数据转换、业务规则校验等场景。
type MethodStrategy struct {
	fn func(ctx context.Context, input string) (string, error)
}

// NewMethodStrategy 创建 MethodStrategy。
//   fn: 接收上下文和渲染后的输入文本，返回任意结果
func NewMethodStrategy(fn func(ctx context.Context, input string) (string, error)) *MethodStrategy {
	return &MethodStrategy{fn: fn}
}

func (s *MethodStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
	// ec.PrevOutput 已经是渲染好的文本（模板渲染在 strategyRunner 完成）
	out, err := s.fn(ctx, ec.PrevOutput)
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}

// =============================================================================
// LLMStrategy —— 纯 LLM 节点（无工具）
// =============================================================================

// LLMStrategy 只调用 LLM，不挂载任何工具，没有 ReAct 循环。
// 适用于翻译、摘要、分类等不需要外部工具的纯文本生成任务。
type LLMStrategy struct {
	systemPrompt string
	factory      AgentFactory
	onChunk      func(string) // 流式输出回调，nil = 不流式
}

// NewLLMStrategy 创建 LLMStrategy。
func NewLLMStrategy(factory AgentFactory, systemPrompt string) *LLMStrategy {
	return &LLMStrategy{factory: factory, systemPrompt: systemPrompt}
}

func (s *LLMStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
	prompt := s.systemPrompt
	if prompt == "" {
		prompt = "You are a helpful assistant."
	}
	agent := s.factory.NewAgent(prompt)
	var out string
	var err error
	if sa, ok := agent.(StreamAgent); ok && s.onChunk != nil {
		out, err = sa.ChatStream(ctx, ec.PrevOutput, s.onChunk)
	} else {
		out, err = agent.Chat(ctx, ec.PrevOutput)
	}
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}

// =============================================================================
// AgentStrategy —— 完整 Agent ReAct 循环 + 工具调用
// =============================================================================

// AgentStrategy 执行完整的 Agent ReAct 循环，可调用工具。
// 等价于当前 sugar.go 中 Auto() 节点的自动执行行为。
type AgentStrategy struct {
	systemPrompt string
	toolFilter   []string
	factory      AgentFactory
	onChunk      func(string) // 流式输出回调，nil = 不流式
}

// NewAgentStrategy 创建 AgentStrategy。
//   toolFilter: 可选的工具白名单，空列表表示不限制
func NewAgentStrategy(factory AgentFactory, systemPrompt string, toolFilter ...string) *AgentStrategy {
	return &AgentStrategy{factory: factory, systemPrompt: systemPrompt, toolFilter: toolFilter}
}

func (s *AgentStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
	prompt := s.systemPrompt
	if prompt == "" {
		prompt = "You are a helpful assistant."
	}
	agent := s.factory.NewAgent(prompt)
	if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && len(s.toolFilter) > 0 {
		f.SetToolFilter(s.toolFilter)
	}
	var out string
	var err error
	if sa, ok := agent.(StreamAgent); ok && s.onChunk != nil {
		out, err = sa.ChatStream(ctx, ec.PrevOutput, s.onChunk)
	} else {
		out, err = agent.Chat(ctx, ec.PrevOutput)
	}
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}
