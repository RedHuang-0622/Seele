package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

var globalWorkPlanSem chan struct{}

// SetMaxConcurrentWorkPlans 限制全局同时执行的 WorkPlan 数量。
// 设为 0 或负数移除限制。默认不限。
func SetMaxConcurrentWorkPlans(n int) {
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
//   Run() 遇到 Approve 节点时生成计划后暂停，返回 StateAwaitingApproval。
//   调用方拿到 PendingQuestion 发送给用户，用户决策后调用 SetDecision + Resume 继续。
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
	mu   sync.RWMutex      // 保护 vars（Parallel/Fork 并发写入时使用）

	// [workplangate] 执行状态机
	execID         string         // 唯一执行 ID，Run 时自动生成
	execState      ExecState      // 当前执行状态
	pauseSnapshot  *pauseSnapshot // approve 节点暂停时的上下文
	pauseDecision  any            // Resume 前由调用方设置的审批结果 V
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
	if wp.entryID == "" {
		return nil, fmt.Errorf("WorkPlan.Run: no nodes defined")
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

// =============================================================================
// 私有原语层 —— 所有执行逻辑在此，公有糖函数不含执行代码
// =============================================================================

// primitiveRunNode 分发到具体节点类型的原语。
func (wp *WorkPlan) primitiveRunNode(
	ctx context.Context,
	n *node,
	prevJSON string,
	result *WorkPlanResult,
) (*NodeResult, error) {
	nr := &NodeResult{
		NodeID:    n.id,
		Kind:      n.kind.String(),
		StartedAt: time.Now(),
	}
	defer func() { nr.EndedAt = time.Now() }()

	input := wp.primitiveRenderInput(n.input, prevJSON)

	var err error
	switch n.kind {
	case kindAuto:
		nr.Output, err = wp.primitiveAuto(ctx, n, input)

	case kindApprove:
		// [workplangate] Approve 不在此处执行；Run 遇 approve 暂停，Resume 调 executeApprove
		nr.Output = prevJSON

	case kindIf, kindSwitch:
		// 条件节点不执行 Agent，只做路由，透传 prevJSON
		nr.Output = prevJSON

	case kindLoop:
		nr.Output, err = wp.primitiveLoop(ctx, n, prevJSON)

	case kindFork:
		nr.Output, err = wp.primitiveFork(ctx, n, prevJSON)

	case kindJoin:
		// Join 节点在 Fork 的原语里已经处理，这里只是占位
		nr.Output = prevJSON

	case kindCheckpoint:
		nr.Output = prevJSON
		result.Checkpoints[n.id] = prevJSON
		if n.checkpoint != nil {
			n.checkpoint.savedAt = time.Now()
			n.checkpoint.snapshot = prevJSON
		}

	case kindEmit:
		nr.Output = prevJSON
		wp.primitiveEmit(n.emitKey, prevJSON)
	}

	return nr, err
}

// primitiveAuto 执行一个完整的 Agent ReAct 循环。
// 这是最核心的原语：一个 Agent，一次 Chat，内部可能有多轮 tool_call。
func (wp *WorkPlan) primitiveAuto(ctx context.Context, n *node, input string) (string, error) {
	agent := wp.primitiveNewAgent(n)
	out, err := agent.Chat(ctx, input)
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}

// [workplangate] prepareApprove 生成审批计划（Run 时调用，不阻塞）。
// 返回结构化计划文本和 Question，调用方负责发送 Question 并收集决策。
func (wp *WorkPlan) prepareApprove(ctx context.Context, n *node, prevJSON string) (planText string, q Question, err error) {
	planAgent := wp.primitiveNewAgent(n)
	input := wp.primitiveRenderInput(n.input, prevJSON)
	planPrompt := fmt.Sprintf(
		`{"action":"plan","task":%q,"instruction":"Analyze the task and output a step-by-step execution plan. Do NOT call any tools. Output ONLY valid JSON, no markdown wrapping.","output_schema":{"summary":"string: one-line plan summary","steps":[{"order":"int","description":"string","tool":"string","actions":"string"}],"expected_output":"string: what this plan will produce"}}`,
		input,
	)
	planOut, err := planAgent.Chat(ctx, planPrompt)
	if err != nil {
		return "", Question{}, fmt.Errorf("prepareApprove: plan: %w", err)
	}

	q = Question{
		ID:      wp.execID + "_" + n.id,
		Content: planOut,
		Options: n.approveOptions,
		KVS:     n.buildKVS(),
		Timeout: n.approveTimeout,
	}
	return planOut, q, nil
}

// [workplangate] executeApprove 根据审批结果 V 执行 approve 节点（Resume 时调用）。
func (wp *WorkPlan) executeApprove(ctx context.Context, n *node, snap *pauseSnapshot) (*NodeResult, error) {
	nr := &NodeResult{
		NodeID:    n.id,
		Kind:      n.kind.String(),
		StartedAt: time.Now(),
	}
	defer func() { nr.EndedAt = time.Now() }()

	action, _ := wp.pauseDecision.(string)

	switch ApproveChoice(action) {
	case ChoiceSkip:
		nr.Skipped = true
		nr.Output = snap.prevJSON
		return nr, nil

	case ChoiceAbort:
		nr.Aborted = true
		nr.Output = snap.prevJSON
		return nr, nil

	default: // ChoiceExecute 或自定义 V
		execAgent := wp.primitiveNewAgent(n)
		input := wp.primitiveRenderInput(n.input, snap.prevJSON)
		out, err := execAgent.Chat(ctx, input)
		if err != nil {
			return nr, fmt.Errorf("executeApprove: %w", err)
		}
		nr.Output = toJSON(out)
		return nr, nil
	}
}

// [workplangate] Resume 从暂停的 approve 节点继续执行 WorkPlan。
// 调用前需先通过 SetDecision 设置审批结果 V。
func (wp *WorkPlan) Resume(ctx context.Context) (*WorkPlanResult, error) {
	snap := wp.pauseSnapshot
	if snap == nil {
		return nil, fmt.Errorf("WorkPlan.Resume: no pause snapshot, Run must be called first")
	}
	wp.pauseSnapshot = nil
	wp.execState = StateExecuting

	// 执行暂停的 approve 节点
	n, ok := wp.nodeIndex[snap.currentID]
	if !ok {
		return snap.result, fmt.Errorf("WorkPlan.Resume: paused node %q not found", snap.currentID)
	}
	if n.kind != kindApprove {
		return snap.result, fmt.Errorf("WorkPlan.Resume: paused node %q is not an approve node (kind=%s)", snap.currentID, n.kind.String())
	}

	nr, err := wp.executeApprove(ctx, n, snap)
	snap.result.NodeResults = append(snap.result.NodeResults, nr)
	if err != nil {
		wp.execState = StateFailed
		return snap.result, fmt.Errorf("node %q: execute approve: %w", n.id, err)
	}

	prevJSON := snap.prevJSON
	if !nr.Skipped && nr.Output != "" {
		prevJSON = nr.Output
	}
	if nr.Aborted {
		snap.result.Aborted = true
		snap.result.AbortReason = fmt.Sprintf("aborted at node %q", n.id)
		wp.execState = StateAborted
		snap.result.TotalElapsed = time.Since(snap.startedAt)
		return snap.result, nil
	}

	currentID := wp.primitiveNext(n, prevJSON)

	// 继续执行后续节点
	for currentID != "" {
		select {
		case <-ctx.Done():
			snap.result.Aborted = true
			snap.result.AbortReason = "context cancelled"
			snap.result.TotalElapsed = time.Since(snap.startedAt)
			return snap.result, nil
		default:
		}

		n2, ok := wp.nodeIndex[currentID]
		if !ok {
			snap.result.TotalElapsed = time.Since(snap.startedAt)
			return snap.result, fmt.Errorf("WorkPlan.Resume: node %q not found", currentID)
		}

		// 嵌套 Approve 节点：再次暂停
		if n2.kind == kindApprove {
			planText, q, err := wp.prepareApprove(ctx, n2, prevJSON)
			if err != nil {
				wp.execState = StateFailed
				snap.result.TotalElapsed = time.Since(snap.startedAt)
				return snap.result, fmt.Errorf("node %q: prepare approve: %w", n2.id, err)
			}
			wp.pauseSnapshot = &pauseSnapshot{
				currentID: currentID,
				prevJSON:  prevJSON,
				result:    snap.result,
				planText:  planText,
				question:  q,
				startedAt: snap.startedAt,
			}
			wp.execState = StateAwaitingApproval
			snap.result.PausedWorkPlan = wp
			return snap.result, nil
		}

		nr2, err := wp.primitiveRunNode(ctx, n2, prevJSON, snap.result)
		snap.result.NodeResults = append(snap.result.NodeResults, nr2)
		if err != nil {
			wp.execState = StateFailed
			snap.result.TotalElapsed = time.Since(snap.startedAt)
			return snap.result, fmt.Errorf("node %q: %w", n2.id, err)
		}
		if nr2.Aborted {
			snap.result.Aborted = true
			snap.result.AbortReason = fmt.Sprintf("aborted at node %q", n2.id)
			wp.execState = StateAborted
			break
		}
		if !nr2.Skipped && nr2.Output != "" {
			prevJSON = nr2.Output
		}
		currentID = wp.primitiveNext(n2, prevJSON)
	}

	wp.execState = StateCompleted
	snap.result.TotalElapsed = time.Since(snap.startedAt)
	return snap.result, nil
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

// primitiveLoop 带 Signal 的循环原语。
//
// 执行流程：
//
//	iter=0,1,2,...
//	├─ 执行循环体节点（primitiveAuto）
//	├─ signal.set(result, iter)       → 触发所有 OnUpdate 回调
//	├─ 检查 until(result) → true → 退出
//	└─ 检查 iter >= maxIter → 跳转 exhaustedID 或退出
//
// 循环体本身是一个 Auto 节点（或子 WorkPlan，后续扩展），
// 每次迭代的输入是上次迭代的输出（通过 {{.PrevResult}} 传递）。
func (wp *WorkPlan) primitiveLoop(ctx context.Context, n *node, initJSON string) (string, error) {
	sig := n.loopSignal
	if sig == nil {
		sig = newSignal()
		n.loopSignal = sig
	}
	defer sig.close()

	bodyNode, ok := wp.nodeIndex[n.loopBodyID]
	if !ok {
		return "", fmt.Errorf("primitiveLoop: body node %q not found", n.loopBodyID)
	}

	current := initJSON
	for iter := 0; ; iter++ {
		select {
		case <-ctx.Done():
			return sig.Get(), ctx.Err()
		default:
		}

		// 执行循环体
		input := wp.primitiveRenderInput(bodyNode.input, current)
		out, err := wp.primitiveAuto(ctx, bodyNode, input)
		if err != nil {
			return sig.Get(), fmt.Errorf("loop iter %d: %w", iter, err)
		}

		// 更新 Signal（触发回调）
		sig.set(out, iter+1)
		current = out

		// 退出条件：until 函数
		if n.loopUntil != nil && n.loopUntil(fromJSON(out)) {
			break
		}

		// 退出条件：最大迭代次数
		if n.loopMaxIter > 0 && iter+1 >= n.loopMaxIter {
			break
		}
	}

	return sig.Get(), nil
}

// primitiveFork 并发启动多个子 Agent，等全部完成后汇合结果。
//
// 每个 ForkBranch 独立运行一个 Agent ReAct 循环。
// 结果以 JSON object 形式汇合：{"label1": result1, "label2": result2}
// Join 节点（如果有）的输入就是这个 JSON object。
func (wp *WorkPlan) primitiveFork(ctx context.Context, n *node, prevJSON string) (string, error) {
	type branchResult struct {
		label string
		out   string
		err   error
	}

	results := make([]branchResult, len(n.forkBranches))
	var wg sync.WaitGroup

	const maxConcurrentFork = 3
	sem := make(chan struct{}, maxConcurrentFork)

	for i, branch := range n.forkBranches {
		wg.Add(1)
		go func(i int, b ForkBranch) {
			sem <- struct{}{}
			defer func() { <-sem }()
			defer wg.Done()
			input := wp.primitiveRenderInput(b.Input, prevJSON)
			prompt := b.SystemPrompt
			if prompt == "" {
				prompt = wp.defaultPrompt
			}
			agent := wp.factory.NewAgent(prompt)
			if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && n.toolFilter != nil {
				f.SetToolFilter(n.toolFilter)
			}
			out, err := agent.Chat(ctx, input)
			if err != nil {
				results[i] = branchResult{label: b.Label, err: err}
				return
			}
			results[i] = branchResult{label: b.Label, out: toJSON(out)}
		}(i, branch)
	}
	wg.Wait()

	// 汇合：构建 JSON object {"label": result, ...}
	merged := make(map[string]interface{}, len(results))
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", r.label, r.err))
			merged[r.label] = nil
			continue
		}
		// 尝试解析为 JSON value，失败则存字符串
		var v interface{}
		if err := json.Unmarshal([]byte(r.out), &v); err == nil {
			merged[r.label] = v
		} else {
			merged[r.label] = r.out
		}
	}

	if len(errs) > 0 && len(merged) == 0 {
		return "", fmt.Errorf("all fork branches failed: %s", strings.Join(errs, "; "))
	}

	b, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("primitiveFork: marshal result: %w", err)
	}
	return string(b), nil
}

// primitiveEmit 把当前 JSON 值写入命名变量。
// 写入后可在后续节点的 input 模板里用 {{.Vars.key}} 引用。
func (wp *WorkPlan) primitiveEmit(key, jsonVal string) {
	wp.mu.Lock()
	if wp.vars == nil {
		wp.vars = make(map[string]string)
	}
	wp.vars[key] = jsonVal
	wp.mu.Unlock()
}

// primitiveNext 决定当前节点之后跳转到哪个节点。
func (wp *WorkPlan) primitiveNext(n *node, prevJSON string) string {
	prev := fromJSON(prevJSON)

	switch n.kind {
	case kindIf:
		if n.ifCond != nil && n.ifCond(prev) {
			return n.ifTrueID
		}
		return n.ifFalseID

	case kindSwitch:
		for _, c := range n.switchCases {
			if c.Match == nil { // default 分支
				return c.NextID
			}
			if c.Match(prev) {
				return c.NextID
			}
		}
		return "" // 没有匹配到任何 case，结束

	case kindLoop:
		// Loop 节点自身处理完循环，next 指向循环后的节点
		if n.loopSignal != nil {
			// 检查是否因为 exhausted 需要走特殊出口
			if n.loopExhaustedID != "" && n.loopSignal.Iter() >= n.loopMaxIter {
				return n.loopExhaustedID
			}
		}
		return n.next

	default:
		return n.next
	}
}

// primitiveNewAgent 为节点创建专属 Agent。
func (wp *WorkPlan) primitiveNewAgent(n *node) Agent {
	prompt := n.systemPrompt
	if prompt == "" {
		prompt = wp.defaultPrompt
	}
	agent := wp.factory.NewAgent(prompt)
	if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && n.toolFilter != nil {
		f.SetToolFilter(n.toolFilter)
	}
	return agent
}

// primitiveRenderInput 渲染输入模板。
//
// 支持的占位符：
//
//	{{.PrevResult}}   上一节点输出的纯文本（JSON string 自动去引号）
//	{{.Vars.key}}     Emit 写入的命名变量（JSON string 自动去引号）
func (wp *WorkPlan) primitiveRenderInput(tmpl, prevJSON string) string {
	result := strings.ReplaceAll(tmpl, "{{.PrevResult}}", fromJSON(prevJSON))

	// 替换 {{.Vars.key}}
	wp.mu.RLock()
	vars := wp.vars
	wp.mu.RUnlock()

	for key, jsonVal := range vars {
		placeholder := "{{.Vars." + key + "}}"
		result = strings.ReplaceAll(result, placeholder, fromJSON(jsonVal))
	}
	return result
}

// primitiveAddNode 内部注册节点，维护链表的 next 自动推导。
func (wp *WorkPlan) primitiveAddNode(n *node) *WorkPlan {
	if len(wp.nodes) > 0 {
		prev := wp.nodes[len(wp.nodes)-1]
		// 条件节点的 next 由 If/Switch 的分支目标决定，不自动推导
		if prev.next == "" &&
			prev.kind != kindIf &&
			prev.kind != kindSwitch {
			prev.next = n.id
		}
	}
	if wp.entryID == "" {
		wp.entryID = n.id
	}
	wp.nodes = append(wp.nodes, n)
	wp.nodeIndex[n.id] = n
	return wp
}

// primitiveAutoID 生成唯一节点 ID。
func (wp *WorkPlan) primitiveAutoID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, len(wp.nodes)+1)
}
