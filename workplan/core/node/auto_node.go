package node

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

const defaultAutoSystemPrompt = "You are a helpful assistant."

// AutoNode is the executable primitive behind a declarative auto node.
// Its Input is rendered against the workflow context before invoking an agent.
type AutoNode struct {
	BaseNode
	factory AgentFactory
	input   string
}

// NewAutoNode creates an auto node backed by an AgentFactory.
func NewAutoNode(id string, factory AgentFactory, input string) *AutoNode {
	return &AutoNode{
		BaseNode: NewBaseNode(id, KindAuto),
		factory:  factory,
		input:    input,
	}
}

// DSLInput returns the declarative task input without rendering it.
func (n *AutoNode) DSLInput() string { return n.input }

// Run executes the node using its construction-time AgentFactory.
func (n *AutoNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	return n.run(ctx, wc, n.factory)
}

// RunWithAgentFactory executes the node with a branch-bound factory when one
// is supplied by the scheduler.
func (n *AutoNode) RunWithAgentFactory(ctx context.Context, wc *types.WorkflowContext, factory AgentFactory) (string, error) {
	if factory == nil {
		factory = n.factory
	}
	return n.run(ctx, wc, factory)
}

func (n *AutoNode) run(ctx context.Context, wc *types.WorkflowContext, factory AgentFactory) (string, error) {
	if factory == nil {
		return "", fmt.Errorf("auto node %q: agent factory is nil", n.ID())
	}
	input := types.RenderTemplate(n.input, wc)
	agt := factory.NewAgent(defaultAutoSystemPrompt)
	if agt == nil {
		return "", fmt.Errorf("auto node %q: agent factory returned nil", n.ID())
	}
	return agt.Chat(ctx, input)
}

var _ InputNode = (*AutoNode)(nil)
