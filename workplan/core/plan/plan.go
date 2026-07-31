// Package plan owns the executable WorkPlan kernel: node, edge, and entry
// state. It deliberately has no dependency on graph views, scheduling, or UI.
package plan

import (
	"sync/atomic"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Plan is the concurrency-safe executable definition of a WorkPlan.
type Plan struct {
	nodes atomic.Pointer[map[string]node.Node]
	edges atomic.Pointer[[]edge.Edge]
	entry atomic.Pointer[string]
}

// New creates an empty executable plan.
func New() *Plan {
	p := &Plan{}
	p.nodes.Store(&map[string]node.Node{})
	p.edges.Store(&[]edge.Edge{})
	entry := ""
	p.entry.Store(&entry)
	return p
}

// AddNode registers or replaces a node by ID.
func (p *Plan) AddNode(n node.Node) {
	for {
		old := p.nodes.Load()
		updated := make(map[string]node.Node, len(*old)+1)
		for id, existing := range *old {
			updated[id] = existing
		}
		updated[n.ID()] = n
		if p.nodes.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// RemoveNode deletes a node by ID.
func (p *Plan) RemoveNode(id string) {
	for {
		old := p.nodes.Load()
		if _, exists := (*old)[id]; !exists {
			return
		}
		updated := make(map[string]node.Node, len(*old)-1)
		for existingID, existing := range *old {
			if existingID != id {
				updated[existingID] = existing
			}
		}
		if p.nodes.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// GetNode returns the node with id, if any.
func (p *Plan) GetNode(id string) node.Node {
	return (*p.nodes.Load())[id]
}

// AllNodes returns a snapshot of every registered node ID.
func (p *Plan) AllNodes() []string {
	nodes := *p.nodes.Load()
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	return ids
}

// AddEdge adds a directed edge to the plan.
func (p *Plan) AddEdge(e edge.Edge) {
	for {
		old := p.edges.Load()
		updated := make([]edge.Edge, len(*old)+1)
		copy(updated, *old)
		updated[len(*old)] = e
		if p.edges.CompareAndSwap(old, &updated) {
			return
		}
	}
}

// GetEdgesFrom returns a snapshot of edges leaving from.
func (p *Plan) GetEdgesFrom(from string) []edge.Edge {
	all := *p.edges.Load()
	result := make([]edge.Edge, 0)
	for _, candidate := range all {
		if candidate.From == from {
			result = append(result, candidate)
		}
	}
	return result
}

// AllEdges returns a snapshot of every edge.
func (p *Plan) AllEdges() []edge.Edge {
	all := *p.edges.Load()
	result := make([]edge.Edge, len(all))
	copy(result, all)
	return result
}

// SetEntry sets the entry node ID.
func (p *Plan) SetEntry(id string) {
	entry := id
	p.entry.Store(&entry)
}

// Entry returns the entry node ID.
func (p *Plan) Entry() string { return *p.entry.Load() }

// Resolve selects the next node from currentID according to the plan's edges.
func (p *Plan) Resolve(currentID string, wc *types.WorkflowContext) string {
	return edge.Resolve(*p.edges.Load(), currentID, wc)
}

// GetNextNodes returns every unconditional successor. If there are no
// unconditional successors, it returns the first matching conditional one.
func (p *Plan) GetNextNodes(currentID string, wc *types.WorkflowContext) []string {
	all := *p.edges.Load()
	ids := make([]string, 0)
	for _, candidate := range all {
		if candidate.From == currentID && candidate.Condition == nil {
			ids = append(ids, candidate.To)
		}
	}
	if len(ids) > 0 {
		return ids
	}
	if next := edge.Resolve(all, currentID, wc); next != "" {
		return []string{next}
	}
	return nil
}
