package plan

import (
	"context"
	"fmt"
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
			nodeID := fmt.Sprintf("n%d", i)
			edgeTo := fmt.Sprintf("n%d", i)
			p.AddNode(newTestNode(nodeID))
			p.AddEdge(edge.Edge{From: "entry", To: edgeTo})
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

func TestAddNodeOverwritesByID(t *testing.T) {
	p := New()
	first := newTestNode("a")
	second := newTestNode("a")

	p.AddNode(first)
	p.AddNode(second)

	if got := len(p.AllNodes()); got != 1 {
		t.Fatalf("AllNodes = %d, want 1", got)
	}
	if p.GetNode("a") != second {
		t.Fatal("AddNode must overwrite an existing node by ID")
	}
}

func TestAddNodeIfAbsentIsIdempotentByID(t *testing.T) {
	p := New()
	first := newTestNode("a")
	second := newTestNode("a")

	if !p.AddNodeIfAbsent(first) {
		t.Fatal("first AddNodeIfAbsent must add the node")
	}
	if p.AddNodeIfAbsent(second) {
		t.Fatal("second AddNodeIfAbsent must report the existing node")
	}
	if p.GetNode("a") != first {
		t.Fatal("AddNodeIfAbsent must keep the first node")
	}
}

func TestSealMakesPlanImmutable(t *testing.T) {
	p := New()
	p.AddNode(newTestNode("start"))
	p.SetEntry("start")
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	p.AddNode(newTestNode("late"))
	p.AddEdge(edge.Edge{From: "start", To: "late"})
	p.SetEntry("late")
	if p.GetNode("late") != nil || len(p.AllEdges()) != 0 || p.Entry() != "start" {
		t.Fatalf("sealed plan was mutated: entry=%q nodes=%v edges=%v", p.Entry(), p.AllNodes(), p.AllEdges())
	}
}

func TestReplaceNodeSwapsAndReportsMissing(t *testing.T) {
	p := New()
	p.AddNode(newTestNode("a"))

	if p.ReplaceNode(newTestNode("missing")) {
		t.Fatal("ReplaceNode must return false when the ID is unknown")
	}

	replacement := newTestNode("a")
	if !p.ReplaceNode(replacement) {
		t.Fatal("ReplaceNode must return true when the ID exists")
	}
	if p.GetNode("a") != replacement {
		t.Fatal("ReplaceNode did not install the new node")
	}
}

func TestAddUnconditionalEdgeIfAbsentIsIdempotentByEndpoints(t *testing.T) {
	p := New()

	if !p.AddUnconditionalEdgeIfAbsent("a", "b") {
		t.Fatal("first edge must be added")
	}
	if p.AddUnconditionalEdgeIfAbsent("a", "b") {
		t.Fatal("duplicate edge must not be added")
	}
	if !p.AddUnconditionalEdgeIfAbsent("a", "c") {
		t.Fatal("different endpoints must be added")
	}

	if got := len(p.AllEdges()); got != 2 {
		t.Fatalf("AllEdges = %d, want 2", got)
	}
}

func TestAddEdgeKeepsClosuresFromSameCodeLocation(t *testing.T) {
	p := New()
	for _, expected := range []string{"backend", "tests"} {
		expected := expected
		p.AddEdge(edge.Edge{
			From: "a",
			To:   "b",
			Condition: func(wc *types.WorkflowContext) bool {
				return wc.Vars["branch"] == expected
			},
		})
	}

	if got := len(p.AllEdges()); got != 2 {
		t.Fatalf("AllEdges = %d, want 2 distinct captured conditions", got)
	}
}
