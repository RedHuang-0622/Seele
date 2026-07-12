package fork

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ── Mock types ─────────────────────────────────────────────────────────────

type mockAgent struct{}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	return `"result:` + input + `"`, nil
}

type mockFactory struct{}

func (m *mockFactory) NewAgent(_ string) node.Agent {
	return &mockAgent{}
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestNewNode(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "branch-a", SystemPrompt: "You are A", Input: "input-a"},
	}
	n := NewNode("fork-1", branches, 2, &mockFactory{})
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "fork-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "fork-1")
	}
	if n.Kind() != node.KindFork {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindFork)
	}
	if len(n.Branches) != 1 {
		t.Errorf("Branches length = %d, want 1", len(n.Branches))
	}
	if n.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", n.MaxConcurrent)
	}
}

func TestNewNode_DefaultMaxConcurrent(t *testing.T) {
	n := NewNode("fork-default", nil, 0, &mockFactory{})
	if n.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (default)", n.MaxConcurrent)
	}
}

func TestNewNode_NegativeMaxConcurrent(t *testing.T) {
	n := NewNode("fork-neg", nil, -5, &mockFactory{})
	if n.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (default)", n.MaxConcurrent)
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	branches := []node.ForkBranch{
		{Label: "research", SystemPrompt: "Research agent", Input: "data"},
		{Label: "write", SystemPrompt: "Write agent", Input: "data"},
	}
	n := Add(g, "fork-node", branches, 2, &mockFactory{})
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if n.ID() != "fork-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "fork-node")
	}
	if got := g.GetNode("fork-node"); got == nil {
		t.Error("fork-node not found in graph")
	}
}

func TestAdd_EmptyBranches(t *testing.T) {
	g := graph.New()
	n := Add(g, "empty-fork", []node.ForkBranch{}, 1, &mockFactory{})
	if n == nil {
		t.Fatal("Add() with empty branches returned nil")
	}
	if len(n.Branches) != 0 {
		t.Errorf("Branches length = %d, want 0", len(n.Branches))
	}
}

func TestRun_SingleBranch(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "single", Input: "hello"},
	}
	n := NewNode("single-fork", branches, 1, &mockFactory{})
	wc := types.NewWorkflowContext()

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "single") {
		t.Errorf("result = %q, should contain branch label 'single'", result)
	}
}

func TestRun_MultipleBranches(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "branch1", Input: "input1"},
		{Label: "branch2", Input: "input2"},
	}
	n := NewNode("multi-fork", branches, 2, &mockFactory{})
	wc := types.NewWorkflowContext()

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "branch1") || !strings.Contains(result, "branch2") {
		t.Errorf("result = %q, should contain both branch labels", result)
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "slow1", Input: "data"},
	}
	n := NewNode("cancel-fork", branches, 1, &mockFactory{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := n.Run(ctx, types.NewWorkflowContext())
	if err != nil {
		t.Logf("Run with cancelled context returned: %v", err)
	}
}

func TestRun_AllBranchesSucceed(t *testing.T) {
	failFactory := &mockFactory{}
	n := NewNode("succeed-fork", []node.ForkBranch{
		{Label: "succeed1", Input: "x"},
	}, 1, failFactory)

	wc := types.NewWorkflowContext()
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() should not error when mock succeeds: %v", err)
	}
	if !strings.Contains(result, "succeed1") {
		t.Errorf("result = %q, should contain 'succeed1'", result)
	}
}

func TestGraphContainsForkNode(t *testing.T) {
	g := graph.New()
	branches := []node.ForkBranch{
		{Label: "a", Input: "in-a"},
		{Label: "b", Input: "in-b"},
	}
	Add(g, "graph-fork", branches, 3, &mockFactory{})

	nodes := g.AllNodes()
	found := false
	for _, id := range nodes {
		if id == "graph-fork" {
			found = true
			break
		}
	}
	if !found {
		t.Error("graph-fork not found via AllNodes")
	}
}
