package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Agent 接口：WorkPlan 只依赖这个接口，不直接导入 Seele 包，
// 避免循环依赖，也方便测试时 mock。
//
// Seele.Agent 天然满足此接口（Chat / ChatStream 签名一致）。
type Agent interface {
	Chat(ctx context.Context, input string) (string, error)
}

// StreamAgent 是可选的 Agent 扩展接口。
// 实现了此接口的 Agent 可以在 Chat 时通过 onChunk 回调逐块输出流式结果。
// WorkPlan 在执行 Auto/LLM 节点时，如果 Agent 实现了 StreamAgent 且
// 节点配置了 onChunk 回调，则会调用 ChatStream 替代 Chat。
type StreamAgent interface {
	ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
}

// AgentFactory 由上层（Engine / Runtime）注入，WorkPlan 用它创建 Agent。
type AgentFactory interface {
	NewAgent(systemPrompt string) Agent
}

// ── WorkPlanOption 函数选项 ──────────────────────────────────────

// WorkPlanOption 是 WorkPlan 构造时的可选配置。
type WorkPlanOption func(*WorkPlan)

// WithSemaphore 设置 WorkPlan 执行时的并发信号量。
// 多个 WorkPlan 可共享同一个 buffered channel，实现跨实例的并发控制。
// nil 表示不限制并发。
func WithSemaphore(sem chan struct{}) WorkPlanOption {
	return func(wp *WorkPlan) { wp.sem = sem }
}

// WithTracer 设置 WorkPlan 的可观测性追踪器。
// tracer 为 nil 时不追踪（默认行为）。
func WithTracer(tracer Tracer) WorkPlanOption {
	return func(wp *WorkPlan) { wp.tracer = tracer }
}

// WithMaxForkConcurrency 设置 Fork 节点的最大并发分支数。默认 3。
func WithMaxForkConcurrency(n int) WorkPlanOption {
	return func(wp *WorkPlan) { wp.maxForkConcurrency = n }
}

// SemaphoreProvider 是 AgentFactory 的可选扩展接口。
// 当 AgentFactory 实现此接口时，WorkPlan 可自动获取并发信号量。
type SemaphoreProvider interface {
	WorkPlanSemaphore() chan struct{}
}

// SemaphoreOpts 从 AgentFactory 提取并发信号量选项（如可用）。
// 用于 workflow 函数中便捷注入：
//
//	wp := workplan.New(factory, gate, prompt, workplan.SemaphoreOpts(factory)...)
func SemaphoreOpts(factory AgentFactory) []WorkPlanOption {
	if sp, ok := factory.(SemaphoreProvider); ok {
		if sem := sp.WorkPlanSemaphore(); sem != nil {
			return []WorkPlanOption{WithSemaphore(sem)}
		}
	}
	return nil
}

// =============================================================================
// WorkPlan
// =============================================================================

// WorkPlan 是工作流的定义和执行引擎。
//
// 构建期（链式调用 Auto/If/Switch/Loop/Fork/Emit...）：
//
//	填充 nodes / nodeIndex / entryID
//
// 执行期（Run）：
//
//	按节点顺序执行，私有原语方法（primitiveXxx）负责具体逻辑，
//	公有语法糖只是原语的声明式包装，不含执行逻辑。
//
// [workplangate] 两段式审批：
//
//	Run() 遇到 Approve 节点时生成计划后暂停，返回 StateAwaitingApproval。
//	调用方拿到 PendingQuestion 发送给用户，用户决策后调用 SetDecision + Resume 继续。
type WorkPlan struct {
	// ── 构建期 ────────────────────────────────────────────────────
	graph     *Graph          // 底层图引擎（v0.2 新增）
	nodes     []*node         // 保留：构建期 node 列表（逐步废弃）
	nodeIndex map[string]*node // 保留：ID→node 索引（sugar 构建期使用）
	entryID   string          // 入口节点 ID
	lastNodeID string         // sugar 自动连边跟踪（v0.2 新增）

	defaultPrompt      string
	factory            AgentFactory
	gate               ApprovalGate
	sem                chan struct{} // 可选的并发信号量，nil = 不限
	maxForkConcurrency int           // Fork 最大并发分支数，默认 3

	// ── 可观测性 ──────────────────────────────────────────────────
	tracer Tracer // 可选追踪，nil = 不追踪

	// ── 执行期（Run 时初始化）────────────────────────────────────
	vars map[string]string // Emit 写入的命名变量
	mu   sync.RWMutex

	// [workplangate] 执行状态机
	execID        string
	execState     ExecState
	pauseSnapshot *pauseSnapshot
	pauseDecision any
}

// New 创建 WorkPlan。
func New(factory AgentFactory, gate ApprovalGate, defaultPrompt string, opts ...WorkPlanOption) *WorkPlan {
	if gate == nil {
		gate = &CLIApprovalGate{}
	}
	wp := &WorkPlan{
		graph:              NewGraph(),
		nodeIndex:          make(map[string]*node),
		factory:            factory,
		gate:               gate,
		defaultPrompt:      defaultPrompt,
		maxForkConcurrency: 3,
		execState:          StateNotStarted,
	}
	for _, o := range opts {
		o(wp)
	}
	return wp
}

// =============================================================================
// Run —— 执行引擎入口
// =============================================================================

func (wp *WorkPlan) Run(ctx context.Context) (*WorkPlanResult, error) {
	if err := wp.Validate(); err != nil {
		return nil, err
	}

	// 并发限制（实例级信号量，nil = 不限）
	if wp.sem != nil {
		select {
		case wp.sem <- struct{}{}:
			defer func() { <-wp.sem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// 初始化执行状态（保留 ToTool 预注入的 vars）
	if wp.vars == nil {
		wp.vars = make(map[string]string)
	}
	wp.execID = fmt.Sprintf("exec_%d", time.Now().UnixNano())
	wp.execState = StateExecuting
	wp.pauseSnapshot = nil
	wp.pauseDecision = nil

	result := &WorkPlanResult{
		Vars:        wp.vars,
		Checkpoints: make(map[string]string),
	}
	start := time.Now()

	// ── Tracer：创建根 span ───────────────────────────────────────
	var rootSpan Span
	if wp.tracer != nil {
		ctx, rootSpan = wp.tracer.NewTrace(ctx, wp.execID)
		rootSpan.SetAttr("entry_node", wp.entryID)
	}

	// 构建执行上下文（v0.2：图引擎通过 ec 传递状态）
	ec := &ExecutionContext{
		Vars:       wp.vars,
		PrevOutput: `""`,
		Result:     result,
		Metadata:   make(map[string]any),
	}

	currentID := wp.entryID
	for currentID != "" {
		select {
		case <-ctx.Done():
			result.Aborted = true
			result.AbortReason = "context cancelled"
			result.TotalElapsed = time.Since(start)
			return result, nil
		default:
		}

		n, ok := wp.nodeIndex[currentID]
		if !ok {
			result.TotalElapsed = time.Since(start)
			return result, fmt.Errorf("WorkPlan.Run: node %q not found", currentID)
		}

		// Approve 节点：生成计划后暂停
		if n.kind == kindApprove {
			planText, q, err := wp.prepareApprove(ctx, n, ec.PrevOutput)
			if err != nil {
				wp.execState = StateFailed
				result.TotalElapsed = time.Since(start)
				return result, fmt.Errorf("node %q: prepare approve: %w", n.id, err)
			}
			wp.pauseSnapshot = &pauseSnapshot{
				currentID: currentID,
				prevJSON:  ec.PrevOutput,
				result:    result,
				planText:  planText,
				question:  q,
				startedAt: start,
			}
			wp.execState = StateAwaitingApproval
			result.PausedWorkPlan = wp
			return result, nil
		}

		// 其他节点：通过图引擎获取 runner 并执行
		runner := wp.graph.GetNode(currentID)
		if runner == nil {
			result.TotalElapsed = time.Since(start)
			if rootSpan != nil {
				rootSpan.SetAttr("error", fmt.Sprintf("runner for node %q not found", currentID))
				rootSpan.End()
			}
			return result, fmt.Errorf("WorkPlan.Run: runner for node %q not found in graph", currentID)
		}

		// ── Tracer：创建节点 span ──────────────────────────────────
		var nodeSpan Span
		if wp.tracer != nil {
			ctx, nodeSpan = wp.tracer.StartSpan(ctx, "node:"+currentID, SpanNode, map[string]string{
				"node_id":   currentID,
				"node_kind": n.kind.String(),
			})
		}

		nodeStart := time.Now()
		output, err := runner.Run(ctx, ec)

		if nodeSpan != nil {
			if err != nil {
				nodeSpan.End(WithSpanError(err))
			} else {
				nodeSpan.End()
			}
		}

		nr := &NodeResult{
			NodeID:    currentID,
			Kind:      n.kind.String(),
			Output:    output,
			StartedAt: nodeStart,
			EndedAt:   time.Now(),
		}
		result.NodeResults = append(result.NodeResults, nr)

		if err != nil {
			nr.Err = err
			wp.execState = StateFailed
			result.TotalElapsed = time.Since(start)
			return result, fmt.Errorf("node %q: %w", n.id, err)
		}
		if output != "" {
			ec.PrevOutput = output
		}

		// 通过图引擎统一路由
		currentID = wp.graph.resolve(currentID, ec)
	}

	wp.execState = StateCompleted
	result.TotalElapsed = time.Since(start)
	if rootSpan != nil {
		rootSpan.End()
	}
	return result, nil
}

// ── [workplangate] 公共方法 ──────────────────────────────────────

// ExecState 返回当前执行状态。
func (wp *WorkPlan) ExecState() ExecState { return wp.execState }

// ExecID 返回唯一执行 ID。
func (wp *WorkPlan) ExecID() string { return wp.execID }

// SetExecID 覆盖自动生成的执行 ID（用于跨服务关联）。
func (wp *WorkPlan) SetExecID(id string) { wp.execID = id }

// PendingQuestion 返回暂停时等待审批的 Question。
// 仅在 ExecState == StateAwaitingApproval 时有值。
func (wp *WorkPlan) PendingQuestion() Question {
	if wp.pauseSnapshot == nil {
		return Question{}
	}
	return wp.pauseSnapshot.question
}

// SetDecision 设置审批结果 V，必须在 Resume 前调用。
func (wp *WorkPlan) SetDecision(v any) { wp.pauseDecision = v }

// =============================================================================
// ToTool —— 将 WorkPlan 包装为可调用的工具（v0.5 新增）
// =============================================================================

// WorkPlanTool 是 WorkPlan 的工具包装。
// 调用方可将此结构注册到 Agent 或自行处理。
// workplan 包保持零外部依赖，不引入 core/tool 包。
type WorkPlanTool struct {
	Name        string                 // 工具名称
	Description string                 // 工具描述
	InputSchema map[string]interface{} // JSON Schema
	PlanRef     *Plan                  // 绑定的 Plan 快照（ToTool 时由 ToPlan() 生成）
	Run         func(ctx context.Context, argsJSON string) (string, error)
}

// ToTool 将 WorkPlan 包装为可通过 tool_call 调用的工具。
//   name:  工具名称（LLM 可见）
//   desc:  工具描述
//   inputSchema: 工具参数 JSON Schema
//
// 示例（在 engine 侧注册）：
//
//	wp := workplan.New(factory, nil, "prompt")
//	wp.Auto("分析", "...").
//	  Fork("并发", [...]).
//	  Auto("汇总", "...")
//	tool := wp.ToTool("workflow_analysis", "执行多步骤分析", schema)
//	engine.RegisterInlineTool(tool.Name, tool.Description, tool.InputSchema, tool.Run)
func (wp *WorkPlan) ToTool(name, desc string, inputSchema map[string]interface{}) WorkPlanTool {
	return WorkPlanTool{
		Name:        name,
		Description: desc,
		InputSchema: inputSchema,
		PlanRef:     wp.ToPlan(),
		Run: func(ctx context.Context, argsJSON string) (string, error) {
			// 解析 argsJSON，将参数注入 wp.vars（供 WorkPlan 执行时通过 ec.Vars 读取）
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(argsJSON), &args); err == nil {
				for k, v := range args {
					if str, ok := v.(string); ok {
						if wp.vars == nil {
							wp.vars = make(map[string]string)
						}
						wp.vars[k] = str
					}
				}
			}
			result, err := wp.Run(ctx)
			if err != nil {
				return "", err
			}
			return result.FinalOutput(), nil
		},
	}
}

// =============================================================================
// Plan —— 可序列化的声明式工作流定义
// =============================================================================

// PlanNodeSpec 是可序列化的节点规格。
type PlanNodeSpec struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // "auto"|"method"|"llm"|"strategy"|"approve"|"if"|"switch"|"loop"|"fork"|"checkpoint"|"emit"|"join"
	Input        string   `json:"input,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	ToolFilter   []string `json:"tool_filter,omitempty"`
	Next         string   `json:"next,omitempty"`

	// approve
	ApproveOptions []ChoiceOption `json:"approve_options,omitempty"`

	// fork
	ForkBranches []ForkBranchSpec `json:"fork_branches,omitempty"`

	// loop
	LoopBodyID      string `json:"loop_body_id,omitempty"`
	LoopMaxIter     int    `json:"loop_max_iter,omitempty"`
	LoopExhaustedID string `json:"loop_exhausted_id,omitempty"`

	// if
	IfTrueID  string `json:"if_true_id,omitempty"`
	IfFalseID string `json:"if_false_id,omitempty"`

	// switch
	SwitchCases []SwitchCaseSpec `json:"switch_cases,omitempty"`

	// emit
	EmitKey string `json:"emit_key,omitempty"`
}

// ForkBranchSpec 可序列化的 ForkBranch。
type ForkBranchSpec struct {
	Label        string `json:"label"`
	SystemPrompt string `json:"system_prompt,omitempty"`
	Input        string `json:"input,omitempty"`
}

// SwitchCaseSpec 可序列化的 SwitchCase。
type SwitchCaseSpec struct {
	NextID string `json:"next_id"`
	Label  string `json:"label,omitempty"`
}

// PlanEdgeSpec 是可序列化的边规格。
type PlanEdgeSpec struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Label     string `json:"label,omitempty"`
	Condition string `json:"condition,omitempty"` // 条件标签
}

// Plan 是可序列化的工作流定义。
type Plan struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version,omitempty"`
	EntryNodeID string         `json:"entry_node_id"`
	Nodes       []PlanNodeSpec `json:"nodes"`
	Edges       []PlanEdgeSpec `json:"edges"`
}

// NewPlan 创建 Plan。
func NewPlan(name string) *Plan {
	return &Plan{Name: name}
}

// PlanNodeOpt 是 Add 方法的可选配置。
type PlanNodeOpt func(*PlanNodeSpec)

// Plan node options.
func WithInput(input string) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.Input = input }
}
func WithSystemPrompt(prompt string) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.SystemPrompt = prompt }
}
func WithToolFilter(tools ...string) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.ToolFilter = tools }
}
func WithPlanNext(id string) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.Next = id }
}
func WithApproveOptions(opts ...ChoiceOption) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.ApproveOptions = opts }
}
func WithForkBranches(branches ...ForkBranchSpec) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.ForkBranches = branches }
}
func WithLoopConfig(bodyID string, maxIter int, exhaustedID string) PlanNodeOpt {
	return func(s *PlanNodeSpec) {
		s.LoopBodyID = bodyID
		s.LoopMaxIter = maxIter
		s.LoopExhaustedID = exhaustedID
	}
}
func WithIfBranches(trueID, falseID string) PlanNodeOpt {
	return func(s *PlanNodeSpec) {
		s.IfTrueID = trueID
		s.IfFalseID = falseID
	}
}
func WithSwitchCases(cases ...SwitchCaseSpec) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.SwitchCases = cases }
}
func WithEmitKey(key string) PlanNodeOpt {
	return func(s *PlanNodeSpec) { s.EmitKey = key }
}

// Add 添加一个节点到 Plan。
func (p *Plan) Add(id, kind string, opts ...PlanNodeOpt) *Plan {
	spec := PlanNodeSpec{ID: id, Kind: kind}
	for _, o := range opts {
		o(&spec)
	}
	p.Nodes = append(p.Nodes, spec)
	return p
}

// Edge 添加一条边到 Plan。
func (p *Plan) Edge(from, to string) *Plan {
	p.Edges = append(p.Edges, PlanEdgeSpec{From: from, To: to})
	return p
}

// EdgeWith 添加一条带标签的边到 Plan。
func (p *Plan) EdgeWith(from, to, label string) *Plan {
	p.Edges = append(p.Edges, PlanEdgeSpec{From: from, To: to, Label: label})
	return p
}

// =============================================================================
// ConditionRegistry —— 条件标签注册表
// =============================================================================

// ConditionRegistry 管理条件标签到 EdgeCondition 的映射。
type ConditionRegistry struct {
	conditions map[string]EdgeCondition
}

// NewConditionRegistry 创建 ConditionRegistry。
func NewConditionRegistry() *ConditionRegistry {
	return &ConditionRegistry{conditions: make(map[string]EdgeCondition)}
}

// Register 注册一个条件标签。
func (r *ConditionRegistry) Register(name string, cond EdgeCondition) {
	r.conditions[name] = cond
}

// Resolve 解析条件标签。
func (r *ConditionRegistry) Resolve(name string) (EdgeCondition, bool) {
	c, ok := r.conditions[name]
	return c, ok
}

// =============================================================================
// 辅助工具：Kind 字符串 ↔ NodeKind 映射
// =============================================================================

var kindToStr = map[NodeKind]string{
	kindAuto:       "auto",
	kindMethod:     "method",
	kindLLM:        "llm",
	kindStrategy:   "strategy",
	kindApprove:    "approve",
	kindIf:         "if",
	kindSwitch:     "switch",
	kindLoop:       "loop",
	kindFork:       "fork",
	kindCheckpoint: "checkpoint",
	kindEmit:       "emit",
	kindJoin:       "join",
}

var strToKind = map[string]NodeKind{
	"auto":       kindAuto,
	"method":     kindMethod,
	"llm":        kindLLM,
	"strategy":   kindStrategy,
	"approve":    kindApprove,
	"if":         kindIf,
	"switch":     kindSwitch,
	"loop":       kindLoop,
	"fork":       kindFork,
	"checkpoint": kindCheckpoint,
	"emit":       kindEmit,
	"join":       kindJoin,
}

// =============================================================================
// nodeToSpec —— 内部 node → PlanNodeSpec
// =============================================================================

func nodeToSpec(n *node) PlanNodeSpec {
	spec := PlanNodeSpec{
		ID:           n.id,
		Kind:         kindToStr[n.kind],
		Input:        n.input,
		SystemPrompt: n.systemPrompt,
		ToolFilter:   n.toolFilter,
		Next:         n.next,
	}

	switch n.kind {
	case kindApprove:
		spec.ApproveOptions = n.approveOptions
	case kindFork:
		for _, b := range n.forkBranches {
			spec.ForkBranches = append(spec.ForkBranches, ForkBranchSpec{
				Label:        b.Label,
				SystemPrompt: b.SystemPrompt,
				Input:        b.Input,
			})
		}
	case kindLoop:
		spec.LoopBodyID = n.loopBodyID
		spec.LoopMaxIter = n.loopMaxIter
		spec.LoopExhaustedID = n.loopExhaustedID
	case kindIf:
		spec.IfTrueID = n.ifTrueID
		spec.IfFalseID = n.ifFalseID
	case kindSwitch:
		for _, c := range n.switchCases {
			spec.SwitchCases = append(spec.SwitchCases, SwitchCaseSpec{
				NextID: c.NextID,
			})
		}
	case kindEmit:
		spec.EmitKey = n.emitKey
	}

	return spec
}

// =============================================================================
// specToNode —— PlanNodeSpec → 内部 node
// =============================================================================

func specToNode(spec PlanNodeSpec) *node {
	n := &node{
		id:           spec.ID,
		kind:         strToKind[spec.Kind],
		input:        spec.Input,
		systemPrompt: spec.SystemPrompt,
		toolFilter:   spec.ToolFilter,
		next:         spec.Next,
	}

	switch n.kind {
	case kindApprove:
		n.approveOptions = spec.ApproveOptions
	case kindFork:
		for _, b := range spec.ForkBranches {
			n.forkBranches = append(n.forkBranches, ForkBranch{
				Label:        b.Label,
				SystemPrompt: b.SystemPrompt,
				Input:        b.Input,
			})
		}
	case kindLoop:
		n.loopBodyID = spec.LoopBodyID
		n.loopMaxIter = spec.LoopMaxIter
		n.loopExhaustedID = spec.LoopExhaustedID
	case kindIf:
		n.ifTrueID = spec.IfTrueID
		n.ifFalseID = spec.IfFalseID
	case kindSwitch:
		for _, c := range spec.SwitchCases {
			n.switchCases = append(n.switchCases, SwitchCase{NextID: c.NextID})
		}
	case kindEmit:
		n.emitKey = spec.EmitKey
	}

	return n
}

// =============================================================================
// specToRunner —— PlanNodeSpec → NodeRunner
// =============================================================================

func specToRunner(spec PlanNodeSpec, factory AgentFactory, defaultPrompt string) (NodeRunner, error) {
	switch spec.Kind {
	case "auto":
		return &strategyRunner{
			id: spec.ID,
			strategy: NewAgentStrategy(factory, spec.SystemPrompt, spec.ToolFilter...),
		}, nil

	case "method":
		return nil, fmt.Errorf("workplan.LoadPlan: method nodes require live function reference, cannot deserialize")

	case "llm":
		return &strategyRunner{
			id: spec.ID,
			strategy: NewLLMStrategy(factory, spec.SystemPrompt),
		}, nil

	case "strategy":
		return nil, fmt.Errorf("workplan.LoadPlan: strategy nodes require live strategy reference, cannot deserialize")

	case "approve":
		return &approveRunner{
			id:            spec.ID,
			systemPrompt:  spec.SystemPrompt,
			input:         spec.Input,
			options:       spec.ApproveOptions,
			factory:       factory,
			defaultPrompt: defaultPrompt,
		}, nil

	case "if", "switch":
		return &controlRunner{id: spec.ID, kind: spec.Kind}, nil

	case "loop":
		return &loopRunner{
			id:      spec.ID,
			maxIter: spec.LoopMaxIter,
			signal:  newSignal(),
		}, nil

	case "fork":
		branches := make([]ForkBranch, len(spec.ForkBranches))
		for i, b := range spec.ForkBranches {
			branches[i] = ForkBranch{
				Label:        b.Label,
				SystemPrompt: b.SystemPrompt,
				Input:        b.Input,
			}
		}
		return &forkRunner{
			id:            spec.ID,
			branches:      branches,
			factory:       factory,
			defaultPrompt: defaultPrompt,
		}, nil

	case "checkpoint":
		return &checkpointRunner{id: spec.ID}, nil

	case "emit":
		return &emitRunner{id: spec.ID, key: spec.EmitKey}, nil

	case "join":
		return &controlRunner{id: spec.ID, kind: "join"}, nil

	default:
		return nil, fmt.Errorf("workplan.LoadPlan: unknown node kind %q", spec.Kind)
	}
}

// =============================================================================
// ToPlan —— 将 WorkPlan 导出为 Plan
// =============================================================================

// ToPlan 将 WorkPlan 导出为可序列化的 Plan。
func (wp *WorkPlan) ToPlan() *Plan {
	plan := &Plan{
		EntryNodeID: wp.entryID,
	}
	for _, n := range wp.nodes {
		plan.Nodes = append(plan.Nodes, nodeToSpec(n))
	}
	for _, e := range wp.graph.edges {
		edgeSpec := PlanEdgeSpec{
			From:  e.From,
			To:    e.To,
			Label: e.Label,
		}
		plan.Edges = append(plan.Edges, edgeSpec)
	}
	return plan
}

// =============================================================================
// LoadPlan —— 从 Plan 加载到 WorkPlan
// =============================================================================

// LoadPlan 从 Plan 加载到 WorkPlan（重建图）。
// registry 可选的 ConditionRegistry，用于解析边上的条件标签。
func (wp *WorkPlan) LoadPlan(plan *Plan, registry *ConditionRegistry) error {
	if plan == nil {
		return fmt.Errorf("WorkPlan.LoadPlan: plan is nil")
	}

	// 重置 WorkPlan
	wp.nodeIndex = make(map[string]*node)
	wp.nodes = nil
	wp.graph = NewGraph()
	wp.entryID = plan.EntryNodeID
	wp.lastNodeID = ""

	// 重建节点
	for _, spec := range plan.Nodes {
		n := specToNode(spec)
		runner, err := specToRunner(spec, wp.factory, wp.defaultPrompt)
		if err != nil {
			return err
		}
		wp.nodes = append(wp.nodes, n)
		wp.nodeIndex[n.id] = n
		wp.graph.AddNode(runner)
	}

	// 重建边
	for _, edgeSpec := range plan.Edges {
		edge := Edge{
			From:  edgeSpec.From,
			To:    edgeSpec.To,
			Label: edgeSpec.Label,
		}
		// 解析条件标签
		if edgeSpec.Condition != "" && registry != nil {
			if cond, ok := registry.Resolve(edgeSpec.Condition); ok {
				edge.Condition = cond
			}
		}
		wp.graph.AddEdge(edge)
	}

	return wp.Validate()
}
