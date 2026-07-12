package serialize

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

type testNode struct {
	node.BaseNode
}

func newTestNode(id string, kind node.NodeKind) *testNode {
	return &testNode{BaseNode: node.NewBaseNode(id, kind)}
}

func (n *testNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	return "", nil
}

func TestNewPlan(t *testing.T) {
	p := NewPlan("test-workflow")
	if p == nil {
		t.Fatal("NewPlan() returned nil")
	}
	if p.Name != "test-workflow" {
		t.Errorf("expected Name 'test-workflow', got %q", p.Name)
	}
	if len(p.Nodes) != 0 {
		t.Error("expected empty nodes")
	}
	if len(p.Edges) != 0 {
		t.Error("expected empty edges")
	}
	if p.EntryNodeID != "" {
		t.Errorf("expected empty EntryNodeID, got %q", p.EntryNodeID)
	}
}

func TestPlanAddNode(t *testing.T) {
	p := NewPlan("test")
	p.Add("node1", "method")
	p.Add("node2", "llm")

	if len(p.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(p.Nodes))
	}
	if p.Nodes[0].ID != "node1" || p.Nodes[0].Kind != "method" {
		t.Errorf("unexpected first node: ID=%q, Kind=%q", p.Nodes[0].ID, p.Nodes[0].Kind)
	}
	if p.Nodes[1].ID != "node2" || p.Nodes[1].Kind != "llm" {
		t.Errorf("unexpected second node: ID=%q, Kind=%q", p.Nodes[1].ID, p.Nodes[1].Kind)
	}
}

func TestPlanAddReturnsItself(t *testing.T) {
	p := NewPlan("test")
	result := p.Add("n1", "method").Add("n2", "llm")
	if result != p {
		t.Error("Add() should return the plan for chaining")
	}
	if len(p.Nodes) != 2 {
		t.Errorf("expected 2 nodes after chaining, got %d", len(p.Nodes))
	}
}

func TestPlanEdge(t *testing.T) {
	p := NewPlan("test")
	p.Edge("from", "to")

	if len(p.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(p.Edges))
	}
	if p.Edges[0].From != "from" || p.Edges[0].To != "to" {
		t.Errorf("unexpected edge: From=%q, To=%q", p.Edges[0].From, p.Edges[0].To)
	}
}

func TestPlanEdgeReturnsItself(t *testing.T) {
	p := NewPlan("test")
	result := p.Edge("a", "b").Edge("c", "d")
	if result != p {
		t.Error("Edge() should return the plan for chaining")
	}
	if len(p.Edges) != 2 {
		t.Errorf("expected 2 edges after chaining, got %d", len(p.Edges))
	}
}

func TestPlanToJSONAndFromJSONRoundTrip(t *testing.T) {
	p := NewPlan("roundtrip")
	p.Add("start", "method")
	p.Add("process", "agent")
	p.Add("end", "method")
	p.Edge("start", "process")
	p.Edge("process", "end")
	p.EntryNodeID = "start"
	p.Description = "A test workflow"
	p.Version = "1.0"

	jsonStr, err := p.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON failed: %v", err)
	}
	if jsonStr == "" {
		t.Fatal("ToJSON returned empty string")
	}

	loaded, err := FromJSON(jsonStr)
	if err != nil {
		t.Fatalf("FromJSON failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("FromJSON returned nil")
	}
	if loaded.Name != "roundtrip" {
		t.Errorf("expected Name 'roundtrip', got %q", loaded.Name)
	}
	if loaded.Description != "A test workflow" {
		t.Errorf("expected Description 'A test workflow', got %q", loaded.Description)
	}
	if loaded.Version != "1.0" {
		t.Errorf("expected Version '1.0', got %q", loaded.Version)
	}
	if loaded.EntryNodeID != "start" {
		t.Errorf("expected EntryNodeID 'start', got %q", loaded.EntryNodeID)
	}
	if len(loaded.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(loaded.Nodes))
	}
	if len(loaded.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(loaded.Edges))
	}

	// Verify node content
	nodesByID := make(map[string]string)
	for _, ns := range loaded.Nodes {
		nodesByID[ns.ID] = ns.Kind
	}
	if nodesByID["start"] != "method" {
		t.Errorf("expected start kind 'method', got %q", nodesByID["start"])
	}
	if nodesByID["process"] != "agent" {
		t.Errorf("expected process kind 'agent', got %q", nodesByID["process"])
	}
}

func TestFromJSONInvalidInput(t *testing.T) {
	_, err := FromJSON("{invalid json}")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestToPlanFromGraph(t *testing.T) {
	g := graph.New()
	g.SetEntry("start")
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("end", node.KindAgent))
	g.AddEdge(edge.Edge{From: "start", To: "end", Label: "go"})

	plan := ToPlan(g)
	if plan == nil {
		t.Fatal("ToPlan returned nil")
	}
	if plan.EntryNodeID != "start" {
		t.Errorf("expected EntryNodeID 'start', got %q", plan.EntryNodeID)
	}
	if len(plan.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(plan.Nodes))
	}
	if len(plan.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(plan.Edges))
	}

	// Node specs should have correct kind strings
	for _, ns := range plan.Nodes {
		if ns.ID == "start" && ns.Kind != "method" {
			t.Errorf("expected start kind 'method', got %q", ns.Kind)
		}
		if ns.ID == "end" && ns.Kind != "agent" {
			t.Errorf("expected end kind 'agent', got %q", ns.Kind)
		}
	}

	// Edge spec should preserve label
	if plan.Edges[0].Label != "go" {
		t.Errorf("expected edge Label 'go', got %q", plan.Edges[0].Label)
	}
}

func TestFromPlanToGraph(t *testing.T) {
	p := NewPlan("test")
	p.Add("start", "method")
	p.Add("process", "llm")
	p.Add("end", "method")
	p.Edge("start", "process")
	p.Edge("process", "end")
	p.EntryNodeID = "start"

	registry := types.NewConditionRegistry()
	g, err := FromPlan(p, registry)
	if err != nil {
		t.Fatalf("FromPlan failed: %v", err)
	}
	if g == nil {
		t.Fatal("FromPlan returned nil")
	}
	if g.Entry() != "start" {
		t.Errorf("expected entry 'start', got %q", g.Entry())
	}

	nodes := g.AllNodes()
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}

	// Verify placeholder nodes
	startNode := g.GetNode("start")
	if startNode == nil {
		t.Fatal("expected 'start' node")
	}
	if startNode.Kind() != node.KindMethod {
		t.Errorf("expected KindMethod, got %v", startNode.Kind())
	}

	processNode := g.GetNode("process")
	if processNode == nil {
		t.Fatal("expected 'process' node")
	}
	if processNode.Kind() != node.KindLLM {
		t.Errorf("expected KindLLM, got %v", processNode.Kind())
	}

	// Placeholder Run should return an error
	_, err = startNode.Run(nil, types.NewWorkflowContext())
	if err == nil {
		t.Error("expected placeholder Run to return error")
	}

	// Verify edges
	if edges[0].From != "start" || edges[0].To != "process" {
		t.Errorf("unexpected first edge: %q -> %q", edges[0].From, edges[0].To)
	}
	if edges[1].From != "process" || edges[1].To != "end" {
		t.Errorf("unexpected second edge: %q -> %q", edges[1].From, edges[1].To)
	}
}

func TestFromPlanWithCondition(t *testing.T) {
	p := NewPlan("test")
	p.Add("start", "method")
	p.Add("branch", "method")
	p.Edge("start", "branch")
	p.Edge("start", "end").Add("end", "method")
	// Set condition on the third edge using PlanEdgeSpec
	p.Edges[1].Condition = "is_ready"
	p.EntryNodeID = "start"

	registry := types.NewConditionRegistry()
	registry.Register("is_ready", func(wc *types.WorkflowContext) bool { return true })

	g, err := FromPlan(p, registry)
	if err != nil {
		t.Fatalf("FromPlan failed: %v", err)
	}
	if g == nil {
		t.Fatal("FromPlan returned nil")
	}

	edges := g.AllEdges()
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}

	// The second edge should have a condition attached
	// Edge order: start->branch (no condition), start->end (with condition)
	if edges[1].Condition == nil {
		t.Error("expected condition on second edge, got nil")
	} else {
		// Condition should evaluate to true
		wc := types.NewWorkflowContext()
		if !edges[1].Condition(wc) {
			t.Error("expected condition to return true")
		}
	}
}

func TestPlanNodeSpec(t *testing.T) {
	spec := PlanNodeSpec{ID: "test-node", Kind: "method"}
	if spec.ID != "test-node" {
		t.Errorf("expected ID 'test-node', got %q", spec.ID)
	}
	if spec.Kind != "method" {
		t.Errorf("expected Kind 'method', got %q", spec.Kind)
	}
}

func TestPlanEdgeSpec(t *testing.T) {
	spec := PlanEdgeSpec{
		From:      "a",
		To:        "b",
		Label:     "edge1",
		Condition: "cond1",
	}
	if spec.From != "a" || spec.To != "b" {
		t.Errorf("unexpected From/To: %q -> %q", spec.From, spec.To)
	}
	if spec.Label != "edge1" {
		t.Errorf("expected Label 'edge1', got %q", spec.Label)
	}
	if spec.Condition != "cond1" {
		t.Errorf("expected Condition 'cond1', got %q", spec.Condition)
	}
}

func TestToPlanAndFromPlanRoundTrip(t *testing.T) {
	// Create a graph
	g := graph.New()
	g.SetEntry("fetch")
	g.AddNode(newTestNode("fetch", node.KindMethod))
	g.AddNode(newTestNode("parse", node.KindLLM))
	g.AddNode(newTestNode("save", node.KindMethod))
	g.AddEdge(edge.Edge{From: "fetch", To: "parse", Label: "raw_data"})
	g.AddEdge(edge.Edge{From: "parse", To: "save", Label: "parsed"})

	// Convert to plan
	plan := ToPlan(g)
	if plan.EntryNodeID != "fetch" {
		t.Fatalf("expected EntryNodeID 'fetch', got %q", plan.EntryNodeID)
	}

	// Convert back to graph
	registry := types.NewConditionRegistry()
	restored, err := FromPlan(plan, registry)
	if err != nil {
		t.Fatalf("FromPlan failed: %v", err)
	}

	// Verify the round-trip
	if restored.Entry() != "fetch" {
		t.Errorf("expected entry 'fetch', got %q", restored.Entry())
	}
	if len(restored.AllNodes()) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(restored.AllNodes()))
	}
	if len(restored.AllEdges()) != 2 {
		t.Errorf("expected 2 edges, got %d", len(restored.AllEdges()))
	}

	// Verify edges have correct directions
	edges := restored.AllEdges()
	if edges[0].From != "fetch" || edges[0].To != "parse" {
		t.Errorf("unexpected first edge: %q -> %q", edges[0].From, edges[0].To)
	}
	if edges[1].From != "parse" || edges[1].To != "save" {
		t.Errorf("unexpected second edge: %q -> %q", edges[1].From, edges[1].To)
	}
}
