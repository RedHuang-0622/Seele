package plan

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type testNode struct{ node.BaseNode }

func (n *testNode) Run(context.Context, *types.WorkflowContext) (string, error) { return "", nil }

func TestPlanOwnsNodesEdgesAndEntry(t *testing.T) {
	p := New()
	p.AddNode(&testNode{BaseNode: node.NewBaseNode("start", node.KindMethod)})
	p.AddNode(&testNode{BaseNode: node.NewBaseNode("end", node.KindMethod)})
	p.SetEntry("start")
	p.AddEdge(edge.Edge{From: "start", To: "end"})

	if p.Entry() != "start" || p.GetNode("end") == nil {
		t.Fatalf("kernel state was not retained")
	}
	next := p.GetNextNodes("start", types.NewWorkflowContext())
	if len(next) != 1 || next[0] != "end" {
		t.Fatalf("next nodes = %#v", next)
	}
}

func newTestNode(id string) *testNode {
	return &testNode{BaseNode: node.NewBaseNode(id, node.KindMethod)}
}

func TestRemoveNodeDeletesOnlyTheNamedNode(t *testing.T) {
	p := New()
	p.AddNode(newTestNode("start"))
	p.AddNode(newTestNode("end"))

	p.RemoveNode("missing") // no-op, must not disturb existing nodes
	p.RemoveNode("start")

	if p.GetNode("start") != nil {
		t.Fatal("RemoveNode did not delete the node")
	}
	if p.GetNode("end") == nil {
		t.Fatal("RemoveNode deleted an unrelated node")
	}
	if ids := p.AllNodes(); len(ids) != 1 || ids[0] != "end" {
		t.Fatalf("AllNodes = %#v, want [end]", ids)
	}
}

func TestAllNodesAndAllEdgesReturnIndependentSnapshots(t *testing.T) {
	p := New()
	p.AddNode(newTestNode("a"))
	p.AddNode(newTestNode("b"))
	p.AddEdge(edge.Edge{From: "a", To: "b"})

	ids := p.AllNodes()
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("AllNodes = %#v", ids)
	}

	// Mutating a returned snapshot must not corrupt kernel state.
	edges := p.AllEdges()
	edges[0].To = "tampered"
	if again := p.AllEdges(); again[0].To != "b" {
		t.Fatalf("AllEdges returned an aliased slice: %#v", again)
	}
}

func TestGetEdgesFromFiltersBySourceNode(t *testing.T) {
	p := New()
	p.AddEdge(edge.Edge{From: "a", To: "b"})
	p.AddEdge(edge.Edge{From: "a", To: "c"})
	p.AddEdge(edge.Edge{From: "b", To: "c"})

	from := p.GetEdgesFrom("a")
	if len(from) != 2 {
		t.Fatalf("GetEdgesFrom(a) = %#v, want 2 edges", from)
	}
	if got := p.GetEdgesFrom("missing"); len(got) != 0 {
		t.Fatalf("GetEdgesFrom(missing) = %#v, want empty", got)
	}
}

func TestResolvePrefersMatchingConditionalEdge(t *testing.T) {
	p := New()
	p.AddEdge(edge.Edge{From: "a", To: "no", Condition: func(*types.WorkflowContext) bool { return false }})
	p.AddEdge(edge.Edge{From: "a", To: "yes", Condition: func(*types.WorkflowContext) bool { return true }})

	if got := p.Resolve("a", types.NewWorkflowContext()); got != "yes" {
		t.Fatalf("Resolve = %q, want yes", got)
	}
	if got := p.Resolve("missing", types.NewWorkflowContext()); got != "" {
		t.Fatalf("Resolve(missing) = %q, want empty", got)
	}
}

func TestGetNextNodesFallsBackToConditionalEdges(t *testing.T) {
	p := New()
	p.AddEdge(edge.Edge{From: "a", To: "cond", Condition: func(*types.WorkflowContext) bool { return true }})

	next := p.GetNextNodes("a", types.NewWorkflowContext())
	if len(next) != 1 || next[0] != "cond" {
		t.Fatalf("conditional fallback = %#v", next)
	}
	if got := p.GetNextNodes("leaf", types.NewWorkflowContext()); got != nil {
		t.Fatalf("GetNextNodes(leaf) = %#v, want nil", got)
	}
}

// The kernel replaced graph's atomic store, so concurrent writers must not
// lose updates. Run with -race for full value.
func TestConcurrentAddNodeAndAddEdgeLoseNoWrites(t *testing.T) {
	p := New()
	const writers = 32

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a' + i%26))
			p.AddNode(newTestNode(id + string(rune('0'+i/26))))
			p.AddEdge(edge.Edge{From: "entry", To: id})
		}(i)
	}
	wg.Wait()

	if got := len(p.AllNodes()); got != writers {
		t.Fatalf("AllNodes = %d, want %d — concurrent AddNode lost writes", got, writers)
	}
	if got := len(p.AllEdges()); got != writers {
		t.Fatalf("AllEdges = %d, want %d — concurrent AddEdge lost writes", got, writers)
	}
}
