package workplan

import (
	"context"
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

	// 初始化执行状态
	wp.vars = make(map[string]string)
	wp.execID = fmt.Sprintf("exec_%d", time.Now().UnixNano())
	wp.execState = StateExecuting
	wp.pauseSnapshot = nil
	wp.pauseDecision = nil

	result := &WorkPlanResult{
		Vars:        wp.vars,
		Checkpoints: make(map[string]string),
	}
	start := time.Now()

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
			return result, fmt.Errorf("WorkPlan.Run: runner for node %q not found in graph", currentID)
		}

		nodeStart := time.Now()
		output, err := runner.Run(ctx, ec)

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
