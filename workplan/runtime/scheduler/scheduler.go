// Package scheduler orchestrates the execution of nodes in the graph.
// It determines execution order: sequential or concurrent (fork).
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// Scheduler drives the execution loop.
type Scheduler struct {
	graph              *graph.Graph
	executor           *executor.Executor
	MaxForkConcurrency int
	OnNodeDone         func(nr *types.NodeResult) // 每节点完成回调
}

// SetNodeHook 设置节点完成回调。
func (s *Scheduler) SetNodeHook(hook func(nr *types.NodeResult)) {
	s.OnNodeDone = hook
}

// New creates a scheduler bound to a graph and executor.
func New(g *graph.Graph, exec *executor.Executor) *Scheduler {
	return &Scheduler{graph: g, executor: exec, MaxForkConcurrency: 3}
}

// SetMaxForkConcurrency limits concurrently running branches in automatic forks.
func (s *Scheduler) SetMaxForkConcurrency(maxConcurrent int) {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	s.MaxForkConcurrency = maxConcurrent
}

// Run executes the graph from the entry node.
// Automatically detects fork patterns: when a node has multiple unconditional
// outgoing edges, all downstream nodes run concurrently (goroutine fan-out).
func (s *Scheduler) Run(ctx context.Context) (*types.WorkPlanResult, error) {
	wc := types.NewWorkflowContext()
	start := time.Now()
	currentID := s.graph.Entry()

	for currentID != "" {
		select {
		case <-ctx.Done():
			wc.Result.Aborted = true
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, nil
		default:
		}

		n := s.graph.GetNode(currentID)
		if n == nil {
			return wc.Result, fmt.Errorf("node %q not found", currentID)
		}

		nodeStart := time.Now()
		output, err := s.executor.RunNode(ctx, n, wc)
		nr := &types.NodeResult{
			NodeBase: types.NodeBase{
				NodeID:    currentID,
				Kind:      n.Kind().String(),
				Output:    output,
				StartedAt: nodeStart,
				EndedAt:   time.Now(),
			},
		}
		nr.Err = err
		nr.Status = statusFromResult(nr, err)
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		// Record output for multi-upstream reference via {{.PrevResults.nodeID}}
		if output != "" {
			wc.PrevResults[currentID] = output
		}

		if s.OnNodeDone != nil {
			s.OnNodeDone(nr)
		}

		if err != nil {
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, fmt.Errorf("node %q: %w", currentID, err)
		}

		if output != "" {
			wc.PrevOutput = output
		}

		// Resolve next node(s)
		nextIDs := s.graph.GetNextNodes(currentID, wc)
		if len(nextIDs) == 0 {
			break // end of graph
		}

		if len(nextIDs) == 1 {
			// Sequential: single next node
			currentID = nextIDs[0]
			continue
		}

		// ── Fork detected: multiple unconditional outgoing edges ──────
		// Run all downstream nodes concurrently, merge results.
		nextID, err := s.fork(ctx, nextIDs, wc)
		if err != nil {
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, err
		}
		currentID = nextID
		if currentID == "" {
			break
		}
	}

	wc.Result.TotalElapsed = time.Since(start)
	return wc.Result, nil
}

// fork runs all target nodes concurrently and merges their outputs.
// It cancels sibling branches on the first failure and only writes parent
// context after every branch succeeds.
func (s *Scheduler) fork(ctx context.Context, nextIDs []string, wc *types.WorkflowContext) (string, error) {
	type branchResult struct {
		id    string
		kind  string
		out   string
		err   error
		start time.Time
		end   time.Time
	}

	results := make([]branchResult, len(nextIDs))
	branchContexts := make([]*types.WorkflowContext, len(nextIDs))
	for index := range nextIDs {
		branchContexts[index] = wc.Clone()
	}
	var wg sync.WaitGroup
	forkCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sem := make(chan struct{}, s.maxForkConcurrency())

	for i, id := range nextIDs {
		wg.Add(1)
		go func(idx int, nodeID string) {
			defer wg.Done()
			start := time.Now()
			select {
			case sem <- struct{}{}:
			case <-forkCtx.Done():
				results[idx] = branchResult{id: nodeID, err: forkContextError(forkCtx), start: start, end: time.Now()}
				return
			}
			defer func() { <-sem }()

			n := s.graph.GetNode(nodeID)
			if n == nil {
				err := fmt.Errorf("fork node %q not found", nodeID)
				results[idx] = branchResult{id: nodeID, err: err, start: start, end: time.Now()}
				cancel(err)
				return
			}

			out, err := s.executor.RunNode(forkCtx, n, branchContexts[idx])
			results[idx] = branchResult{
				id: nodeID, kind: n.Kind().String(),
				out: out, err: err, start: start, end: time.Now(),
			}
			if err != nil {
				cancel(err)
			}
		}(i, id)
	}
	wg.Wait()

	// Record branch lifecycle results before determining whether join may run.
	var firstErr error
	var failedBranch string
	for _, r := range results {
		nr := &types.NodeResult{
			NodeBase: types.NodeBase{
				NodeID:    r.id,
				Kind:      r.kind,
				Output:    r.out,
				StartedAt: r.start,
				EndedAt:   r.end,
			},
			Err: r.err,
		}
		nr.Status = statusFromResult(nr, r.err)
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		if s.OnNodeDone != nil {
			s.OnNodeDone(nr)
		}

		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
				failedBranch = r.id
			}
		}
	}
	if firstErr != nil {
		return "", fmt.Errorf("fork branch %q: %w", failedBranch, firstErr)
	}

	merged := make(map[string]any, len(results))
	for _, r := range results {
		if r.out != "" {
			wc.PrevResults[r.id] = r.out
		}
		var v any
		if json.Unmarshal([]byte(r.out), &v) == nil {
			merged[r.id] = v
		} else {
			merged[r.id] = r.out
		}
	}

	// Store merged output
	b, _ := json.Marshal(merged)
	wc.PrevOutput = string(b)

	// Find common next node: all branches must converge to the same target
	var commonNext string
	for _, id := range nextIDs {
		nexts := s.graph.GetNextNodes(id, wc)
		for _, nid := range nexts {
			if commonNext == "" {
				commonNext = nid
			} else if commonNext != nid {
				// Divergent — can't continue deterministically
				return "", nil
			}
		}
	}

	return commonNext, nil
}

func (s *Scheduler) maxForkConcurrency() int {
	if s.MaxForkConcurrency <= 0 {
		return 3
	}
	return s.MaxForkConcurrency
}

func forkContextError(ctx context.Context) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

// RunWithCheckpoint is identical to Run but also returns per-node snapshots.
func (s *Scheduler) RunWithCheckpoint(ctx context.Context) (*types.WorkPlanResult, map[string]*types.Snapshot, error) {
	wc := types.NewWorkflowContext()
	start := time.Now()
	currentID := s.graph.Entry()
	checkpoints := make(map[string]*types.Snapshot)

	for currentID != "" {
		select {
		case <-ctx.Done():
			wc.Result.Aborted = true
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, checkpoints, nil
		default:
		}

		n := s.graph.GetNode(currentID)
		if n == nil {
			return wc.Result, checkpoints, fmt.Errorf("node %q not found", currentID)
		}

		nodeStart := time.Now()
		output, err := s.executor.RunNode(ctx, n, wc)
		nr := &types.NodeResult{
			NodeBase: types.NodeBase{
				NodeID:    currentID,
				Kind:      n.Kind().String(),
				Output:    output,
				StartedAt: nodeStart,
				EndedAt:   time.Now(),
			},
		}
		nr.Err = err
		nr.Status = statusFromResult(nr, err)
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		// Record output for multi-upstream reference via {{.PrevResults.nodeID}}
		if output != "" {
			wc.PrevResults[currentID] = output
		}

		if s.OnNodeDone != nil {
			s.OnNodeDone(nr)
		}

		if err != nil {
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, checkpoints, fmt.Errorf("node %q: %w", currentID, err)
		}

		if output != "" {
			wc.PrevOutput = output
		}

		checkpoints[currentID] = &types.Snapshot{
			NodeID: currentID, Context: wc,
			Timestamp: time.Now(), Status: types.StatusRunning,
		}

		nextIDs := s.graph.GetNextNodes(currentID, wc)
		if len(nextIDs) == 0 {
			break
		}
		if len(nextIDs) == 1 {
			currentID = nextIDs[0]
			continue
		}
		// Fork: use the same fork logic (simplified — no checkpoints for branches)
		nextID, err := s.fork(ctx, nextIDs, wc)
		if err != nil {
			wc.Result.TotalElapsed = time.Since(start)
			return wc.Result, checkpoints, err
		}
		currentID = nextID
		if currentID == "" {
			break
		}
	}

	wc.Result.TotalElapsed = time.Since(start)
	wc.Result.Vars = wc.Vars
	return wc.Result, checkpoints, nil
}

// statusFromResult derives the status string from a NodeResult and its error.
func statusFromResult(nr *types.NodeResult, err error) string {
	if nr.Aborted {
		return "aborted"
	}
	if err != nil {
		return "failed"
	}
	if nr.Skipped {
		return "skipped"
	}
	return "completed"
}
