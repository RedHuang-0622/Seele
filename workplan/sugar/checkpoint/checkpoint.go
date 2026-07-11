// Package checkpoint provides the Checkpoint() sugar — snapshot writer.
package checkpoint

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// CheckpointNode records the current output as a checkpoint.
type CheckpointNode struct {
	node.BaseNode
}

// NewNode creates a checkpoint node.
func NewNode(id string) *CheckpointNode {
	return &CheckpointNode{BaseNode: node.NewBaseNode(id, node.KindCheckpoint)}
}

// Run records PrevOutput to Result.Checkpoints.
func (n *CheckpointNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if wc.Result != nil && wc.Result.Checkpoints != nil {
		wc.Result.Checkpoints[n.ID()] = wc.PrevOutput
	}
	return wc.PrevOutput, nil
}

// Add registers a checkpoint node in the graph.
func Add(g *graph.Graph, id string) *CheckpointNode {
	n := NewNode(id)
	g.AddNode(n)
	return n
}
