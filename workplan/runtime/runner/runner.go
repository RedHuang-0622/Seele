// Package runner is the top-level entry point for workflow execution.
// It provides Run() for fresh execution and Resume() for checkpoint recovery.
package runner

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/checkpoint"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/scheduler"
	"github.com/RedHuang-0622/Seele/workplan/runtime/validate"
)

// Runner is the entry point for workflow execution.
type Runner struct {
	plan     *coreplan.Plan
	sched    *scheduler.Scheduler
	exec     *executor.Executor
	checkMgr *checkpoint.Manager
}

// Option configures the runner.
type Option func(*Runner)

// New creates a runner from a Plan. Node implementations own all execution
// dependencies; Runner receives no agent or tool factory.
func New(p *coreplan.Plan, opts ...Option) *Runner {
	exec := executor.New()
	sched := scheduler.New(p, exec)
	r := &Runner{
		plan:  p,
		sched: sched,
		exec:  exec,
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
	if err := validate.Plan(r.plan); err != nil {
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

		n := r.plan.GetNode(currentID)
		if n == nil {
			return wc.Result, fmt.Errorf("resume: node %q not found", currentID)
		}

		output, err := r.exec.RunNode(ctx, n, wc)
		kind := "unknown"
		if described, ok := n.(node.Kinded); ok {
			kind = described.Kind().String()
		}
		nr := &types.NodeResult{
			NodeBase: types.NodeBase{
				NodeID: currentID,
				Kind:   kind,
				Output: output,
			},
			Err: err,
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		// Record output for multi-upstream reference via {{.PrevResults.nodeID}}
		if output != "" {
			wc.PrevResults[currentID] = output
		}
		if err != nil {
			wc.Result.TotalElapsed = start
			return wc.Result, fmt.Errorf("resume node %q: %w", currentID, err)
		}
		if output != "" {
			wc.PrevOutput = output
		}
		currentID = r.plan.Resolve(currentID, wc)
	}

	wc.Result.TotalElapsed = start
	wc.Result.Vars = wc.Vars
	return wc.Result, nil
}

// Plan returns the underlying WorkPlan kernel.
func (r *Runner) Plan() *coreplan.Plan { return r.plan }
