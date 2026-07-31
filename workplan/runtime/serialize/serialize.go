// Package serialize bridges the versioned Seele JSON DSL and runtime graphs.
package serialize

import (
	"context"
	"fmt"
	"sort"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/dsl"
)

// Plan is the versioned, executable Seele WorkPlan JSON document.
type Plan = dsl.Plan

// PlanNodeSpec is a task node in a versioned Seele WorkPlan document.
type PlanNodeSpec = dsl.Node

// PlanEdgeSpec is a dependency edge in a versioned Seele WorkPlan document.
type PlanEdgeSpec = dsl.Edge

// NewPlan creates an empty versioned Plan. Callers must add an entry and node
// definitions before serializing or compiling it.
func NewPlan() *Plan {
	return &Plan{Version: dsl.Version}
}

// FromJSON parses the versioned Seele WorkPlan JSON DSL.
func FromJSON(data string) (*Plan, error) {
	return dsl.Parse(data)
}

// ToPlan exports a WorkPlan kernel as an executable Seele WorkPlan document.
// Only core or sugar auto nodes exposing node.InputNode can be represented by
// DSL version 1.
func ToPlan(p *coreplan.Plan) (*Plan, error) {
	plan := NewPlan()
	plan.Entry = p.Entry()
	ids := p.AllNodes()
	sort.Strings(ids)
	for _, id := range ids {
		n := p.GetNode(id)
		if n == nil {
			return nil, fmt.Errorf("export Seele DSL: node %q is missing from graph", id)
		}
		if n.Kind() != node.KindAuto {
			return nil, fmt.Errorf("export Seele DSL: node %q has kind %q; DSL version %d supports only %q nodes", id, n.Kind(), dsl.Version, "auto")
		}
		inputNode, ok := n.(node.InputNode)
		if !ok {
			return nil, fmt.Errorf("export Seele DSL: node %q does not expose its declarative input", id)
		}
		plan.Nodes = append(plan.Nodes, PlanNodeSpec{ID: id, Input: inputNode.DSLInput(), Kind: "auto"})
	}
	for _, e := range p.AllEdges() {
		plan.Edges = append(plan.Edges, PlanEdgeSpec{From: e.From, To: e.To})
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}

// Compile validates p and creates executable core auto nodes and core edges.
// It intentionally bypasses the workplan sugar packages.
func Compile(p *Plan, factory node.AgentFactory) (*coreplan.Plan, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if factory == nil {
		return nil, fmt.Errorf("compile Seele DSL: agent factory is nil")
	}
	compiled := coreplan.New()
	for _, spec := range p.Nodes {
		compiled.AddNode(node.NewAutoNode(spec.ID, factory, spec.Input))
	}
	compiled.SetEntry(p.Entry)
	for _, spec := range p.Edges {
		compiled.AddEdge(edge.Edge{From: spec.From, To: spec.To})
	}
	return compiled, nil
}

// FromPlan reconstructs a non-executable placeholder graph for callers that
// inspect graph structure without supplying an AgentFactory. Use Compile when
// the graph must run subagents.
func FromPlan(p *Plan, _ *types.ConditionRegistry) (*coreplan.Plan, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	compiled := coreplan.New()
	for _, spec := range p.Nodes {
		compiled.AddNode(&placeholderNode{BaseNode: node.NewBaseNode(spec.ID, node.KindAuto)})
	}
	compiled.SetEntry(p.Entry)
	for _, spec := range p.Edges {
		compiled.AddEdge(edge.Edge{From: spec.From, To: spec.To})
	}
	return compiled, nil
}

// placeholderNode is used only by FromPlan, which cannot accept an
// AgentFactory. Compile provides executable graphs.
type placeholderNode struct{ node.BaseNode }

func (p *placeholderNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return "", fmt.Errorf("placeholder node %q: compile the DSL with an AgentFactory before execution", p.ID())
}
