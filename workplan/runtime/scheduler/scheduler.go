// Package scheduler orchestrates the execution of nodes in the graph.
// It determines the order: sequential, concurrent, or conditional.
package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// Scheduler drives the execution loop: get current node → execute → resolve next.
type Scheduler struct {
	graph    *graph.Graph
	executor *executor.Executor
}

// New creates a scheduler bound to a graph and executor.
func New(g *graph.Graph, exec *executor.Executor) *Scheduler {
	return &Scheduler{graph: g, executor: exec}
}

// Run executes the graph from the entry node, following edges until completion.
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

		// Handle checkpoint recording
		// CheckpointNode handles its own recording in Run()

		currentID = s.graph.Resolve(currentID, wc)
	}

	wc.Result.TotalElapsed = time.Since(start)
	return wc.Result, nil
}

// RunWithCheckpoint executes the graph and returns checkpoint snapshots for each node.
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

		// Record checkpoint
		checkpoints[currentID] = &types.Snapshot{
			NodeID:    currentID,
			Context:   wc,
			Timestamp: time.Now(),
			Status:    types.StatusRunning,
		}

		currentID = s.graph.Resolve(currentID, wc)
	}

	wc.Result.TotalElapsed = time.Since(start)
	wc.Result.Vars = wc.Vars
	return wc.Result, checkpoints, nil
}
