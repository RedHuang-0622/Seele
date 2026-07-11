// Package runner is the top-level entry point for workflow execution.
// It provides Run() for fresh execution and Resume() for checkpoint recovery.
package runner

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/checkpoint"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
	"github.com/RedHuang-0622/Seele/workplan/runtime/scheduler"
	"github.com/RedHuang-0622/Seele/workplan/runtime/validate"
)

// Runner is the entry point for workflow execution.
type Runner struct {
	graph    *graph.Graph
	sched    *scheduler.Scheduler
	exec     *executor.Executor
	checkMgr *checkpoint.Manager
	factory  node.AgentFactory
}

// Option configures the runner.
type Option func(*Runner)

// New creates a runner from a graph.
func New(g *graph.Graph, factory node.AgentFactory, opts ...Option) *Runner {
	exec := executor.New()
	sched := scheduler.New(g, exec)
	r := &Runner{
		graph:   g,
		sched:   sched,
		exec:    exec,
		factory: factory,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// WithCheckpoint enables checkpoint support with the given store.
func WithCheckpoint(store checkpoint.Store) Option {
	return func(r *Runner) {
		r.checkMgr = checkpoint.NewManager(store)
	}
}

// Run validates and executes the graph from the beginning.
func (r *Runner) Run(ctx context.Context) (*types.WorkPlanResult, error) {
	if err := validate.Graph(r.graph); err != nil {
		return nil, fmt.Errorf("graph validation: %w", err)
	}
	return r.sched.Run(ctx)
}

// Resume continues execution from a saved checkpoint.
func (r *Runner) Resume(ctx context.Context, snapshotID string) (*types.WorkPlanResult, error) {
	if r.checkMgr == nil {
		return nil, fmt.Errorf("checkpoint not enabled: use WithCheckpoint option")
	}
	wc, err := r.checkMgr.Load(snapshotID)
	if err != nil {
		return nil, fmt.Errorf("resume: %w", err)
	}

	// Continue from the checkpoint node
	currentID := snapshotID
	start := wc.Result.TotalElapsed

	for currentID != "" {
		select {
		case <-ctx.Done():
			wc.Result.Aborted = true
			wc.Result.TotalElapsed = start
			return wc.Result, nil
		default:
		}

		n := r.graph.GetNode(currentID)
		if n == nil {
			return wc.Result, fmt.Errorf("resume: node %q not found", currentID)
		}

		output, err := r.exec.RunNode(ctx, n, wc)
		nr := &types.NodeResult{
			NodeID: currentID, Kind: n.Kind().String(),
			Output: output, Err: err,
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		if err != nil {
			wc.Result.TotalElapsed = start
			return wc.Result, fmt.Errorf("resume node %q: %w", currentID, err)
		}
		if output != "" {
			wc.PrevOutput = output
		}
		currentID = r.graph.Resolve(currentID, wc)
	}

	wc.Result.TotalElapsed = start
	wc.Result.Vars = wc.Vars
	return wc.Result, nil
}

// Graph returns the underlying graph.
func (r *Runner) Graph() *graph.Graph { return r.graph }
