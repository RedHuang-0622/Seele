// Package serialize provides Plan ↔ Graph bidirectional conversion.
// Plan is a pure-data JSON-serializable representation of a workflow.
package serialize

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// PlanNodeSpec describes a single node in the serializable plan.
type PlanNodeSpec struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// PlanEdgeSpec describes a single edge in the serializable plan.
type PlanEdgeSpec struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Label     string `json:"label,omitempty"`
	Condition string `json:"condition,omitempty"` // label reference to ConditionRegistry
}

// Plan is a serializable workflow definition.
type Plan struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Version     string         `json:"version,omitempty"`
	EntryNodeID string         `json:"entry_node_id"`
	Nodes       []PlanNodeSpec `json:"nodes"`
	Edges       []PlanEdgeSpec `json:"edges"`
}

// NewPlan creates an empty plan.
func NewPlan(name string) *Plan {
	return &Plan{Name: name}
}

// Add appends a node spec to the plan.
func (p *Plan) Add(id, kind string) *Plan {
	p.Nodes = append(p.Nodes, PlanNodeSpec{ID: id, Kind: kind})
	return p
}

// Edge appends an edge spec to the plan.
func (p *Plan) Edge(from, to string) *Plan {
	p.Edges = append(p.Edges, PlanEdgeSpec{From: from, To: to})
	return p
}

// ToJSON serializes the plan to JSON.
func (p *Plan) ToJSON() (string, error) {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// FromJSON deserializes a plan from JSON.
func FromJSON(data string) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal([]byte(data), &p); err != nil {
		return nil, fmt.Errorf("plan deserialize: %w", err)
	}
	return &p, nil
}

// ToPlan exports a Graph to a Plan.
func ToPlan(g *graph.Graph) *Plan {
	plan := &Plan{EntryNodeID: g.Entry()}
	for _, id := range g.AllNodes() {
		n := g.GetNode(id)
		kind := "unknown"
		if n != nil {
			kind = n.Kind().String()
		}
		plan.Nodes = append(plan.Nodes, PlanNodeSpec{ID: id, Kind: kind})
	}
	for _, e := range g.AllEdges() {
		plan.Edges = append(plan.Edges, PlanEdgeSpec{
			From: e.From, To: e.To, Label: e.Label,
		})
	}
	return plan
}

// FromPlan reconstructs a Graph from a Plan.
// Node implementations must be provided externally since Go functions cannot be serialized.
func FromPlan(p *Plan, registry *types.ConditionRegistry) (*graph.Graph, error) {
	g := graph.New()
	g.SetEntry(p.EntryNodeID)

	// Create placeholder nodes (kind recorded but no execution logic)
	for _, ns := range p.Nodes {
		kind := parseKind(ns.Kind)
		placeholder := node.NewBaseNode(ns.ID, kind)
		// Wrap in a placeholder struct that implements node.Node
		g.AddNode(&placeholderNode{BaseNode: placeholder, kind: kind})
	}

	// Add edges with optional conditions
	for _, es := range p.Edges {
		e := edge.Edge{From: es.From, To: es.To, Label: es.Label}
		if es.Condition != "" && registry != nil {
			if cond, ok := registry.Resolve(es.Condition); ok {
				e.Condition = cond
			}
		}
		g.AddEdge(e)
	}

	return g, nil
}

// placeholderNode is a minimal node.Node implementation for deserialized plans.
// Real execution logic must be injected by replacing placeholder nodes.
type placeholderNode struct {
	node.BaseNode
	kind node.NodeKind
}

func (p *placeholderNode) Kind() node.NodeKind { return p.kind }
func (p *placeholderNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	return "", fmt.Errorf("placeholder node %q: execution logic not injected; use ReplaceNode for real execution", p.ID())
}

func parseKind(s string) node.NodeKind {
	switch s {
	case "method":
		return node.KindMethod
	case "llm":
		return node.KindLLM
	case "agent", "auto":
		return node.KindAuto
	case "approve":
		return node.KindApprove
	case "if":
		return node.KindIf
	case "switch":
		return node.KindSwitch
	case "loop":
		return node.KindLoop
	case "fork":
		return node.KindFork
	case "join":
		return node.KindJoin
	case "checkpoint":
		return node.KindCheckpoint
	case "emit":
		return node.KindEmit
	default:
		return node.KindMethod
	}
}
