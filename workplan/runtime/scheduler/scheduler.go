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
	graph    *graph.Graph
	executor *executor.Executor
}

// New creates a scheduler bound to a graph and executor.
func New(g *graph.Graph, exec *executor.Executor) *Scheduler {
	return &Scheduler{graph: g, executor: exec}
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
			NodeID: currentID, Kind: n.Kind().String(),
			Output: output, StartedAt: nodeStart, EndedAt: time.Now(),
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)

		if err != nil {
			nr.Err = err
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
		currentID = s.fork(ctx, nextIDs, wc)
		if currentID == "" {
			break
		}
	}

	wc.Result.TotalElapsed = time.Since(start)
	return wc.Result, nil
}

// fork runs all target nodes concurrently and merges their outputs.
// Returns the common next node ID for all branches, or "" if graph ends.
func (s *Scheduler) fork(ctx context.Context, nextIDs []string, wc *types.WorkflowContext) string {
	type branchResult struct {
		id    string
		kind  string
		out   string
		err   error
		start time.Time
		end   time.Time
	}

	results := make([]branchResult, len(nextIDs))
	var wg sync.WaitGroup

	for i, id := range nextIDs {
		wg.Add(1)
		go func(idx int, nodeID string) {
			defer wg.Done()
			start := time.Now()

			n := s.graph.GetNode(nodeID)
			if n == nil {
				results[idx] = branchResult{id: nodeID, err: fmt.Errorf("fork node %q not found", nodeID), start: start, end: time.Now()}
				return
			}

			out, err := s.executor.RunNode(ctx, n, wc)
			results[idx] = branchResult{
				id: nodeID, kind: n.Kind().String(),
				out: out, err: err, start: start, end: time.Now(),
			}
		}(i, id)
	}
	wg.Wait()

	// Record branch results and collect errors
	merged := make(map[string]any)
	var firstErr error
	for _, r := range results {
		nr := &types.NodeResult{
			NodeID: r.id, Kind: r.kind,
			Output: r.out, Err: r.err,
			StartedAt: r.start, EndedAt: r.end,
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)

		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			merged[r.id] = nil
		} else {
			var v any
			if json.Unmarshal([]byte(r.out), &v) == nil {
				merged[r.id] = v
			} else {
				merged[r.id] = r.out
			}
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
				return ""
			}
		}
	}

	if firstErr != nil {
		return commonNext // let runner report the error
	}
	return commonNext
}

// RunWithCheckpoint is identical to Run but also returns per-node snapshots.
// (Implementation preserved from original — fork logic not yet applied here)
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
			NodeID: currentID, Kind: n.Kind().String(),
			Output: output, StartedAt: nodeStart, EndedAt: time.Now(),
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)

		if err != nil {
			nr.Err = err
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
		currentID = s.fork(ctx, nextIDs, wc)
		if currentID == "" {
			break
		}
	}

	wc.Result.TotalElapsed = time.Since(start)
	wc.Result.Vars = wc.Vars
	return wc.Result, checkpoints, nil
}
