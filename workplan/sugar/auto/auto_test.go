package auto

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ── Mock types ─────────────────────────────────────────────────────────────

type mockAgent struct{}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	return "mock:" + input, nil
}

type mockFactory struct{}

func (m *mockFactory) NewAgent(_ string) node.Agent {
	return &mockAgent{}
}

type mockLLMProvider struct{}

func (m *mockLLMProvider) Chat(_ context.Context, input string) (string, error) {
	return "llm:" + input, nil
}

func (m *mockLLMProvider) ChatStream(_ context.Context, input string, onChunk func(string)) (string, error) {
	onChunk("llm:" + input)
	return "llm:" + input, nil
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestNewMethodStrategy(t *testing.T) {
	fn := func(ctx context.Context, input string) (string, error) {
		return "processed:" + input, nil
	}
	s := NewMethodStrategy(fn)
	if s == nil {
		t.Fatal("NewMethodStrategy returned nil")
	}
}

func TestNewLLMStrategy(t *testing.T) {
	prov := &mockLLMProvider{}
	s := NewLLMStrategy(prov)
	if s == nil {
		t.Fatal("NewLLMStrategy returned nil")
	}
}

func TestNewAgentStrategy(t *testing.T) {
	factory := &mockFactory{}
	s := NewAgentStrategy(factory, "You are a test assistant.")
	if s == nil {
		t.Fatal("NewAgentStrategy returned nil")
	}
}

func TestNewAgentStrategy_DefaultPrompt(t *testing.T) {
	factory := &mockFactory{}
	s := NewAgentStrategy(factory, "")
	if s == nil {
		t.Fatal("NewAgentStrategy with empty prompt returned nil")
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	n := Add(g, "agent-node", "hello", &mockFactory{})
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if n.ID() != "agent-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "agent-node")
	}
	if got := g.GetNode("agent-node"); got == nil {
		t.Error("agent-node not found in graph")
	}
}

func TestAddMethod(t *testing.T) {
	g := graph.New()
	fn := func(ctx context.Context, input string) (string, error) {
		return "result", nil
	}
	n := AddMethod(g, "method-node", fn)
	if n == nil {
		t.Fatal("AddMethod() returned nil")
	}
	if n.ID() != "method-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "method-node")
	}
	if got := g.GetNode("method-node"); got == nil {
		t.Error("method-node not found in graph")
	}
}

func TestAddLLM(t *testing.T) {
	g := graph.New()
	n := AddLLM(g, "llm-node", "prompt", &mockLLMProvider{})
	if n == nil {
		t.Fatal("AddLLM() returned nil")
	}
	if n.ID() != "llm-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "llm-node")
	}
	if got := g.GetNode("llm-node"); got == nil {
		t.Error("llm-node not found in graph")
	}
}

func TestWithToolFilter(t *testing.T) {
	n := &StrategyNode{}
	opt := WithToolFilter([]string{"tool-a", "tool-b"})
	opt(n)
	if len(n.ToolFilter) != 2 {
		t.Errorf("ToolFilter length = %d, want 2", len(n.ToolFilter))
	}
}

func TestWithSystemPrompt(t *testing.T) {
	n := &StrategyNode{}
	opt := WithSystemPrompt("custom prompt")
	opt(n)
	if n.SystemPrompt != "custom prompt" {
		t.Errorf("SystemPrompt = %q, want %q", n.SystemPrompt, "custom prompt")
	}
}

func TestWithOnChunk(t *testing.T) {
	n := &StrategyNode{}
	called := false
	opt := WithOnChunk(func(s string) { called = true })
	opt(n)
	if n.onChunk == nil {
		t.Error("onChunk should not be nil")
	}
	n.onChunk("test")
	if !called {
		t.Error("onChunk callback was not called")
	}
}

func TestMethodStrategy_Execute(t *testing.T) {
	s := NewMethodStrategy(func(ctx context.Context, input string) (string, error) {
		return "ok", nil
	})
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"hello"`
	result, err := s.Execute(context.Background(), wc)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != `"ok"` {
		t.Errorf("Execute() = %q, want %q", result, `"ok"`)
	}
}

func TestStrategyNode_Run_NilStrategy(t *testing.T) {
	n := &StrategyNode{
		BaseNode: node.NewBaseNode("nil-strat", node.KindMethod),
		Strategy: nil,
	}
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"passthrough"`
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != `"passthrough"` {
		t.Errorf("Run() = %q, want %q", result, `"passthrough"`)
	}
}

func TestStrategyNode_Run_WithInput(t *testing.T) {
	fn := func(ctx context.Context, input string) (string, error) {
		return "done", nil
	}
	n := &StrategyNode{
		BaseNode: node.NewBaseNode("with-input", node.KindMethod),
		Strategy: NewMethodStrategy(fn),
		Input:    "{{.PrevResult}}",
	}
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"input-data"`
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != `"done"` {
		t.Errorf("Run() = %q, want %q", result, `"done"`)
	}
}

func TestStrategyNode_Kind(t *testing.T) {
	n := &StrategyNode{
		BaseNode: node.NewBaseNode("kind-test", node.KindMethod),
	}
	if n.Kind() != node.KindMethod {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindMethod)
	}
}

func TestAddToGraph_AllNodesPresent(t *testing.T) {
	g := graph.New()
	Add(g, "a", "input", &mockFactory{})
	AddMethod(g, "b", func(ctx context.Context, input string) (string, error) {
		return "ok", nil
	})
	AddLLM(g, "c", "prompt", &mockLLMProvider{})

	nodes := g.AllNodes()
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d: %v", len(nodes), nodes)
	}
}
