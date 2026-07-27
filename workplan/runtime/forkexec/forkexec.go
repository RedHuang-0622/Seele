// Package forkexec coordinates isolated parallel branch execution.
package forkexec

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Policy controls how a fork responds to branch failures.
type Policy string

const (
	// PolicyFailFast cancels sibling branches and prevents Join after a failure.
	PolicyFailFast Policy = "fail_fast"
	// PolicyBestEffort allows Join to aggregate successful branch results.
	PolicyBestEffort Policy = "best_effort"
)

// BranchState describes a branch lifecycle state.
type BranchState string

const (
	StateQueued    BranchState = "queued"
	StateStarted   BranchState = "started"
	StateCompleted BranchState = "completed"
	StateFailed    BranchState = "failed"
	StateCanceled  BranchState = "canceled"
	StatePanicked  BranchState = "panicked"
)

// Limiter is an optional externally supplied branch limiter.
// Seele accepts it as an injection point and does not load Seelex configuration.
type Limiter interface {
	Acquire(context.Context) error
	Release()
}

// BranchRuntime is read-only metadata injected by Seelex for a branch.
type BranchRuntime struct {
	SessionID    string
	WorkspaceID  string
	Role         string
	AccountID    string
	Provider     string
	TraceID      string
	AgentFactory node.AgentFactory
	Limiter      Limiter
}

// BranchContext is exclusively owned by one branch.
type BranchContext struct {
	Workflow *types.WorkflowContext
	Runtime  BranchRuntime
}

// Event is emitted for each observable branch lifecycle transition.
type Event struct {
	Type     BranchState
	BranchID string
	NodeID   string
	Err      error
	At       time.Time
}

// Spec defines a branch to execute.
type Spec struct {
	ID      string
	NodeID  string
	Runtime BranchRuntime
	Prepare func(*BranchContext)
	Execute func(context.Context, *BranchContext) (string, error)
}

// Result is the stable result of one branch execution.
type Result struct {
	ID        string
	NodeID    string
	Output    string
	State     BranchState
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
	Context   *BranchContext
}

// Coordinator executes parallel branches and owns cancellation, panic recovery,
// concurrency limiting, and stable result ordering.
type Coordinator struct {
	MaxConcurrent int
	Policy        Policy
	OnEvent       func(Event)
	eventMu       sync.Mutex
}

// Run executes all specs from a frozen parent snapshot.
func (c *Coordinator) Run(ctx context.Context, parent *types.WorkflowContext, specs []Spec) ([]Result, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	specs = append([]Spec(nil), specs...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })

	snapshot := parent.Clone()
	results := make([]Result, len(specs))
	forkCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	sem := make(chan struct{}, c.maxConcurrent())
	var wg sync.WaitGroup
	var failure struct {
		sync.Mutex
		err error
	}

	recordFailure := func(err error) {
		if err == nil || c.policy() == PolicyBestEffort {
			return
		}
		failure.Lock()
		if failure.err == nil {
			failure.err = err
			cancel(err)
		}
		failure.Unlock()
	}

	for index, spec := range specs {
		c.emit(Event{Type: StateQueued, BranchID: spec.ID, NodeID: spec.NodeID, At: time.Now()})
		wg.Add(1)
		go func(index int, spec Spec) {
			defer wg.Done()
			branch := &BranchContext{Workflow: snapshot.Clone(), Runtime: spec.Runtime}
			results[index] = Result{ID: spec.ID, NodeID: spec.NodeID, State: StateQueued, Context: branch}
			if spec.Prepare != nil {
				spec.Prepare(branch)
			}

			if err := acquire(forkCtx, sem, spec.Runtime.Limiter); err != nil {
				results[index].State = StateCanceled
				results[index].Err = err
				results[index].EndedAt = time.Now()
				c.emit(Event{Type: StateCanceled, BranchID: spec.ID, NodeID: spec.NodeID, Err: err, At: results[index].EndedAt})
				return
			}
			defer release(sem, spec.Runtime.Limiter)

			results[index].State = StateStarted
			results[index].StartedAt = time.Now()
			c.emit(Event{Type: StateStarted, BranchID: spec.ID, NodeID: spec.NodeID, At: results[index].StartedAt})
			defer func() {
				if recovered := recover(); recovered != nil {
					err := fmt.Errorf("panic: %v", recovered)
					results[index].State = StatePanicked
					results[index].Err = err
					results[index].EndedAt = time.Now()
					c.emit(Event{Type: StatePanicked, BranchID: spec.ID, NodeID: spec.NodeID, Err: err, At: results[index].EndedAt})
					recordFailure(err)
				}
			}()

			output, err := spec.Execute(forkCtx, branch)
			results[index].Output = types.ToJSON(output)
			results[index].EndedAt = time.Now()
			if err != nil {
				if forkCtx.Err() != nil && c.policy() == PolicyFailFast {
					results[index].State = StateCanceled
					results[index].Err = context.Cause(forkCtx)
					c.emit(Event{Type: StateCanceled, BranchID: spec.ID, NodeID: spec.NodeID, Err: results[index].Err, At: results[index].EndedAt})
					return
				}
				results[index].State = StateFailed
				results[index].Err = err
				c.emit(Event{Type: StateFailed, BranchID: spec.ID, NodeID: spec.NodeID, Err: err, At: results[index].EndedAt})
				recordFailure(err)
				return
			}

			branch.Workflow.PrevOutput = results[index].Output
			branch.Workflow.PrevResults[spec.ID] = results[index].Output
			results[index].State = StateCompleted
			c.emit(Event{Type: StateCompleted, BranchID: spec.ID, NodeID: spec.NodeID, At: results[index].EndedAt})
		}(index, spec)
	}
	wg.Wait()

	failure.Lock()
	err := failure.err
	failure.Unlock()
	return results, err
}

// Join writes successful results to the parent execution flow in stable order.
func (c *Coordinator) Join(parent *types.WorkflowContext, results []Result) error {
	return c.join(parent, results, false)
}

// JoinAggregate always writes a branch-ID keyed aggregate, including for one branch.
func (c *Coordinator) JoinAggregate(parent *types.WorkflowContext, results []Result) error {
	return c.join(parent, results, true)
}

func (c *Coordinator) join(parent *types.WorkflowContext, results []Result, forceAggregate bool) error {
	results = append([]Result(nil), results...)
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if c.policy() == PolicyFailFast {
		for _, result := range results {
			if result.State != StateCompleted {
				return fmt.Errorf("fork branch %q: %w", result.ID, result.Err)
			}
		}
	}

	merged := make(map[string]any, len(results))
	successes := 0
	for _, result := range results {
		if result.State != StateCompleted {
			merged[result.ID] = nil
			continue
		}
		successes++
		parent.PrevResults[result.ID] = result.Output
		var value any
		if json.Unmarshal([]byte(result.Output), &value) == nil {
			merged[result.ID] = value
		} else {
			merged[result.ID] = result.Output
		}
	}
	if successes == 1 && len(results) == 1 && !forceAggregate {
		branch := results[0].Context.Workflow.Clone()
		parent.PrevOutput = branch.PrevOutput
		parent.PrevResults = branch.PrevResults
		parent.Vars = branch.Vars
		parent.Metadata = branch.Metadata
		return nil
	}
	output, _ := json.Marshal(merged)
	parent.PrevOutput = string(output)
	return nil
}

func (c *Coordinator) maxConcurrent() int {
	if c.MaxConcurrent <= 0 {
		return 3
	}
	return c.MaxConcurrent
}

func (c *Coordinator) policy() Policy {
	if c.Policy == PolicyBestEffort {
		return PolicyBestEffort
	}
	return PolicyFailFast
}

func (c *Coordinator) emit(event Event) {
	if c.OnEvent != nil {
		c.eventMu.Lock()
		defer c.eventMu.Unlock()
		c.OnEvent(event)
	}
}

func acquire(ctx context.Context, sem chan struct{}, limiter Limiter) error {
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	if limiter == nil {
		return nil
	}
	if err := limiter.Acquire(ctx); err != nil {
		<-sem
		return err
	}
	return nil
}

func release(sem chan struct{}, limiter Limiter) {
	if limiter != nil {
		limiter.Release()
	}
	<-sem
}
