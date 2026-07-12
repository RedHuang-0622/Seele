package scheduler

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
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
	g := graph.New()
	exec := executor.New()
	s := New(g, exec)
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.graph != g {
		t.Error("graph not set")
	}
	if s.executor != exec {
		t.Error("executor not set")
	}
}

func TestRunBasicLinearExecution(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("mid", node.KindMethod))
	g.AddNode(newTestNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "mid"})
	g.AddEdge(edge.Edge{From: "mid", To: "end"})

	exec := executor.New()
	s := New(g, exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 3 {
		t.Fatalf("expected 3 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "mid" {
		t.Errorf("expected second node 'mid', got %q", result.NodeResults[1].NodeID)
	}
	if result.NodeResults[2].NodeID != "end" {
		t.Errorf("expected third node 'end', got %q", result.NodeResults[2].NodeID)
	}
	if result.Aborted {
		t.Error("expected Aborted to be false")
	}
}

func TestRunWithSingleNode(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("only", node.KindMethod))
	g.SetEntry("only")

	exec := executor.New()
	s := New(g, exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(result.NodeResults) != 1 {
		t.Fatalf("expected 1 node result, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "only" {
		t.Errorf("expected node 'only', got %q", result.NodeResults[0].NodeID)
	}
}

func TestRunWithBranchConditionalEdge(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("branchA", node.KindMethod))
	g.AddNode(newTestNode("branchB", node.KindMethod))
	g.SetEntry("start")

	trueCond := func(wc *types.WorkflowContext) bool { return true }
	falseCond := func(wc *types.WorkflowContext) bool { return false }
	g.AddEdge(edge.Edge{From: "start", To: "branchA", Condition: trueCond})
	g.AddEdge(edge.Edge{From: "start", To: "branchB", Condition: falseCond})

	exec := executor.New()
	s := New(g, exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Only branchA should execute (true condition), branchB should be skipped
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results (start + branchA), got %d", len(result.NodeResults))
	}
	if result.NodeResults[1].NodeID != "branchA" {
		t.Errorf("expected second node 'branchA', got %q", result.NodeResults[1].NodeID)
	}
}

func TestRunWithFork(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("b1", node.KindMethod))
	g.AddNode(newTestNode("b2", node.KindMethod))
	g.AddNode(newTestNode("merge", node.KindMethod))
	g.SetEntry("start")

	// Unconditional fork: start -> {b1, b2}
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	// Convergence: b1 -> merge, b2 -> merge
	g.AddEdge(edge.Edge{From: "b1", To: "merge"})
	g.AddEdge(edge.Edge{From: "b2", To: "merge"})

	exec := executor.New()
	s := New(g, exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Should execute: start, b1, b2, merge (4 nodes)
	if len(result.NodeResults) != 4 {
		t.Fatalf("expected 4 node results (start, b1, b2, merge), got %d", len(result.NodeResults))
	}
	// First node should always be "start"
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
}

func TestRunWithForkDivergent(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("b1", node.KindMethod))
	g.AddNode(newTestNode("b2", node.KindMethod))
	g.SetEntry("start")

	// Unconditional fork: start -> {b1, b2}
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	// b1 -> end1, b2 -> end2 (different targets — divergent)
	g.AddEdge(edge.Edge{From: "b1", To: "end1"})
	g.AddEdge(edge.Edge{From: "b2", To: "end2"})

	exec := executor.New()
	s := New(g, exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Divergent fork returns early after branches, so we expect:
	// start, b1, b2 — then divergent detection returns ""
	if len(result.NodeResults) < 3 {
		t.Errorf("expected at least 3 node results, got %d", len(result.NodeResults))
	}
}

func TestRunNodeNotFound(t *testing.T) {
	g := graph.New()
	g.SetEntry("nonexistent")

	exec := executor.New()
	s := New(g, exec)

	_, err := s.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for missing entry node, got nil")
	}
}

func TestRunContextCancellation(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.SetEntry("start")

	exec := executor.New()
	s := New(g, exec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediately cancel

	result, err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run should handle cancellation gracefully: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Aborted {
		t.Error("expected Aborted to be true after cancellation")
	}
}

func TestRunWithCheckpointCreatesSnapshots(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})

	exec := executor.New()
	s := New(g, exec)

	result, checkpoints, err := s.RunWithCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("RunWithCheckpoint failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 2 {
		t.Errorf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if len(checkpoints) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(checkpoints))
	}
	if _, ok := checkpoints["a"]; !ok {
		t.Error("expected checkpoint for node 'a'")
	}
	if _, ok := checkpoints["b"]; !ok {
		t.Error("expected checkpoint for node 'b'")
	}
	if checkpoints["a"].Status != types.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", checkpoints["a"].Status)
	}
	if checkpoints["a"].NodeID != "a" {
		t.Errorf("expected NodeID 'a', got %q", checkpoints["a"].NodeID)
	}
	if checkpoints["a"].Context == nil {
		t.Error("expected non-nil Context in checkpoint")
	}
}
