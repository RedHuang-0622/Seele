// Package edge defines the Edge structure and routing logic.
// Edges connect nodes in the graph and determine the next node to execute.
package edge

import (
	"sort"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Edge is a directed edge connecting two nodes.
// The Condition field uses types.EdgeCondition (defined in core/types).
type Edge struct {
	From      string
	To        string
	Condition types.EdgeCondition // nil = unconditional
	Priority  int                 // lower number = higher priority
	Label     string              // debug/serialization label
}

// Resolve determines the next node ID from currentID based on edges.
// Rules:
//  1. Unconditional edges (Condition == nil) are matched first
//  2. If no unconditional edge, conditional edges are sorted by Priority
//  3. The first condition that returns true wins
//  4. If no edge matches, returns "" (graph ends)
func Resolve(edges []Edge, currentID string, wc *types.WorkflowContext) string {
	var candidates []Edge
	for _, e := range edges {
		if e.From == currentID {
			candidates = append(candidates, e)
		}
	}
	// Unconditional edges first
	for _, e := range candidates {
		if e.Condition == nil {
			return e.To
		}
	}
	// Conditional edges sorted by priority
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Priority < candidates[j].Priority
	})
	for _, e := range candidates {
		if e.Condition(wc) {
			return e.To
		}
	}
	return ""
}

// HasEdgeFrom checks if any edge originates from the given node.
func HasEdgeFrom(edges []Edge, from string) bool {
	for _, e := range edges {
		if e.From == from {
			return true
		}
	}
	return false
}

// HasEdgeTo checks if any edge terminates at the given node.
func HasEdgeTo(edges []Edge, to string) bool {
	for _, e := range edges {
		if e.To == to {
			return true
		}
	}
	return false
}
