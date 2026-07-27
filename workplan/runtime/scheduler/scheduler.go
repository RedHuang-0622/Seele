// Package scheduler orchestrates dependency-aware WorkPlan execution.
package scheduler

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// Scheduler drives dependency-aware execution of graph nodes.
type Scheduler struct {
	graph              *graph.Graph
	executor           *executor.Executor
	MaxForkConcurrency int
	ForkPolicy         forkexec.Policy
	RuntimeFor         func(branchID string) forkexec.BranchRuntime
	OnNodeDone         func(nr *types.NodeResult)
	OnBranchEvent      func(forkexec.Event)
}

// SetNodeHook sets the per-node completion callback.
func (s *Scheduler) SetNodeHook(hook func(nr *types.NodeResult)) {
	s.OnNodeDone = hook
}

// SetBranchEventHook sets the observable branch lifecycle callback.
func (s *Scheduler) SetBranchEventHook(hook func(forkexec.Event)) {
	s.OnBranchEvent = hook
}

// SetMaxForkConcurrency limits concurrently running branches in automatic forks.
func (s *Scheduler) SetMaxForkConcurrency(maxConcurrent int) {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	s.MaxForkConcurrency = maxConcurrent
}

// SetForkPolicy configures automatic fork failure behavior.
// Fail-fast is the default; best-effort must be explicitly requested.
func (s *Scheduler) SetForkPolicy(policy forkexec.Policy) {
	if policy == forkexec.PolicyBestEffort {
		s.ForkPolicy = policy
		return
	}
	s.ForkPolicy = forkexec.PolicyFailFast
}

// SetBranchRuntimeResolver accepts read-only Seelex branch runtime injection.
func (s *Scheduler) SetBranchRuntimeResolver(resolver func(string) forkexec.BranchRuntime) {
	s.RuntimeFor = resolver
}

// New creates a scheduler bound to a graph and executor.
func New(g *graph.Graph, exec *executor.Executor) *Scheduler {
	return &Scheduler{
		graph: g, executor: exec, MaxForkConcurrency: 3, ForkPolicy: forkexec.PolicyFailFast,
	}
}

// Run executes the graph using dependency counts for every activated edge.
func (s *Scheduler) Run(ctx context.Context) (*types.WorkPlanResult, error) {
	result, _, err := s.run(ctx, false)
	return result, err
}

// RunWithCheckpoint executes the graph and returns a snapshot for each completed node.
func (s *Scheduler) RunWithCheckpoint(ctx context.Context) (*types.WorkPlanResult, map[string]*types.Snapshot, error) {
	return s.run(ctx, true)
}

func (s *Scheduler) run(ctx context.Context, captureCheckpoints bool) (*types.WorkPlanResult, map[string]*types.Snapshot, error) {
	wc := types.NewWorkflowContext()
	checkpoints := make(map[string]*types.Snapshot)
	start := time.Now()
	entry := s.graph.Entry()
	if entry == "" {
		return wc.Result, checkpoints, nil
	}

	dependencies := s.dependencies()
	completedDeps := make(map[string]int)
	ready := []string{entry}
	queued := map[string]bool{entry: true}
	upstreamOutput := make(map[string]string)

	for len(ready) > 0 {
		select {
		case <-ctx.Done():
			wc.Result.Aborted = true
			wc.Result.AbortReason = ctx.Err().Error()
			wc.Result.TotalElapsed = time.Since(start)
			wc.Result.Vars = wc.Vars
			return wc.Result, checkpoints, nil
		default:
		}

		sort.Strings(ready)
		specs := make([]forkexec.Spec, 0, len(ready))
		for _, nodeID := range ready {
			nodeID := nodeID
			n := s.graph.GetNode(nodeID)
			runtime := forkexec.BranchRuntime{}
			if s.RuntimeFor != nil {
				runtime = s.RuntimeFor(nodeID)
			}
			specs = append(specs, forkexec.Spec{
				ID: nodeID, NodeID: nodeID, Runtime: runtime,
				Prepare: func(branch *forkexec.BranchContext) {
					if dependencies[nodeID] == 1 {
						if output, ok := upstreamOutput[nodeID]; ok {
							branch.Workflow.PrevOutput = output
						}
					}
				},
				Execute: func(branchCtx context.Context, branch *forkexec.BranchContext) (string, error) {
					if n == nil {
						return "", fmt.Errorf("node %q not found", nodeID)
					}
					return s.executor.RunNode(branchCtx, n, branch.Workflow)
				},
			})
		}

		coordinator := forkexec.Coordinator{
			MaxConcurrent: s.MaxForkConcurrency,
			Policy:        s.ForkPolicy,
			OnEvent:       s.OnBranchEvent,
		}
		results, runErr := coordinator.Run(ctx, wc, specs)
		s.recordResults(wc, results, captureCheckpoints, checkpoints)
		if runErr != nil && s.ForkPolicy != forkexec.PolicyBestEffort {
			wc.Result.TotalElapsed = time.Since(start)
			wc.Result.Vars = wc.Vars
			return wc.Result, checkpoints, runErr
		}
		if err := coordinator.Join(wc, results); err != nil {
			wc.Result.TotalElapsed = time.Since(start)
			wc.Result.Vars = wc.Vars
			return wc.Result, checkpoints, err
		}

		nextReady := make([]string, 0)
		for _, result := range results {
			if result.State != forkexec.StateCompleted {
				continue
			}
			nextIDs := s.graph.GetNextNodes(result.NodeID, result.Context.Workflow)
			for _, nextID := range nextIDs {
				completedDeps[nextID]++
				if dependencies[nextID] == 1 {
					upstreamOutput[nextID] = result.Output
				}
				if completedDeps[nextID] == dependencies[nextID] && !queued[nextID] {
					queued[nextID] = true
					nextReady = append(nextReady, nextID)
				}
			}
		}
		ready = nextReady
	}

	wc.Result.TotalElapsed = time.Since(start)
	wc.Result.Vars = wc.Vars
	return wc.Result, checkpoints, nil
}

func (s *Scheduler) dependencies() map[string]int {
	counts := make(map[string]int)
	for _, edge := range s.graph.AllEdges() {
		counts[edge.To]++
	}
	return counts
}

func (s *Scheduler) recordResults(wc *types.WorkflowContext, results []forkexec.Result, captureCheckpoints bool, checkpoints map[string]*types.Snapshot) {
	for _, result := range results {
		n := s.graph.GetNode(result.NodeID)
		kind := "unknown"
		if n != nil {
			kind = n.Kind().String()
		}
		nr := &types.NodeResult{NodeBase: types.NodeBase{
			NodeID: result.NodeID, Kind: kind, Status: string(result.State), Output: result.Output,
			StartedAt: result.StartedAt, EndedAt: result.EndedAt,
		}, Err: result.Err}
		wc.Result.NodeResults = append(wc.Result.NodeResults, nr)
		if s.OnNodeDone != nil {
			s.OnNodeDone(nr)
		}
		if captureCheckpoints && result.State == forkexec.StateCompleted {
			checkpoints[result.NodeID] = &types.Snapshot{
				NodeID: result.NodeID, Context: result.Context.Workflow.Clone(), Timestamp: result.EndedAt, Status: types.StatusRunning,
			}
		}
	}
}
