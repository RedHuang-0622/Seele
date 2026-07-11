// Package graph provides the graph structure that manages nodes and edges.
// It is the central data structure of the workplan runtime.
package graph

import (
	"sync/atomic"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Graph manages nodes and edges with atomic (lock-free) reads/writes.
type Graph struct {
	nodes atomic.Pointer[map[string]node.Node]
	edges atomic.Pointer[[]edge.Edge]
	entry string
}

// New creates an empty graph.
func New() *Graph {
	g := &Graph{}
	g.nodes.Store(&map[string]node.Node{})
	g.edges.Store(&[]edge.Edge{})
	return g
}

// AddNode registers a node (CAS lock-free write).
func (g *Graph) AddNode(n node.Node) {
	for {
		old := g.nodes.Load()
		m := make(map[string]node.Node, len(*old)+1)
		for k, v := range *old {
			m[k] = v
		}
		m[n.ID()] = n
		if g.nodes.CompareAndSwap(old, &m) {
			return
		}
	}
}

// RemoveNode deletes a node by ID (CAS lock-free write).
func (g *Graph) RemoveNode(id string) {
	for {
		old := g.nodes.Load()
		if _, ok := (*old)[id]; !ok {
			return
		}
		m := make(map[string]node.Node, len(*old)-1)
		for k, v := range *old {
			if k != id {
				m[k] = v
			}
		}
		if g.nodes.CompareAndSwap(old, &m) {
			return
		}
	}
}

// GetNode retrieves a node by ID (lock-free read).
func (g *Graph) GetNode(id string) node.Node {
	m := *g.nodes.Load()
	return m[id]
}

// AllNodes returns all node IDs.
func (g *Graph) AllNodes() []string {
	m := *g.nodes.Load()
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}

// AddEdge registers an edge (CAS lock-free write).
func (g *Graph) AddEdge(e edge.Edge) {
	for {
		old := g.edges.Load()
		cp := make([]edge.Edge, len(*old)+1)
		copy(cp, *old)
		cp[len(*old)] = e
		if g.edges.CompareAndSwap(old, &cp) {
			return
		}
	}
}

// GetEdgesFrom returns all edges originating from the given node.
func (g *Graph) GetEdgesFrom(from string) []edge.Edge {
	edges := *g.edges.Load()
	var result []edge.Edge
	for _, e := range edges {
		if e.From == from {
			result = append(result, e)
		}
	}
	return result
}

// AllEdges returns a copy of all edges.
func (g *Graph) AllEdges() []edge.Edge {
	edges := *g.edges.Load()
	cp := make([]edge.Edge, len(edges))
	copy(cp, edges)
	return cp
}

// SetEntry sets the entry node ID.
func (g *Graph) SetEntry(id string) { g.entry = id }

// Entry returns the entry node ID.
func (g *Graph) Entry() string { return g.entry }

// Resolve determines the next node from currentID via edges.
func (g *Graph) Resolve(currentID string, wc *types.WorkflowContext) string {
	edges := *g.edges.Load()
	return edge.Resolve(edges, currentID, wc)
}
