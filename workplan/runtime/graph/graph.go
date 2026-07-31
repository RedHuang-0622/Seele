// Package graph provides the mutable graph-facing view assembled around a
// WorkPlan kernel. Scheduling and DSL compilation depend on core/plan instead.
package graph

import (
	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// Graph is the graph editing and inspection facade for a WorkPlan kernel.
type Graph struct{ plan *coreplan.Plan }

// New creates an editing facade around a fresh Plan kernel.
func New() *Graph { return NewWithPlan(coreplan.New()) }

// NewWithPlan creates an editing facade around p.
func NewWithPlan(p *coreplan.Plan) *Graph {
	if p == nil {
		p = coreplan.New()
	}
	return &Graph{plan: p}
}

// Plan exposes the kernel assembled behind this graph facade.
func (g *Graph) Plan() *coreplan.Plan { return g.plan }

func (g *Graph) AddNode(n node.Node)                  { g.plan.AddNode(n) }
func (g *Graph) RemoveNode(id string)                 { g.plan.RemoveNode(id) }
func (g *Graph) GetNode(id string) node.Node          { return g.plan.GetNode(id) }
func (g *Graph) AllNodes() []string                   { return g.plan.AllNodes() }
func (g *Graph) AddEdge(e edge.Edge)                  { g.plan.AddEdge(e) }
func (g *Graph) GetEdgesFrom(from string) []edge.Edge { return g.plan.GetEdgesFrom(from) }
func (g *Graph) AllEdges() []edge.Edge                { return g.plan.AllEdges() }
func (g *Graph) SetEntry(id string)                   { g.plan.SetEntry(id) }
func (g *Graph) Entry() string                        { return g.plan.Entry() }
func (g *Graph) Resolve(currentID string, wc *types.WorkflowContext) string {
	return g.plan.Resolve(currentID, wc)
}
func (g *Graph) GetNextNodes(currentID string, wc *types.WorkflowContext) []string {
	return g.plan.GetNextNodes(currentID, wc)
}
