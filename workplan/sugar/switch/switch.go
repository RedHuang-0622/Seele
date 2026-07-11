// Package switchpkg provides the If() and Switch() sugar — conditional branching.
package switchpkg

import (
	"context"
	"strings"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ControlNode is a pass-through node that uses edges for conditional routing.
type ControlNode struct {
	node.BaseNode
}

// NewNode creates a control node for If/Switch routing.
func NewNode(id string, kind node.NodeKind) *ControlNode {
	return &ControlNode{BaseNode: node.NewBaseNode(id, kind)}
}

// Run passes through PrevOutput unchanged.
func (n *ControlNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	return wc.PrevOutput, nil
}

// If adds a binary conditional branch node.
// cond receives the previous node's output (plain text).
func If(g *graph.Graph, id string, cond func(string) bool, trueID, falseID string) *ControlNode {
	n := NewNode(id, node.KindIf)
	g.AddNode(n)
	g.AddEdge(edge.Edge{
		From: id, To: trueID, Priority: 0, Label: "true",
		Condition: func(wc *types.WorkflowContext) bool { return cond(types.FromJSON(wc.PrevOutput)) },
	})
	if falseID != "" {
		g.AddEdge(edge.Edge{
			From: id, To: falseID, Priority: 1, Label: "false",
			Condition: func(wc *types.WorkflowContext) bool {
				return !cond(types.FromJSON(wc.PrevOutput))
			},
		})
	}
	return n
}

// Switch adds a multi-way conditional branch node.
func Switch(g *graph.Graph, id string, cases ...node.SwitchCase) *ControlNode {
	n := NewNode(id, node.KindSwitch)
	g.AddNode(n)
	for i, c := range cases {
		cc := c // capture
		g.AddEdge(edge.Edge{
			From: id, To: cc.NextID, Priority: i, Label: "case",
			Condition: func(wc *types.WorkflowContext) bool {
				if cc.Match == nil {
					return true // default case
				}
				return cc.Match(types.FromJSON(wc.PrevOutput))
			},
		})
	}
	return n
}

// Contains returns a condition function that checks if output contains a substring.
func Contains(substr string) func(string) bool {
	return func(s string) bool {
		return substr != "" && strings.Contains(s, substr)
	}
}

// NotContains returns the inverse of Contains.
func NotContains(substr string) func(string) bool {
	return func(s string) bool {
		return !Contains(substr)(s)
	}
}
