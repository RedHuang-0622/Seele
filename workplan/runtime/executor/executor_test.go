package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
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
	e := New()
	if e == nil {
		t.Fatal("New() returned nil")
	}
}

func TestRunNode(t *testing.T) {
	e := New()
	n := newMockNode("test", node.KindMethod)
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "result", nil
	}

	wc := types.NewWorkflowContext()
	out, err := e.RunNode(context.Background(), n, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `"result"`
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestRunNodeError(t *testing.T) {
	e := New()
	n := newMockNode("test", node.KindMethod)
	sentinel := errors.New("node error")
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "", sentinel
	}

	wc := types.NewWorkflowContext()
	_, err := e.RunNode(context.Background(), n, wc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestRunNodeWithAgentNode(t *testing.T) {
	e := New()
	n := newMockNode("agent", node.KindAgent)
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "agent output", nil
	}

	wc := types.NewWorkflowContext()
	out, err := e.RunNode(context.Background(), n, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The executor normalises output to JSON; non-JSON gets quoted
	expected := `"agent output"`
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestRunNodeWithFunctionNode(t *testing.T) {
	e := New()
	n := newMockNode("fn", node.KindMethod)
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "function output", nil
	}

	wc := types.NewWorkflowContext()
	out, err := e.RunNode(context.Background(), n, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `"function output"`
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestRunNodeWithLLMNode(t *testing.T) {
	e := New()
	n := newMockNode("llm", node.KindLLM)
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "llm response", nil
	}

	wc := types.NewWorkflowContext()
	out, err := e.RunNode(context.Background(), n, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `"llm response"`
	if out != expected {
		t.Errorf("expected %q, got %q", expected, out)
	}
}

func TestRunNodeWithUnknownKindReturnsError(t *testing.T) {
	e := New()
	// The executor delegates to node.Run(); it does not validate NodeKind.
	// An "unknown kind" is represented by a node whose Run() returns an error.
	n := newMockNode("unknown", node.NodeKind(999))
	sentinel := errors.New("unknown node kind error")
	n.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "", sentinel
	}

	wc := types.NewWorkflowContext()
	_, err := e.RunNode(context.Background(), n, wc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestJSONNormalizationInOutput(t *testing.T) {
	e := New()
	wc := types.NewWorkflowContext()

	// Already-valid JSON must not be double-encoded
	n1 := newMockNode("n1", node.KindMethod)
	n1.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return `{"result": 42}`, nil
	}
	out, err := e.RunNode(context.Background(), n1, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `{"result": 42}` {
		t.Errorf("expected unchanged JSON, got %q", out)
	}

	// Non-JSON string must be JSON-encoded (quoted)
	n2 := newMockNode("n2", node.KindMethod)
	n2.runFn = func(ctx context.Context, wc *types.WorkflowContext) (string, error) {
		return "plain text", nil
	}
	out, err = e.RunNode(context.Background(), n2, wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != `"plain text"` {
		t.Errorf("expected JSON-quoted output, got %q", out)
	}
}
