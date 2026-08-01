// Package runner is the top-level entry point for workflow execution.
// It provides Run() for fresh execution and Resume() for checkpoint recovery.
package runner

import (
	"context"
	"fmt"
	"time"

	seeleerrors "github.com/RedHuang-0622/Seele/errors"
	frameworkevent "github.com/RedHuang-0622/Seele/event"
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
	plan        *coreplan.Plan
	sched       *scheduler.Scheduler
	exec        *executor.Executor
	checkMgr    *checkpoint.Manager
	eventConfig EventConfig
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
	runCtx, recorder, err := r.startEvents(ctx)
	if err != nil {
		return nil, seeleerrors.Wrap(err, seeleerrors.Context{
			Code: "workplan.runner.event", Struct: "runner.Runner", Function: "Run", Step: "event",
		})
	}
	if recorder != nil {
		defer recorder.Close()
	}
	publishPlanStart(runCtx, recorder)
	if err := validate.Plan(r.plan); err != nil {
		wrapped := seeleerrors.Wrap(err, seeleerrors.Context{
			Code: "workplan.runner.validate", Struct: "runner.Runner", Function: "Run", Step: "validate",
		})
		publishPlanEnd(runCtx, recorder, false, wrapped)
		return nil, wrapped
	}
	result, runErr := r.sched.Run(runCtx)
	publishPlanEnd(runCtx, recorder, result != nil && result.Aborted, runErr)
	return result, runErr
}

// Resume continues execution from a saved checkpoint.
func (r *Runner) Resume(ctx context.Context, snapshotID string) (*types.WorkPlanResult, error) {
	runCtx, recorder, eventErr := r.startEvents(ctx)
	if eventErr != nil {
		return nil, seeleerrors.Wrap(eventErr, seeleerrors.Context{
			Code: "workplan.runner.event", Struct: "runner.Runner", Function: "Resume", Step: "event",
		})
	}
	if recorder != nil {
		defer recorder.Close()
	}
	publishPlanStart(runCtx, recorder)
	if r.checkMgr == nil {
		err := seeleerrors.New("workplan.runner.checkpoint", "checkpoint not enabled: use WithCheckpoint option")
		publishPlanEnd(runCtx, recorder, false, err)
		return nil, err
	}
	wc, err := r.checkMgr.Load(snapshotID)
	if err != nil {
		wrapped := seeleerrors.Wrap(err, seeleerrors.Context{
			Code: "workplan.runner.resume", Struct: "runner.Runner", Function: "Resume", Step: "load", Raw: snapshotID,
		})
		publishPlanEnd(runCtx, recorder, false, wrapped)
		return nil, wrapped
	}

	// Continue from the checkpoint node
	currentID := snapshotID
	start := wc.Result.TotalElapsed

	for currentID != "" {
		select {
		case <-runCtx.Done():
			wc.Result.Aborted = true
			wc.Result.TotalElapsed = start
			publishPlanEnd(runCtx, recorder, true, nil)
			return wc.Result, nil
		default:
		}

		n := r.plan.GetNode(currentID)
		if n == nil {
			err := seeleerrors.New("workplan.runner.node", fmt.Sprintf("node %q not found", currentID))
			publishPlanEnd(runCtx, recorder, false, err)
			return wc.Result, err
		}

		startedAt := time.Now()
		if recorder != nil {
			recorder.Publish(runCtx, frameworkevent.Event{
				Source: "workplan.runner", Type: frameworkevent.TypeLifecycle, Status: frameworkevent.StatusRunning,
				Scope:     frameworkevent.Scope{NodeID: currentID},
				Locations: []frameworkevent.Location{{Kind: "workplan.node", IDs: map[string]string{"node_id": currentID}}},
			})
		}
		lease := frameworkevent.HeartbeatLease(nil)
		if recorder != nil {
			lease = recorder.StartHeartbeat(runCtx, frameworkevent.Scope{NodeID: currentID}, map[string]string{"event_source": "workplan.runner"}, frameworkevent.LocatorFunc(func() frameworkevent.Location {
				return frameworkevent.Location{Kind: "workplan.node", IDs: map[string]string{"node_id": currentID}}
			}))
		}
		output, err := r.exec.RunNode(runCtx, n, wc)
		if lease != nil {
			lease.Stop()
		}
		endedAt := time.Now()
		kind := "unknown"
		if described, ok := n.(node.Kinded); ok {
			kind = described.Kind().String()
		}
		nr := &types.NodeResult{
			NodeBase: types.NodeBase{
				NodeID: currentID,
				Kind:   kind,
				Output: output,
				Status: func() string {
					if err != nil {
						return string(frameworkevent.StatusFailed)
					}
					return string(frameworkevent.StatusCompleted)
				}(),
				StartedAt: startedAt,
				EndedAt:   endedAt,
			},
			Err: err,
		}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		// Record output for multi-upstream reference via {{.PrevResults.nodeID}}
		if output != "" {
			wc.SetResultRaw(currentID, output)
			nodeValue := types.RawValue(output)
			nr.Value = &nodeValue
		}
		if err != nil {
			wc.Result.TotalElapsed = start
			wrapped := seeleerrors.Wrap(err, seeleerrors.Context{
				Code: "workplan.runner.node", Struct: "runner.Runner", Function: "Resume", Step: currentID, Raw: currentID,
			})
			if recorder != nil {
				recorder.Publish(runCtx, frameworkevent.Event{
					Source: "workplan.runner", Type: frameworkevent.TypeLifecycle, Status: frameworkevent.StatusFailed,
					Scope: frameworkevent.Scope{NodeID: currentID}, Failure: frameworkevent.FailureFrom(wrapped),
				})
			}
			publishPlanEnd(runCtx, recorder, false, wrapped)
			return wc.Result, wrapped
		}
		if recorder != nil {
			recorder.Publish(runCtx, frameworkevent.Event{
				Source: "workplan.runner", Type: frameworkevent.TypeLifecycle, Status: frameworkevent.StatusCompleted,
				Scope: frameworkevent.Scope{NodeID: currentID}, Content: []byte(types.ToJSON(output)),
			})
		}
		if output != "" {
			wc.SetPrevRaw(output)
		}
		currentID = r.plan.Resolve(currentID, wc)
	}

	wc.Result.TotalElapsed = start
	wc.Result.Vars = wc.Vars
	publishPlanEnd(runCtx, recorder, false, nil)
	return wc.Result, nil
}

// Plan returns the underlying WorkPlan kernel.
func (r *Runner) Plan() *coreplan.Plan { return r.plan }
