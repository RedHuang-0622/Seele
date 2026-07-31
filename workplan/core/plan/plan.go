// Package plan owns the executable WorkPlan kernel: node, edge, and entry
// state. It deliberately has no dependency on graph views, scheduling, or UI.
package plan

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Plan is the concurrency-safe executable definition of a WorkPlan.
type Plan struct {
	mutationMu sync.Mutex
	nodes      atomic.Pointer[map[string]node.Node]
	edges      atomic.Pointer[[]edge.Edge]
	entry      atomic.Pointer[string]
	sealed     atomic.Bool
}

// ErrSealed indicates that a plan has entered its immutable execution phase.
var ErrSealed = fmt.Errorf("plan is sealed")

// Definition is the dependency-free input to Build.
type Definition struct {
	Nodes []node.Node
	Edges []edge.Edge
	Entry string
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

// Build creates a Plan from nodes, edges, and an entry, validates its topology
// and seals it for execution. Callers that need incremental construction can
// use New, mutate it, then call Seal explicitly.
func Build(def Definition) (*Plan, error) {
	p := New()
	for _, n := range def.Nodes {
		p.AddNode(n)
	}
	for _, e := range def.Edges {
		p.AddEdge(e)
	}
	p.SetEntry(def.Entry)
	if err := p.Seal(); err != nil {
		return nil, err
	}
	return p, nil
}

// IsSealed reports whether the plan is immutable.
func (p *Plan) IsSealed() bool { return p != nil && p.sealed.Load() }

// Seal validates the topology and transitions the plan to immutable state.
func (p *Plan) Seal() error {
	if p == nil {
		return fmt.Errorf("nil plan")
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.sealed.Load() {
		return nil
	}
	if err := p.Validate(); err != nil {
		return err
	}
	p.sealed.Store(true)
	return nil
}

// Validate checks node IDs, edge references, entry, reachability, and cycles.
// It intentionally stays in core/plan so runtime validation is optional.
func (p *Plan) Validate() error {
	if p == nil {
		return fmt.Errorf("nil plan")
	}
	nodes := *p.nodes.Load()
	ids := make([]string, 0, len(nodes))
	for id, n := range nodes {
		if n == nil {
			return fmt.Errorf("node %q is nil", id)
		}
		if id == "" || n.ID() == "" {
			return fmt.Errorf("node ID must not be empty")
		}
		if n.ID() != id {
			return fmt.Errorf("node map key %q does not match node ID %q", id, n.ID())
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entry := p.Entry()
	if entry != "" {
		if _, ok := nodes[entry]; !ok {
			return fmt.Errorf("entry node %q not found", entry)
		}
	}
	adj := make(map[string][]string, len(nodes))
	for _, id := range ids {
		adj[id] = nil
	}
	for i, e := range p.AllEdges() {
		if _, ok := nodes[e.From]; !ok {
			return fmt.Errorf("edge[%d] from %q: node not found", i, e.From)
		}
		if _, ok := nodes[e.To]; !ok {
			return fmt.Errorf("edge[%d] to %q: node not found", i, e.To)
		}
		if e.From == e.To {
			return fmt.Errorf("edge[%d] %q -> %q creates a cycle", i, e.From, e.To)
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	state := make(map[string]uint8, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		state[id] = 1
		for _, next := range adj[id] {
			switch state[next] {
			case 1:
				return fmt.Errorf("cycle detected through edge %q -> %q", id, next)
			case 0:
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		state[id] = 2
		return nil
	}
	for _, id := range ids {
		if state[id] == 0 {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	if entry != "" {
		reachable := map[string]bool{entry: true}
		queue := []string{entry}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			for _, next := range adj[id] {
				if !reachable[next] {
					reachable[next] = true
					queue = append(queue, next)
				}
			}
		}
		for _, id := range ids {
			if !reachable[id] {
				return fmt.Errorf("node %q is unreachable from entry %q", id, entry)
			}
		}
	}
	return nil
}

// AddNode registers or replaces n by ID.
func (p *Plan) AddNode(n node.Node) {
	if p == nil || n == nil {
		return
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return
	}
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

// AddNodeIfAbsent registers n only when its ID is not present. It returns
// true when n was added and false when an existing node won the race.
func (p *Plan) AddNodeIfAbsent(n node.Node) bool {
	if p == nil || n == nil {
		return false
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return false
	}
	for {
		old := p.nodes.Load()
		if _, exists := (*old)[n.ID()]; exists {
			return false
		}
		updated := make(map[string]node.Node, len(*old)+1)
		for id, existing := range *old {
			updated[id] = existing
		}
		updated[n.ID()] = n
		if p.nodes.CompareAndSwap(old, &updated) {
			return true
		}
	}
}

// ReplaceNode overwrites the node with id. Returns false if no such node
// exists, in which case the kernel is left unchanged.
func (p *Plan) ReplaceNode(n node.Node) bool {
	if p == nil || n == nil {
		return false
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return false
	}
	for {
		old := p.nodes.Load()
		if _, exists := (*old)[n.ID()]; !exists {
			return false
		}
		updated := make(map[string]node.Node, len(*old))
		for id, existing := range *old {
			updated[id] = existing
		}
		updated[n.ID()] = n
		if p.nodes.CompareAndSwap(old, &updated) {
			return true
		}
	}
}

// RemoveNode deletes a node by ID.
func (p *Plan) RemoveNode(id string) {
	if p == nil {
		return
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return
	}
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

// AddEdge appends a directed edge to the plan. Conditional function values
// are deliberately not compared because Go functions have no reliable
// identity relation beyond nil.
func (p *Plan) AddEdge(e edge.Edge) {
	if p == nil {
		return
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return
	}
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

// AddUnconditionalEdgeIfAbsent adds an unconditional edge only when the same
// endpoints are not already connected by another unconditional edge. It
// returns true when the edge was added. Conditional edges must use AddEdge.
func (p *Plan) AddUnconditionalEdgeIfAbsent(from, to string) bool {
	if p == nil {
		return false
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return false
	}
	for {
		old := p.edges.Load()
		for _, candidate := range *old {
			if candidate.From == from && candidate.To == to && candidate.Condition == nil {
				return false
			}
		}
		updated := make([]edge.Edge, len(*old)+1)
		copy(updated, *old)
		updated[len(*old)] = edge.Edge{From: from, To: to}
		if p.edges.CompareAndSwap(old, &updated) {
			return true
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
	if p == nil {
		return
	}
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if p.IsSealed() {
		return
	}
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
