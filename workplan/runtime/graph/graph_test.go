package graph

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type testNode struct {
	node.BaseNode
	runFn func(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

func newTestNode(id string, kind node.NodeKind) *testNode {
	return &testNode{BaseNode: node.NewBaseNode(id, kind)}
}

func (n *testNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if n.runFn != nil {
		return n.runFn(ctx, wc)
	}
	return "", nil
}

func TestNew(t *testing.T) {
	g := New()
	if g == nil {
		t.Fatal("New() returned nil")
	}
	if len(*g.nodes.Load()) != 0 {
		t.Error("expected empty nodes map")
	}
	if len(*g.edges.Load()) != 0 {
		t.Error("expected empty edges slice")
	}
}

func TestAddNodeAndGetNode(t *testing.T) {
	g := New()
	n := newTestNode("a", node.KindMethod)
	g.AddNode(n)

	got := g.GetNode("a")
	if got == nil {
		t.Fatal("GetNode('a') returned nil")
	}
	if got.ID() != "a" {
		t.Errorf("expected ID 'a', got %q", got.ID())
	}
	if got.Kind() != node.KindMethod {
		t.Errorf("expected KindMethod, got %v", got.Kind())
	}
}

func TestAddNodeDuplicateOverwrites(t *testing.T) {
	g := New()
	n1 := newTestNode("x", node.KindMethod)
	n2 := newTestNode("x", node.KindLLM)
	g.AddNode(n1)
	g.AddNode(n2)

	got := g.GetNode("x")
	if got == nil {
		t.Fatal("GetNode('x') returned nil")
	}
	if got.Kind() != node.KindLLM {
		t.Errorf("expected KindLLM after overwrite, got %v", got.Kind())
	}
}

func TestRemoveNode(t *testing.T) {
	g := New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.RemoveNode("a")

	if g.GetNode("a") != nil {
		t.Error("expected nil after removal")
	}
}

func TestRemoveNodeNonExistent(t *testing.T) {
	g := New()
	// Must not panic
	g.RemoveNode("nonexistent")
}

func TestAllNodes(t *testing.T) {
	g := New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindLLM))
	g.AddNode(newTestNode("c", node.KindAgent))

	ids := g.AllNodes()
	if len(ids) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(ids))
	}

	m := make(map[string]bool)
	for _, id := range ids {
		m[id] = true
	}
	if !m["a"] || !m["b"] || !m["c"] {
		t.Error("AllNodes result missing expected IDs")
	}
}

func TestAllNodesEmpty(t *testing.T) {
	g := New()
	ids := g.AllNodes()
	if len(ids) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(ids))
	}
}

func TestAddEdge(t *testing.T) {
	g := New()
	e := edge.Edge{From: "a", To: "b"}
	g.AddEdge(e)

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].From != "a" || edges[0].To != "b" {
		t.Errorf("unexpected edge: From=%q, To=%q", edges[0].From, edges[0].To)
	}
}

func TestGetEdgesFrom(t *testing.T) {
	g := New()
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "a", To: "c"})
	g.AddEdge(edge.Edge{From: "b", To: "c"})

	edges := g.GetEdgesFrom("a")
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges from 'a', got %d", len(edges))
	}

	targets := make(map[string]bool)
	for _, e := range edges {
		targets[e.To] = true
	}
	if !targets["b"] || !targets["c"] {
		t.Error("GetEdgesFrom('a') missing expected targets")
	}
}

func TestGetEdgesFromNone(t *testing.T) {
	g := New()
	edges := g.GetEdgesFrom("nonexistent")
	if len(edges) != 0 {
		t.Errorf("expected 0 edges, got %d", len(edges))
	}
}

func TestAllEdgesReturnsCopy(t *testing.T) {
	g := New()
	g.AddEdge(edge.Edge{From: "a", To: "b"})

	edges := g.AllEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}

	// Mutating the returned slice must not affect the graph
	edges[0].From = "modified"
	original := g.AllEdges()
	if original[0].From != "a" {
		t.Error("AllEdges did not return an independent copy")
	}
}

func TestSetEntryAndEntry(t *testing.T) {
	g := New()
	if g.Entry() != "" {
		t.Error("expected empty entry initially")
	}

	g.SetEntry("start")
	if g.Entry() != "start" {
		t.Errorf("expected entry 'start', got %q", g.Entry())
	}

	g.SetEntry("other")
	if g.Entry() != "other" {
		t.Errorf("expected entry 'other', got %q", g.Entry())
	}
}

func TestResolveDelegatesToEdgeResolve(t *testing.T) {
	g := New()
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "c"})

	wc := types.NewWorkflowContext()

	next := g.Resolve("a", wc)
	if next != "b" {
		t.Errorf("expected 'b', got %q", next)
	}

	next = g.Resolve("b", wc)
	if next != "c" {
		t.Errorf("expected 'c', got %q", next)
	}

	next = g.Resolve("c", wc)
	if next != "" {
		t.Errorf("expected '', got %q", next)
	}
}

func TestResolveWithCondition(t *testing.T) {
	g := New()
	cond := func(wc *types.WorkflowContext) bool { return true }
	g.AddEdge(edge.Edge{From: "a", To: "b", Condition: cond})

	wc := types.NewWorkflowContext()
	next := g.Resolve("a", wc)
	if next != "b" {
		t.Errorf("expected 'b', got %q", next)
	}
}

func TestGetNextNodesForkAware(t *testing.T) {
	g := New()
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "a", To: "c"})
	g.AddEdge(edge.Edge{From: "a", To: "d"})

	wc := types.NewWorkflowContext()
	next := g.GetNextNodes("a", wc)
	if len(next) != 3 {
		t.Fatalf("expected 3 next nodes, got %d", len(next))
	}

	m := make(map[string]bool)
	for _, id := range next {
		m[id] = true
	}
	if !m["b"] || !m["c"] || !m["d"] {
		t.Error("GetNextNodes missing expected fork targets")
	}
}

func TestGetNextNodesConditionalFallback(t *testing.T) {
	g := New()
	cond := func(wc *types.WorkflowContext) bool { return true }
	g.AddEdge(edge.Edge{From: "a", To: "b", Condition: cond})

	wc := types.NewWorkflowContext()
	next := g.GetNextNodes("a", wc)
	if len(next) != 1 || next[0] != "b" {
		t.Errorf("expected ['b'], got %v", next)
	}
}

func TestGetNextNodesNoMatch(t *testing.T) {
	g := New()
	wc := types.NewWorkflowContext()
	next := g.GetNextNodes("a", wc)
	if next != nil {
		t.Errorf("expected nil, got %v", next)
	}
}

func TestGraphEmptyState(t *testing.T) {
	g := New()
	if g.Entry() != "" {
		t.Error("expected empty entry")
	}
	if len(g.AllNodes()) != 0 {
		t.Error("expected no nodes")
	}
	if len(g.AllEdges()) != 0 {
		t.Error("expected no edges")
	}
	if g.GetNode("nonexistent") != nil {
		t.Error("expected nil for nonexistent node")
	}
}
