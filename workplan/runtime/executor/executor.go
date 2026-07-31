// Package executor is responsible for actually running nodes.
// It handles template rendering before execution and output processing after.
package executor

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Executor runs individual nodes with pre/post processing.
type Executor struct{}

// New creates an executor.
func New() *Executor {
	return &Executor{}
}

// RunNode executes a node through the only runtime contract: Node.Run.
func (e *Executor) RunNode(ctx context.Context, n node.Node, wc *types.WorkflowContext) (string, error) {
	return e.runNode(ctx, n, wc)
}

// runNode executes a single node with pre-processing (template rendering) and post-processing.
func (e *Executor) runNode(ctx context.Context, n node.Node, wc *types.WorkflowContext) (string, error) {
	// Pre-run: render templates if the node has input configuration
	// (rendering is done by the node itself via RenderTemplate)

	// Execute the node
	if typed, ok := n.(node.ValueNode); ok {
		value, err := typed.RunValue(ctx, wc)
		if err != nil {
			return "", err
		}
		return value.RawString(), nil
	}
	output, err := n.Run(ctx, wc)
	if err != nil {
		return "", err
	}

	// Post-run: normalize output to JSON
	return types.ToJSON(output), nil
}
