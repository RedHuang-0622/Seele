package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// =============================================================================
// primitive.go —— 私有原语层
// =============================================================================
//
// 所有执行逻辑在此，公有语法糖（sugar.go）只做声明式节点构造，不含执行代码。
// 设计原则：每个 primitive 方法接收 node + prevJSON，返回 output + error。

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
func (wp *WorkPlan) primitiveAuto(ctx context.Context, n *node, input string) (string, error) {
	agent := wp.primitiveNewAgent(n)
	out, err := agent.Chat(ctx, input)
	if err != nil {
		return "", err
	}
	return toJSON(out), nil
}

// [workplangate] prepareApprove 生成审批计划（Run 时调用，不阻塞）。
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

// primitiveLoop 带 Signal 的循环原语。
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
			defer wg.Done()
			defer func() {
					if r := recover(); r != nil {
						results[i] = branchResult{label: b.Label, err: fmt.Errorf("branch panic: %v", r)}
					}
				}()

			// 获取信号量前先检查父 context 是否已取消
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			// 获取信号量后再次检查（排队期间父 context 可能被取消）
			if ctx.Err() != nil {
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}

			input := wp.primitiveRenderInput(b.Input, prevJSON)
			prompt := b.SystemPrompt
			if prompt == "" {
				prompt = wp.defaultPrompt
			}
			agent := wp.factory.NewAgent(prompt)
		if agent == nil {
				results[i] = branchResult{label: b.Label, err: fmt.Errorf("factory returned nil agent for prompt: %q", prompt)}
				return
			}
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

	// 所有分支汇合后检查父 context：若已取消，不继续处理结果
	if ctx.Err() != nil {
		return "", ctx.Err()
	}

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
		// 条件节点的 next 由分支逻辑决定，不自动推导
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
