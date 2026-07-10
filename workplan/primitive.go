package workplan

import (
	"context"
	"fmt"
	"time"
)

// =============================================================================
// primitive.go —— approve 节点的执行逻辑
// =============================================================================
//
// v0.4 重构：原语执行引擎已全部迁至 runner.go + graph.go。
// 本文件仅保留 approve 节点的 prepare/execute 逻辑（因需要访问 WorkPlan 内部状态）。
// Resume() 使用图引擎的 runner.Run() + graph.resolve() 执行后续节点。

// [workplangate] prepareApprove 生成审批计划（Run 时调用，不阻塞）。
func (wp *WorkPlan) prepareApprove(ctx context.Context, n *node, prevJSON string) (planText string, q Question, err error) {
	prompt := n.systemPrompt
	if prompt == "" {
		prompt = wp.defaultPrompt
	}
	planAgent := wp.factory.NewAgent(prompt)
	if f, ok := planAgent.(interface{ SetToolFilter([]string) }); ok && n.toolFilter != nil {
		f.SetToolFilter(n.toolFilter)
	}
	input := renderTemplate(n.input, &ExecutionContext{PrevOutput: prevJSON, Vars: wp.vars})
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
		prompt := n.systemPrompt
		if prompt == "" {
			prompt = wp.defaultPrompt
		}
		execAgent := wp.factory.NewAgent(prompt)
		if f, ok := execAgent.(interface{ SetToolFilter([]string) }); ok && n.toolFilter != nil {
			f.SetToolFilter(n.toolFilter)
		}
		input := renderTemplate(n.input, &ExecutionContext{PrevOutput: snap.prevJSON, Vars: wp.vars})
		out, err := execAgent.Chat(ctx, input)
		if err != nil {
			return nr, fmt.Errorf("executeApprove: %w", err)
		}
		nr.Output = toJSON(out)
		return nr, nil
	}
}

// [workplangate] Resume 从暂停的 approve 节点继续执行 WorkPlan。
//
// v0.4 重构：使用图引擎（graph.GetNode + runner.Run + graph.resolve）执行后续节点，
// 不再依赖旧的 primitiveRunNode / primitiveNext。
func (wp *WorkPlan) Resume(ctx context.Context) (*WorkPlanResult, error) {
	snap := wp.pauseSnapshot
	if snap == nil {
		return nil, fmt.Errorf("WorkPlan.Resume: no pause snapshot, Run must be called first")
	}
	wp.pauseSnapshot = nil
	wp.execState = StateExecuting

	// ── Tracer：创建根 span ───────────────────────────────────────
	var rootSpan Span
	if wp.tracer != nil {
		ctx, rootSpan = wp.tracer.NewTrace(ctx, wp.execID)
	}

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

	ec := &ExecutionContext{
		Vars:       wp.vars,
		PrevOutput: snap.prevJSON,
		Result:     snap.result,
		Metadata:   make(map[string]any),
	}

	if !nr.Skipped && nr.Output != "" {
		ec.PrevOutput = nr.Output
	}
	if nr.Aborted {
		snap.result.Aborted = true
		snap.result.AbortReason = fmt.Sprintf("aborted at node %q", n.id)
		wp.execState = StateAborted
		snap.result.TotalElapsed = time.Since(snap.startedAt)
		return snap.result, nil
	}

	// 通过图引擎执行后续节点（与 Run() 使用相同的 runner + resolve 路径）
	currentID := wp.graph.resolve(snap.currentID, ec)

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
			planText, q, err := wp.prepareApprove(ctx, n2, ec.PrevOutput)
			if err != nil {
				wp.execState = StateFailed
				snap.result.TotalElapsed = time.Since(snap.startedAt)
				return snap.result, fmt.Errorf("node %q: prepare approve: %w", n2.id, err)
			}
			wp.pauseSnapshot = &pauseSnapshot{
				currentID: currentID,
				prevJSON:  ec.PrevOutput,
				result:    snap.result,
				planText:  planText,
				question:  q,
				startedAt: snap.startedAt,
			}
			wp.execState = StateAwaitingApproval
			snap.result.PausedWorkPlan = wp
			return snap.result, nil
		}

		// 使用图引擎的 runner 执行节点
		runner := wp.graph.GetNode(currentID)
		if runner == nil {
			snap.result.TotalElapsed = time.Since(snap.startedAt)
			return snap.result, fmt.Errorf("WorkPlan.Resume: runner for node %q not found in graph", currentID)
		}

		nodeStart := time.Now()
		output, err := runner.Run(ctx, ec)

		nr2 := &NodeResult{
			NodeID:    currentID,
			Kind:      n2.kind.String(),
			Output:    output,
			StartedAt: nodeStart,
			EndedAt:   time.Now(),
		}
		snap.result.NodeResults = append(snap.result.NodeResults, nr2)

		if err != nil {
			nr2.Err = err
			wp.execState = StateFailed
			snap.result.TotalElapsed = time.Since(snap.startedAt)
			return snap.result, fmt.Errorf("node %q: %w", n2.id, err)
		}
		if output != "" {
			ec.PrevOutput = output
		}

		currentID = wp.graph.resolve(currentID, ec)
	}

	wp.execState = StateCompleted
	snap.result.TotalElapsed = time.Since(snap.startedAt)
	if rootSpan != nil {
		rootSpan.End()
	}
	return snap.result, nil
}
