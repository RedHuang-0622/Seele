package approve

import (
	"context"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ── Mock types ─────────────────────────────────────────────────────────────

type mockGate struct {
	fn func(ctx context.Context, q Question) (any, error)
}

func (m *mockGate) Ask(ctx context.Context, q Question) (any, error) {
	return m.fn(ctx, q)
}

type mockAgent struct{}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	return "approved:" + input, nil
}

type mockFactory struct{}

func (m *mockFactory) NewAgent(_ string) node.Agent {
	return &mockAgent{}
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestNewNode(t *testing.T) {
	n := NewNode("approve-1", &mockGate{}, &mockFactory{})
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "approve-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "approve-1")
	}
	if n.Kind() != node.KindApprove {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindApprove)
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	gate := &mockGate{}
	n := Add(g, "approve-node", "execute prompt", gate, &mockFactory{})
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if got := g.GetNode("approve-node"); got == nil {
		t.Error("approve-node not found in graph")
	}
}

func TestQuestion_DefaultChoice(t *testing.T) {
	q := Question{
		ID:      "q1",
		Content: "proceed?",
		Options: []ChoiceOption{
			{Key: "yes", Label: "Yes"},
			{Key: "no", Label: "No"},
		},
	}
	if dc := q.DefaultChoice(); dc != "yes" {
		t.Errorf("DefaultChoice() = %q, want %q", dc, "yes")
	}
}

func TestQuestion_DefaultChoice_Empty(t *testing.T) {
	q := Question{ID: "empty"}
	if dc := q.DefaultChoice(); dc != "" {
		t.Errorf("DefaultChoice() = %q, want empty", dc)
	}
}

func TestQuestion_Resolve(t *testing.T) {
	q := Question{
		Options: []ChoiceOption{
			{Key: "continue", Label: "Continue"},
			{Key: "abort", Label: "Abort"},
		},
		KVS: map[string]any{
			"continue": "next-step",
			"abort":    "cleanup",
		},
	}

	v, ok := q.Resolve("continue")
	if !ok {
		t.Error("Resolve('continue') should return ok=true")
	}
	if v != "next-step" {
		t.Errorf("Resolve('continue') = %v, want %v", v, "next-step")
	}

	v, ok = q.Resolve("abort")
	if !ok {
		t.Error("Resolve('abort') should return ok=true")
	}
}

func TestQuestion_Resolve_UnknownKey(t *testing.T) {
	q := Question{
		Options: []ChoiceOption{{Key: "go", Label: "Go"}},
		KVS:     map[string]any{"go": "value"},
	}
	// Resolve falls back to DefaultChoice (first option key) when key is not in KVS
	v, ok := q.Resolve("unknown")
	if ok {
		t.Error("Resolve('unknown') should return ok=false (fallback)")
	}
	if v != "value" {
		t.Errorf("Resolve('unknown') = %v, want 'value' (fallback to default)", v)
	}
}

func TestQuestion_Resolve_NoKVS(t *testing.T) {
	q := Question{
		Options: []ChoiceOption{{Key: "opt1", Label: "Option 1"}},
	}
	v, ok := q.Resolve("opt1")
	if ok {
		t.Error("Resolve('opt1') with no KVS should return ok=false")
	}
	if v != nil {
		t.Errorf("Resolve('opt1') = %v, want nil", v)
	}
}

func TestChoices(t *testing.T) {
	opts := Choices("execute", "skip", "abort")
	if len(opts) != 3 {
		t.Fatalf("len(Choices) = %d, want 3", len(opts))
	}
	if opts[0].Key != "execute" || opts[0].Label != "执行" {
		t.Errorf("first choice = %+v", opts[0])
	}
	if opts[2].Key != "abort" || opts[2].Style != "danger" {
		t.Errorf("third choice = %+v", opts[2])
	}
}

func TestChoices_Custom(t *testing.T) {
	opts := Choices("custom-key")
	if len(opts) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(opts))
	}
	if opts[0].Key != "custom-key" || opts[0].Label != "custom-key" {
		t.Errorf("custom choice = %+v", opts[0])
	}
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if len(opts) != 3 {
		t.Fatalf("len(DefaultOptions) = %d, want 3", len(opts))
	}
	if opts[0].Key != "execute" {
		t.Errorf("first default option key = %q, want %q", opts[0].Key, "execute")
	}
}

func TestWithOptions(t *testing.T) {
	n := NewNode("opt-test", &mockGate{}, &mockFactory{})
	customOpts := []ChoiceOption{{Key: "custom", Label: "Custom"}}
	WithOptions(customOpts)(n)
	if len(n.Options) != 1 || n.Options[0].Key != "custom" {
		t.Errorf("Options = %+v", n.Options)
	}
}

func TestWithKVS(t *testing.T) {
	n := NewNode("kvs-test", &mockGate{}, &mockFactory{})
	kvs := map[string]any{"key1": "val1"}
	WithKVS(kvs)(n)
	if v, ok := n.KVS["key1"]; !ok || v != "val1" {
		t.Errorf("KVS = %v", n.KVS)
	}
}

func TestWithTimeout(t *testing.T) {
	n := NewNode("timeout-test", &mockGate{}, &mockFactory{})
	WithTimeout(30 * time.Second)(n)
	if n.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want %v", n.Timeout, 30*time.Second)
	}
}

func TestBuildKVS_FromOptions(t *testing.T) {
	n := NewNode("build-kvs", &mockGate{}, &mockFactory{})
	n.Options = Choices("a", "b", "c")
	kvs := n.BuildKVS()
	if len(kvs) != 3 {
		t.Fatalf("BuildKVS length = %d, want 3", len(kvs))
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, ok := kvs[k]; !ok {
			t.Errorf("BuildKVS missing key %q", k)
		}
	}
}

func TestBuildKVS_Explicit(t *testing.T) {
	n := NewNode("explicit-kvs", &mockGate{}, &mockFactory{})
	n.KVS = map[string]any{"custom": "value"}
	kvs := n.BuildKVS()
	if kvs["custom"] != "value" {
		t.Errorf("BuildKVS = %v", kvs)
	}
	if len(kvs) != 1 {
		t.Errorf("BuildKVS should not add from Options when KVS is set")
	}
}

func TestAddToGraph_NodeFound(t *testing.T) {
	g := graph.New()
	gate := &mockGate{
		fn: func(ctx context.Context, q Question) (any, error) {
			return "execute", nil
		},
	}
	Add(g, "approve-graph", "input", gate, &mockFactory{})
	if g.GetNode("approve-graph") == nil {
		t.Error("node not found in graph")
	}
}
