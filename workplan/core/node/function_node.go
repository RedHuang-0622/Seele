package node

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// FunctionNode wraps a plain Go function as a node.
type FunctionNode struct {
	BaseNode
	fn func(ctx context.Context, input string) (string, error)
}

// NewFunctionNode creates a function node.
func NewFunctionNode(id string, fn func(ctx context.Context, input string) (string, error)) *FunctionNode {
	return &FunctionNode{
		BaseNode: NewBaseNode(id, KindMethod),
		fn:       fn,
	}
}

// Run executes the wrapped function with PrevOutput as input.
func (n *FunctionNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	out, err := n.fn(ctx, wc.PrevOutput)
	if err != nil {
		return "", err
	}
	return types.ToJSON(out), nil
}
