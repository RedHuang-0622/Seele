package validate

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

func TestGraphValidPasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	err := Graph(g)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestEntryNodeMissingEntryReturnsError(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.SetEntry("nonexistent")

	err := EntryNode(g)
	if err == nil {
		t.Fatal("expected error for missing entry node, got nil")
	}
}

func TestEntryNodeEmptyEntryIsValid(t *testing.T) {
	g := graph.New()
	err := EntryNode(g)
	if err != nil {
		t.Fatalf("expected no error for empty graph, got: %v", err)
	}
}

func TestEntryNodeValidPasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.SetEntry("start")

	err := EntryNode(g)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestEdgeReferencesInvalidEdgeTarget(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddEdge(edge.Edge{From: "a", To: "nonexistent"})

	err := EdgeReferences(g)
	if err == nil {
		t.Fatal("expected error for invalid edge target, got nil")
	}
}

func TestEdgeReferencesValidPasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.AddEdge(edge.Edge{From: "a", To: "b"})

	err := EdgeReferences(g)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCyclicDetectsCycle(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.AddNode(newTestNode("c", node.KindMethod))
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "c"})
	g.AddEdge(edge.Edge{From: "c", To: "a"}) // cycle: a -> b -> c -> a

	err := Cyclic(g)
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
}

func TestCyclicNoCyclePasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.AddNode(newTestNode("c", node.KindMethod))
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "c"})

	err := Cyclic(g)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCyclicSelfLoop(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddEdge(edge.Edge{From: "a", To: "a"}) // self-loop

	err := Cyclic(g)
	if err == nil {
		t.Fatal("expected cycle detection error for self-loop, got nil")
	}
}

func TestOrphanDetectsUnreachableNodes(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("orphan", node.KindMethod))
	g.SetEntry("start")
	// No edges at all — "orphan" has no incoming edges and is not the entry

	err := Orphan(g)
	if err == nil {
		t.Fatal("expected orphan detection error, got nil")
	}
}

func TestOrphanAllReachablePasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	err := Orphan(g)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestOrphanNoEntryPasses(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	// No entry set — Orphan returns nil early

	err := Orphan(g)
	if err != nil {
		t.Fatalf("expected no error for graph with no entry, got: %v", err)
	}
}

func TestOrphanDisconnectedGraph(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.AddNode(newTestNode("c", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	// "c" has no incoming edges and is not the entry

	err := Orphan(g)
	if err == nil {
		t.Fatal("expected orphan detection error for disconnected node 'c', got nil")
	}
}
