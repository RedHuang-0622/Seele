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

// ── 全局 WorkPlan 并发控制 ────────────────────────────────────────

var (
	globalWorkPlanSem   chan struct{}
	globalWorkPlanSemMu sync.Mutex
)

// SetMaxConcurrentWorkPlans 限制全局同时执行的 WorkPlan 数量。
// 设为 0 或负数移除限制。默认不限。并发安全。
func SetMaxConcurrentWorkPlans(n int) {
	globalWorkPlanSemMu.Lock()
	defer globalWorkPlanSemMu.Unlock()
	if n <= 0 {
		globalWorkPlanSem = nil
		return
	}
	globalWorkPlanSem = make(chan struct{}, n)
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
	nodes     []*node
	nodeIndex map[string]*node
	entryID   string

	defaultPrompt string
	factory       AgentFactory
	gate          ApprovalGate

	// ── 执行期（Run 时初始化）────────────────────────────────────
	vars map[string]string // Emit 写入的命名变量，key → JSON 字符串
	mu   sync.RWMutex      // 保护 vars（Fork 并发写入时使用）

	// [workplangate] 执行状态机
	execID        string         // 唯一执行 ID，Run 时自动生成
	execState     ExecState      // 当前执行状态
	pauseSnapshot *pauseSnapshot // approve 节点暂停时的上下文
	pauseDecision any            // Resume 前由调用方设置的审批结果 V
}

// New 创建 WorkPlan。
//
//	factory       必填，用于创建子 Agent
//	gate          人工确认实现，nil 时使用 CLIApprovalGate
//	defaultPrompt 所有节点共享的默认系统提示词
func New(factory AgentFactory, gate ApprovalGate, defaultPrompt string) *WorkPlan {
	if gate == nil {
		gate = &CLIApprovalGate{}
	}
	return &WorkPlan{
		nodeIndex:     make(map[string]*node),
		factory:       factory,
		gate:          gate,
		defaultPrompt: defaultPrompt,
		// [workplangate] 初始状态
		execState: StateNotStarted,
	}
}

// =============================================================================
// Run —— 执行引擎入口
// =============================================================================

func (wp *WorkPlan) Run(ctx context.Context) (*WorkPlanResult, error) {
	if err := wp.Validate(); err != nil {
		return nil, err
	}

	// 全局并发限制：获取执行槽位
	if globalWorkPlanSem != nil {
		select {
		case globalWorkPlanSem <- struct{}{}:
			defer func() { <-globalWorkPlanSem }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// [workplangate] 初始化执行状态
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

	prevJSON := `""` // 上一个节点的 JSON 输出，初始为空字符串
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

		// [workplangate] Approve 节点：生成计划后暂停，不在此处阻塞等用户
		if n.kind == kindApprove {
			planText, q, err := wp.prepareApprove(ctx, n, prevJSON)
			if err != nil {
				wp.execState = StateFailed
				result.TotalElapsed = time.Since(start)
				return result, fmt.Errorf("node %q: prepare approve: %w", n.id, err)
			}
			wp.pauseSnapshot = &pauseSnapshot{
				currentID: currentID,
				prevJSON:  prevJSON,
				result:    result,
				planText:  planText,
				question:  q,
				startedAt: start,
			}
			wp.execState = StateAwaitingApproval
			result.PausedWorkPlan = wp
			return result, nil
		}

		nr, err := wp.primitiveRunNode(ctx, n, prevJSON, result)
		result.NodeResults = append(result.NodeResults, nr)

		if err != nil {
			wp.execState = StateFailed
			result.TotalElapsed = time.Since(start)
			return result, fmt.Errorf("node %q: %w", n.id, err)
		}
		if nr.Aborted {
			result.Aborted = true
			result.AbortReason = fmt.Sprintf("aborted at node %q", n.id)
			wp.execState = StateAborted
			break
		}
		if !nr.Skipped && nr.Output != "" {
			prevJSON = nr.Output
		}

		currentID = wp.primitiveNext(n, prevJSON)
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
