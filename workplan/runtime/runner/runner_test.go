package runner

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/checkpoint"
)

type mockNode struct {
	node.BaseNode
	runFn func(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

func newMockNode(id string, kind node.NodeKind) *mockNode {
	return &mockNode{BaseNode: node.NewBaseNode(id, kind)}
}

func (m *mockNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if m.runFn != nil {
		return m.runFn(ctx, wc)
	}
	return "", nil
}

func TestNew(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.plan != g {
		t.Error("plan not set on runner")
	}
	if r.sched == nil {
		t.Error("scheduler not initialized")
	}
	if r.exec == nil {
		t.Error("executor not initialized")
	}
	if r.checkMgr != nil {
		t.Error("expected nil checkMgr without WithCheckpoint")
	}
}

func TestNewWithPlan(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	if r.Plan() != g {
		t.Fatal("runner must retain the supplied plan")
	}
}

func TestWithCheckpointOption(t *testing.T) {
	g := coreplan.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))
	if r.checkMgr == nil {
		t.Fatal("expected checkMgr to be set")
	}

	// Verify the checkpoint manager works
	wc := types.NewWorkflowContext()
	_, err := r.checkMgr.Save("test", wc)
	if err != nil {
		t.Fatalf("checkpoint save failed: %v", err)
	}
}

func TestRunExecutesPlan(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	r := New(g)
	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "end" {
		t.Errorf("expected second node 'end', got %q", result.NodeResults[1].NodeID)
	}
}

func TestRunValidatesPlan(t *testing.T) {
	g := coreplan.New()
	// Plan has an entry set to a node that does not exist.
	g.SetEntry("nonexistent")
	g.AddNode(newMockNode("start", node.KindMethod))

	r := New(g)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected validation error for missing entry node, got nil")
	}
}

func TestRunDetectsCycle(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("a", node.KindMethod))
	g.AddNode(newMockNode("b", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "a"}) // cycle

	r := New(g)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected cycle validation error, got nil")
	}
}

func TestPlanAccessor(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	returned := r.Plan()
	if returned != g {
		t.Error("Plan() returned a different Plan instance")
	}
}

func TestResumeFromCheckpoint(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))

	// Manually save a checkpoint for "start"
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"checkpoint-value"`
	r.checkMgr.Save("start", wc)

	// Resume from checkpoint
	result, err := r.Resume(context.Background(), "start")
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should execute "start" and "end"
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "end" {
		t.Errorf("expected second node 'end', got %q", result.NodeResults[1].NodeID)
	}
}

func TestResumeWithoutCheckpoint(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	_, err := r.Resume(context.Background(), "some-id")
	if err == nil {
		t.Fatal("expected error when checkpoint not enabled, got nil")
	}
}

func TestResumeWithMissingSnapshot(t *testing.T) {
	g := coreplan.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))

	_, err := r.Resume(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}
