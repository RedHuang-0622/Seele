// Package fork provides the Fork() sugar — concurrent multi-agent execution.
package fork

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
)

// ForkNode executes multiple branches concurrently.
type ForkNode struct {
	node.BaseNode
	Branches      []node.ForkBranch
	MaxConcurrent int
	factory       node.AgentFactory
	DefaultPrompt string
	Policy        forkexec.Policy
	JoinPolicy    forkexec.JoinPolicy
	RuntimeFor    func(node.ForkBranch) forkexec.BranchRuntime
	OnEvent       func(forkexec.Event)
}

// NewNode creates a fork node.
func NewNode(id string, branches []node.ForkBranch, maxConcurrent int, factory node.AgentFactory) *ForkNode {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &ForkNode{
		BaseNode:      node.NewBaseNode(id, node.KindFork),
		Branches:      branches,
		MaxConcurrent: maxConcurrent,
		factory:       factory,
		Policy:        forkexec.PolicyFailFast,
	}
}

// SetPolicy changes fork failure behavior. Best-effort is opt-in only.
func (n *ForkNode) SetPolicy(policy forkexec.Policy) {
	if policy == forkexec.PolicyBestEffort {
		n.Policy = policy
		return
	}
	n.Policy = forkexec.PolicyFailFast
}

// SetJoinPolicy changes explicit fork merge behavior.
func (n *ForkNode) SetJoinPolicy(policy forkexec.JoinPolicy) {
	if policy == forkexec.JoinRequireAll || policy == forkexec.JoinSuccessful {
		n.JoinPolicy = policy
		return
	}
	n.JoinPolicy = ""
}

// SetRuntimeResolver accepts branch-bound Seelex runtime injection.
func (n *ForkNode) SetRuntimeResolver(resolver func(node.ForkBranch) forkexec.BranchRuntime) {
	n.RuntimeFor = resolver
}

// Run executes all branches concurrently and merges results.
func (n *ForkNode) Run(ctx context.Context, ec *types.WorkflowContext) (string, error) {
	specs := make([]forkexec.Spec, 0, len(n.Branches))
	for _, branch := range n.Branches {
		branch := branch
		runtime := forkexec.BranchRuntime{}
		if n.RuntimeFor != nil {
			runtime = n.RuntimeFor(branch)
		}
		specs = append(specs, forkexec.Spec{
			ID: branch.Label, NodeID: branch.Label, Runtime: runtime,
			Execute: func(ctx context.Context, branchCtx *forkexec.BranchContext) (string, error) {
				input := types.RenderTemplate(branch.Input, branchCtx.Workflow)
				prompt := branch.SystemPrompt
				if prompt == "" {
					prompt = n.DefaultPrompt
				}
				if prompt == "" {
					prompt = "You are a helpful assistant."
				}
				factory := n.factory
				if factory == nil {
					return "", fmt.Errorf("nil agent factory")
				}
				agent := factory.NewAgent(prompt)
				if agent == nil {
					return "", fmt.Errorf("nil agent")
				}
				return agent.Chat(ctx, input)
			},
		})
	}

	contexts := forkexec.NewContextManager(ec)
	coordinator := forkexec.ForkCoordinator{MaxConcurrent: n.MaxConcurrent, Policy: n.Policy, JoinPolicy: n.JoinPolicy, OnEvent: n.OnEvent}
	results, runErr := coordinator.RunWithContextManager(ctx, contexts, specs)
	for _, result := range results {
		nr := &types.NodeResult{NodeBase: types.NodeBase{
			NodeID: result.ID, Kind: node.KindFork.String(), Status: string(result.State),
			Output: result.Output, StartedAt: result.StartedAt, EndedAt: result.EndedAt,
		}, Err: result.Err}
		ec.Result.NodeResults = append(ec.Result.NodeResults, nr)
	}
	if runErr != nil {
		return "", runErr
	}
	if err := coordinator.JoinAggregateWithContextManager(contexts, ec, results); err != nil {
		return "", err
	}
	return ec.PrevOutput, nil
}

// Add registers a fork node in the graph.
func Add(g *coreplan.Plan, id string, branches []node.ForkBranch, maxConcurrent int, factory node.AgentFactory) *ForkNode {
	n := NewNode(id, branches, maxConcurrent, factory)
	g.AddNode(n)
	return n
}
