package loop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ── Mock types ─────────────────────────────────────────────────────────────

type mockAgent struct {
	iter int
	mu   sync.Mutex
}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	m.mu.Lock()
	m.iter++
	iter := m.iter
	m.mu.Unlock()
	if iter >= 3 {
		return `"done"`, nil
	}
	return `"iteration-` + input + `"`, nil
}

type mockFactory struct{}

func (m *mockFactory) NewAgent(_ string) node.Agent {
	return &mockAgent{}
}

// ── Signal tests ───────────────────────────────────────────────────────────

func TestNewSignal(t *testing.T) {
	s := NewSignal()
	if s == nil {
		t.Fatal("NewSignal() returned nil")
	}
	if s.Get() != `""` {
		t.Errorf("initial Get() = %q, want %q", s.Get(), `""`)
	}
	if s.Iter() != 0 {
		t.Errorf("initial Iter() = %d, want 0", s.Iter())
	}
}

func TestSignal_SetAndGet(t *testing.T) {
	s := NewSignal()
	s.Set("hello", 1)

	if s.Get() != `"hello"` {
		t.Errorf("Get() = %q, want %q", s.Get(), `"hello"`)
	}
	if s.GetString() != "hello" {
		t.Errorf("GetString() = %q, want %q", s.GetString(), "hello")
	}
	if s.Iter() != 1 {
		t.Errorf("Iter() = %d, want 1", s.Iter())
	}
}

func TestSignal_SetMultiple(t *testing.T) {
	s := NewSignal()
	s.Set("first", 1)
	s.Set("second", 2)

	if s.GetString() != "second" {
		t.Errorf("GetString() = %q, want %q", s.GetString(), "second")
	}
	if s.Iter() != 2 {
		t.Errorf("Iter() = %d, want 2", s.Iter())
	}
}

func TestSignal_CloseAndWait(t *testing.T) {
	s := NewSignal()
	s.Set("final-value", 1)

	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()

	s.Close()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait() did not return after Close()")
	}

	if s.GetString() != "final-value" {
		t.Errorf("after wait GetString() = %q, want %q", s.GetString(), "final-value")
	}
}

func TestSignal_CloseIdempotent(t *testing.T) {
	s := NewSignal()
	s.Close()
	s.Close() // should not panic
}

func TestSignal_OnUpdate(t *testing.T) {
	s := NewSignal()
	var received []string
	var mu sync.Mutex

	s.OnUpdate(func(v string) {
		mu.Lock()
		received = append(received, v)
		mu.Unlock()
	})

	s.Set("update-1", 1)
	s.Set("update-2", 2)

	mu.Lock()
	if len(received) != 2 {
		t.Fatalf("received %d updates, want 2", len(received))
	}
	if received[0] != `"update-1"` || received[1] != `"update-2"` {
		t.Errorf("received = %v", received)
	}
	mu.Unlock()
}

func TestSignal_OnUpdateMultipleCallbacks(t *testing.T) {
	s := NewSignal()
	var c1, c2 int

	s.OnUpdate(func(v string) { c1++ })
	s.OnUpdate(func(v string) { c2++ })

	s.Set("val", 1)
	s.Set("val2", 2)

	if c1 != 2 || c2 != 2 {
		t.Errorf("c1=%d, c2=%d, both want 2", c1, c2)
	}
}

// ── LoopNode tests ─────────────────────────────────────────────────────────

func TestNewNode(t *testing.T) {
	n := NewNode("loop-1", "body-id", &mockFactory{})
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "loop-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "loop-1")
	}
	if n.Kind() != node.KindLoop {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindLoop)
	}
	if n.BodyID != "body-id" {
		t.Errorf("BodyID = %q, want %q", n.BodyID, "body-id")
	}
	if n.Signal == nil {
		t.Error("Signal should not be nil")
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	signal := Add(g, "loop-node", "body-node", &mockFactory{})
	if signal == nil {
		t.Fatal("Add() returned nil signal")
	}
	if got := g.GetNode("loop-node"); got == nil {
		t.Error("loop-node not found in graph")
	}
}

func TestAdd_WithOptions(t *testing.T) {
	g := graph.New()
	cond := func(s string) bool { return s == "done" }
	signal := Add(g, "loops-with-opts", "body", &mockFactory{},
		WithUntil(cond),
		WithMaxIter(5),
		WithOnExhausted("fallback"),
	)
	if signal == nil {
		t.Fatal("Add() with options returned nil signal")
	}

	n := g.GetNode("loops-with-opts")
	if n == nil {
		t.Fatal("node not found")
	}
	ln, ok := n.(*LoopNode)
	if !ok {
		t.Fatal("node is not *LoopNode")
	}
	if ln.Until == nil {
		t.Error("Until should not be nil")
	}
	if ln.MaxIter != 5 {
		t.Errorf("MaxIter = %d, want 5", ln.MaxIter)
	}
	if ln.OnExhausted != "fallback" {
		t.Errorf("OnExhausted = %q, want %q", ln.OnExhausted, "fallback")
	}
}

func TestWithBodyConfig(t *testing.T) {
	g := graph.New()
	signal := Add(g, "loop-body", "body", &mockFactory{},
		WithBodyConfig("system prompt", "{{.PrevResult}}"),
	)
	if signal == nil {
		t.Fatal("Add() with body config returned nil signal")
	}

	n := g.GetNode("loop-body")
	ln, ok := n.(*LoopNode)
	if !ok {
		t.Fatal("node is not *LoopNode")
	}
	if ln.bodyPrompt != "system prompt" {
		t.Errorf("bodyPrompt = %q, want %q", ln.bodyPrompt, "system prompt")
	}
	if ln.bodyInput != "{{.PrevResult}}" {
		t.Errorf("bodyInput = %q", ln.bodyInput)
	}
}

func TestWithMaxIter(t *testing.T) {
	n := NewNode("maxiter-test", "body", &mockFactory{})
	WithMaxIter(3)(n)
	if n.MaxIter != 3 {
		t.Errorf("MaxIter = %d, want 3", n.MaxIter)
	}
}

func TestWithUntil(t *testing.T) {
	n := NewNode("until-test", "body", &mockFactory{})
	cond := func(s string) bool { return s == "stop" }
	WithUntil(cond)(n)
	if n.Until == nil {
		t.Error("Until should not be nil")
	}
	if !n.Until("stop") {
		t.Error("Until('stop') should return true")
	}
	if n.Until("continue") {
		t.Error("Until('continue') should return false")
	}
}

func TestGraphContainsLoopNode(t *testing.T) {
	g := graph.New()
	Add(g, "graph-loop", "body", &mockFactory{})
	nodes := g.AllNodes()
	found := false
	for _, id := range nodes {
		if id == "graph-loop" {
			found = true
			break
		}
	}
	if !found {
		t.Error("graph-loop not found via AllNodes")
	}
}
