// Package emit provides the Emit() sugar — named variable writer.
package emit

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// EmitNode writes PrevOutput to a named variable.
type EmitNode struct {
	node.BaseNode
	Key string
}

// NewNode creates an emit node.
func NewNode(id, key string) *EmitNode {
	return &EmitNode{BaseNode: node.NewBaseNode(id, node.KindEmit), Key: key}
}

// Run writes PrevOutput to Vars[key] and passes it through.
func (n *EmitNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if wc.Vars != nil {
		wc.Vars[n.Key] = wc.PrevOutput
	}
	if wc.Result != nil {
		wc.Result.Vars = wc.Vars
	}
	return wc.PrevOutput, nil
}

// Add registers an emit node in the graph.
func Add(g *graph.Graph, id, key string) *EmitNode {
	n := NewNode(id, key)
	g.AddNode(n)
	return n
}
