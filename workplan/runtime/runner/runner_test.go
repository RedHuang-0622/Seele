package runner

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/checkpoint"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
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

type mockAgent struct{}

func (m *mockAgent) Chat(ctx context.Context, input string) (string, error) {
	return input, nil
}

type mockAgentFactory struct{}

func (m *mockAgentFactory) NewAgent(systemPrompt string) node.Agent {
	return &mockAgent{}
}

func TestNew(t *testing.T) {
	g := graph.New()
	r := New(g, nil)
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.graph != g {
		t.Error("graph not set on runner")
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

func TestNewWithFactory(t *testing.T) {
	g := graph.New()
	factory := &mockAgentFactory{}
	r := New(g, factory)
	if r.factory == nil {
		t.Fatal("expected factory to be set")
	}
}

func TestWithCheckpointOption(t *testing.T) {
	g := graph.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, nil, WithCheckpoint(store))
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

func TestRunExecutesGraph(t *testing.T) {
	g := graph.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	r := New(g, nil)
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

func TestRunValidatesGraph(t *testing.T) {
	g := graph.New()
	// Graph has an entry set to a node that does not exist
	g.SetEntry("nonexistent")
	g.AddNode(newMockNode("start", node.KindMethod))

	r := New(g, nil)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected validation error for missing entry node, got nil")
	}
}

func TestRunDetectsCycle(t *testing.T) {
	g := graph.New()
	g.AddNode(newMockNode("a", node.KindMethod))
	g.AddNode(newMockNode("b", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "a"}) // cycle

	r := New(g, nil)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected cycle validation error, got nil")
	}
}

func TestGraphAccessor(t *testing.T) {
	g := graph.New()
	r := New(g, nil)
	returned := r.Graph()
	if returned != g {
		t.Error("Graph() returned a different graph instance")
	}
}

func TestResumeFromCheckpoint(t *testing.T) {
	g := graph.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	store := checkpoint.NewMemoryStore()
	r := New(g, nil, WithCheckpoint(store))

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
	g := graph.New()
	r := New(g, nil)
	_, err := r.Resume(context.Background(), "some-id")
	if err == nil {
		t.Fatal("expected error when checkpoint not enabled, got nil")
	}
}

func TestResumeWithMissingSnapshot(t *testing.T) {
	g := graph.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, nil, WithCheckpoint(store))

	_, err := r.Resume(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}
