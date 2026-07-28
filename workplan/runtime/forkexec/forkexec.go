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

// ForkPolicy controls how a fork responds to branch failures.
type ForkPolicy string

// Policy is retained as a compatibility alias for ForkPolicy.
type Policy = ForkPolicy

const (
	// PolicyFailFast cancels sibling branches and prevents Join after a failure.
	PolicyFailFast Policy = "fail_fast"
	// PolicyBestEffort allows Join to aggregate successful branch results.
	PolicyBestEffort Policy = "best_effort"
)

const (
	// ForkPolicyFailFast is the explicit ForkPolicy name for fail-fast mode.
	ForkPolicyFailFast = PolicyFailFast
	// ForkPolicyBestEffort is the explicit ForkPolicy name for best-effort mode.
	ForkPolicyBestEffort = PolicyBestEffort
)

// JoinPolicy controls which branch results may be merged.
type JoinPolicy string

const (
	// JoinRequireAll rejects a Join whenever any branch does not complete.
	JoinRequireAll JoinPolicy = "require_all"
	// JoinSuccessful merges successful results and preserves failed states.
	JoinSuccessful JoinPolicy = "successful"
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

// ParentSnapshot is an immutable deep copy of a parent workflow context.
// Branch execution never receives a pointer to the mutable parent context.
type ParentSnapshot struct {
	workflow *types.WorkflowContext
}

// NewParentSnapshot freezes parent state for a fork.
func NewParentSnapshot(parent *types.WorkflowContext) ParentSnapshot {
	if parent == nil {
		parent = types.NewWorkflowContext()
	}
	return ParentSnapshot{workflow: parent.Clone()}
}

// CloneWorkflow returns an independent mutable context for one branch.
func (s ParentSnapshot) CloneWorkflow() *types.WorkflowContext {
	if s.workflow == nil {
		return types.NewWorkflowContext()
	}
	return s.workflow.Clone()
}

// BranchContext is exclusively owned by one branch.
type BranchContext struct {
	BranchID string
	Workflow *types.WorkflowContext
	Runtime  BranchRuntime
}

// ContextManager owns the immutable parent snapshot and all context boundary
// operations used by a fork: branch creation and deterministic join merging.
type ContextManager struct {
	parent ParentSnapshot
}

// NewContextManager freezes parent before any branch goroutine starts.
func NewContextManager(parent *types.WorkflowContext) *ContextManager {
	return &ContextManager{parent: NewParentSnapshot(parent)}
}

// ParentSnapshot returns the isolated fork baseline.
func (m *ContextManager) ParentSnapshot() ParentSnapshot {
	if m == nil {
		return NewParentSnapshot(nil)
	}
	return m.parent
}

// NewBranchContext creates an independently owned branch context.
func (m *ContextManager) NewBranchContext(branchID string, runtime BranchRuntime) *BranchContext {
	return &BranchContext{BranchID: branchID, Workflow: m.ParentSnapshot().CloneWorkflow(), Runtime: runtime}
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

// BranchResult is the stable result of one branch execution.
type BranchResult struct {
	ID        string
	NodeID    string
	Output    string
	State     BranchState
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
	Context   *BranchContext
}

// Result is retained as a compatibility alias for BranchResult.
type Result = BranchResult

// ForkCoordinator is the sole executor for automatic and explicit forks. It
// owns cancellation, panic recovery, concurrency limiting, and stable result
// ordering; contexts are supplied exclusively by ContextManager.
type ForkCoordinator struct {
	MaxConcurrent int
	Policy        ForkPolicy
	JoinPolicy    JoinPolicy
	OnEvent       func(Event)
	eventMu       sync.Mutex
}

// Coordinator is retained as a compatibility alias for ForkCoordinator.
type Coordinator = ForkCoordinator

// Run executes all specs through an explicit ContextManager boundary.
// New code should prefer RunWithContextManager when it already owns one.
func (c *ForkCoordinator) Run(ctx context.Context, parent *types.WorkflowContext, specs []Spec) ([]BranchResult, error) {
	return c.RunWithContextManager(ctx, NewContextManager(parent), specs)
}

// RunWithContextManager executes all specs from one frozen parent snapshot.
func (c *ForkCoordinator) RunWithContextManager(ctx context.Context, contexts *ContextManager, specs []Spec) ([]BranchResult, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if contexts == nil {
		return nil, fmt.Errorf("nil context manager")
	}
	specs = append([]Spec(nil), specs...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })

	results := make([]BranchResult, len(specs))
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
			branch := contexts.NewBranchContext(spec.ID, spec.Runtime)
			results[index] = BranchResult{ID: spec.ID, NodeID: spec.NodeID, State: StateQueued, Context: branch}
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
func (c *ForkCoordinator) Join(parent *types.WorkflowContext, results []BranchResult) error {
	return c.JoinWithContextManager(NewContextManager(parent), parent, results)
}

// JoinAggregate always writes a branch-ID keyed aggregate, including for one branch.
func (c *ForkCoordinator) JoinAggregate(parent *types.WorkflowContext, results []BranchResult) error {
	return c.JoinAggregateWithContextManager(NewContextManager(parent), parent, results)
}

// JoinWithContextManager merges scheduler branch results through their context boundary.
func (c *ForkCoordinator) JoinWithContextManager(contexts *ContextManager, parent *types.WorkflowContext, results []BranchResult) error {
	if contexts == nil {
		return fmt.Errorf("nil context manager")
	}
	return contexts.Join(parent, results, c.joinPolicy(), false)
}

// JoinAggregateWithContextManager merges explicit ForkNode results as an aggregate.
func (c *ForkCoordinator) JoinAggregateWithContextManager(contexts *ContextManager, parent *types.WorkflowContext, results []BranchResult) error {
	if contexts == nil {
		return fmt.Errorf("nil context manager")
	}
	return contexts.Join(parent, results, c.joinPolicy(), true)
}

// Join merges results in stable branch-ID order.
func (m *ContextManager) Join(parent *types.WorkflowContext, results []BranchResult, policy JoinPolicy, forceAggregate bool) error {
	if parent == nil {
		return fmt.Errorf("nil parent workflow context")
	}
	results = append([]BranchResult(nil), results...)
	sort.Slice(results, func(i, j int) bool { return results[i].ID < results[j].ID })
	if policy == JoinRequireAll {
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

func (c *ForkCoordinator) maxConcurrent() int {
	if c.MaxConcurrent <= 0 {
		return 3
	}
	return c.MaxConcurrent
}

func (c *ForkCoordinator) policy() ForkPolicy {
	if c.Policy == ForkPolicyBestEffort {
		return ForkPolicyBestEffort
	}
	return ForkPolicyFailFast
}

func (c *ForkCoordinator) joinPolicy() JoinPolicy {
	if c.JoinPolicy == JoinRequireAll || c.JoinPolicy == JoinSuccessful {
		return c.JoinPolicy
	}
	if c.policy() == ForkPolicyBestEffort {
		return JoinSuccessful
	}
	return JoinRequireAll
}

func (c *ForkCoordinator) emit(event Event) {
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
