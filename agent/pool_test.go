package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// ── Mock Session ─────────────────────────────────────────────────────────────────

type mockSession struct {
	id      string
	mu      sync.Mutex
	history []types.Message
}

func (m *mockSession) Chat(_ context.Context, input string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, types.Message{Role: "user", Content: strPtr(input)})
	reply := "reply:" + input
	m.history = append(m.history, types.Message{Role: "assistant", Content: strPtr(reply)})
	return reply, nil
}

func (m *mockSession) ChatStream(_ context.Context, input string, onChunk func(string)) (string, error) {
	reply := "stream:" + input
	onChunk(reply)
	m.mu.Lock()
	m.history = append(m.history, types.Message{Role: "user", Content: strPtr(input)})
	m.history = append(m.history, types.Message{Role: "assistant", Content: strPtr(reply)})
	m.mu.Unlock()
	return reply, nil
}

func (m *mockSession) History() []types.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]types.Message, len(m.history))
	copy(cp, m.history)
	return cp
}

func (m *mockSession) SessionID() string { return m.id }

func strPtr(s string) *string { return &s }

func newMockSession(id string) *mockSession {
	return &mockSession{id: id}
}

// ── Interface Compile Checks ─────────────────────────────────────────────────────

// TestLoggerInterfaceCompileCheck verifies that *testLogger satisfies the Logger
// interface at compile time.
func TestLoggerInterfaceCompileCheck(t *testing.T) {
	var _ Logger = (*testLogger)(nil)
}

// TestSessionInterfaceCompileCheck verifies that *mockSession satisfies the Session
// interface at compile time.
func TestSessionInterfaceCompileCheck(t *testing.T) {
	var _ Session = (*mockSession)(nil)
}

// ── Tests: NewPool ───────────────────────────────────────────────────────────────

func TestNewPool(t *testing.T) {
	p := NewPool()
	if p == nil {
		t.Fatal("NewPool() returned nil")
	}
	if p.Len() != 0 {
		t.Errorf("Len() = %d, want 0", p.Len())
	}
	if p.Current() != nil {
		t.Error("Current() should be nil for empty pool")
	}
	if p.CurrentLabel() != "" {
		t.Errorf("CurrentLabel() = %q, want empty", p.CurrentLabel())
	}
	if p.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex() = %d, want 0", p.CurrentIndex())
	}
}

// ── Tests: Add ───────────────────────────────────────────────────────────────────

func TestPoolAdd(t *testing.T) {
	p := NewPool()
	s1 := newMockSession("s1")
	s2 := newMockSession("s2")

	idx1 := p.Add("first", s1)
	if idx1 != 0 {
		t.Errorf("first Add index = %d, want 0", idx1)
	}
	if p.Len() != 1 {
		t.Errorf("Len() = %d, want 1", p.Len())
	}

	idx2 := p.Add("second", s2)
	if idx2 != 1 {
		t.Errorf("second Add index = %d, want 1", idx2)
	}
	if p.Len() != 2 {
		t.Errorf("Len() = %d, want 2", p.Len())
	}
}

// ── Tests: Switch ────────────────────────────────────────────────────────────────

func TestPoolSwitch(t *testing.T) {
	p := NewPool()
	s1 := newMockSession("s1")
	s2 := newMockSession("s2")
	p.Add("first", s1)
	p.Add("second", s2)

	if p.CurrentIndex() != 0 {
		t.Errorf("CurrentIndex() = %d, want 0", p.CurrentIndex())
	}

	if err := p.Switch(1); err != nil {
		t.Fatalf("Switch(1) error = %v", err)
	}
	if p.CurrentIndex() != 1 {
		t.Errorf("CurrentIndex() = %d, want 1", p.CurrentIndex())
	}
	if p.Current().SessionID() != "s2" {
		t.Errorf("Current().SessionID() = %q, want %q", p.Current().SessionID(), "s2")
	}
	if p.CurrentLabel() != "second" {
		t.Errorf("CurrentLabel() = %q, want %q", p.CurrentLabel(), "second")
	}

	if err := p.Switch(0); err != nil {
		t.Fatalf("Switch(0) error = %v", err)
	}
	if p.Current().SessionID() != "s1" {
		t.Errorf("Current().SessionID() = %q, want %q", p.Current().SessionID(), "s1")
	}
	if p.CurrentLabel() != "first" {
		t.Errorf("CurrentLabel() = %q, want %q", p.CurrentLabel(), "first")
	}
}

func TestPoolSwitchOutOfRange(t *testing.T) {
	p := NewPool()
	p.Add("only", newMockSession("s1"))

	if err := p.Switch(-1); err == nil {
		t.Error("Switch(-1) should error")
	}
	if err := p.Switch(1); err == nil {
		t.Error("Switch(1) should error for pool with 1 session")
	}
	if err := p.Switch(100); err == nil {
		t.Error("Switch(100) should error")
	}
}

// ── Tests: Current ───────────────────────────────────────────────────────────────

func TestPoolCurrentEmpty(t *testing.T) {
	p := NewPool()
	if s := p.Current(); s != nil {
		t.Errorf("Current() = %v, want nil", s)
	}
	if label := p.CurrentLabel(); label != "" {
		t.Errorf("CurrentLabel() = %q, want empty", label)
	}
}

// ── Tests: CurrentLabel ──────────────────────────────────────────────────────────

func TestPoolCurrentLabel(t *testing.T) {
	p := NewPool()
	p.Add("alpha", newMockSession("s1"))
	p.Add("beta", newMockSession("s2"))

	if label := p.CurrentLabel(); label != "alpha" {
		t.Errorf("CurrentLabel() = %q, want %q", label, "alpha")
	}

	p.Switch(1)
	if label := p.CurrentLabel(); label != "beta" {
		t.Errorf("CurrentLabel() after switch = %q, want %q", label, "beta")
	}
}

// ── Tests: CurrentIndex ──────────────────────────────────────────────────────────

func TestPoolCurrentIndex(t *testing.T) {
	p := NewPool()
	p.Add("a", newMockSession("s1"))
	p.Add("b", newMockSession("s2"))

	if idx := p.CurrentIndex(); idx != 0 {
		t.Errorf("CurrentIndex() = %d, want 0", idx)
	}

	p.Switch(1)
	if idx := p.CurrentIndex(); idx != 1 {
		t.Errorf("CurrentIndex() after switch = %d, want 1", idx)
	}
}

func TestPoolCurrentIndexStartsAtZero(t *testing.T) {
	p := NewPool()
	p.Add("a", newMockSession("s1"))
	p.Add("b", newMockSession("s2"))

	if idx := p.CurrentIndex(); idx != 0 {
		t.Errorf("CurrentIndex() = %d, want 0", idx)
	}
	if label := p.CurrentLabel(); label != "a" {
		t.Errorf("CurrentLabel() = %q, want %q", label, "a")
	}
}

// ── Tests: Len ───────────────────────────────────────────────────────────────────

func TestPoolLen(t *testing.T) {
	p := NewPool()
	if l := p.Len(); l != 0 {
		t.Errorf("Len() = %d, want 0", l)
	}
	p.Add("a", newMockSession("s1"))
	if l := p.Len(); l != 1 {
		t.Errorf("Len() = %d, want 1", l)
	}
	p.Add("b", newMockSession("s2"))
	if l := p.Len(); l != 2 {
		t.Errorf("Len() = %d, want 2", l)
	}
}

// ── Tests: All / Summary ─────────────────────────────────────────────────────────

func TestPoolAll(t *testing.T) {
	p := NewPool()
	s1 := newMockSession("s1")
	s2 := newMockSession("s2")
	p.Add("first", s1)
	p.Add("second", s2)

	all := p.All()
	if len(all) != 2 {
		t.Fatalf("All() length = %d, want 2", len(all))
	}

	if all[0].Index != 0 || all[0].Label != "first" || all[0].SessionID != "s1" {
		t.Errorf("first summary mismatch: %+v", all[0])
	}
	if !all[0].IsCurrent {
		t.Error("first should be current")
	}
	if all[1].IsCurrent {
		t.Error("second should not be current")
	}

	p.Switch(1)
	all = p.All()
	if !all[1].IsCurrent {
		t.Error("second should be current after switch")
	}
	if all[0].IsCurrent {
		t.Error("first should not be current after switch")
	}
}

func TestPoolAllEmpty(t *testing.T) {
	p := NewPool()
	all := p.All()
	if all == nil {
		t.Fatal("All() should return empty slice, not nil")
	}
	if len(all) != 0 {
		t.Errorf("All() length = %d, want 0", len(all))
	}
}

// ── Tests: Chat ──────────────────────────────────────────────────────────────────

func TestPoolChat(t *testing.T) {
	p := NewPool()
	p.Add("default", newMockSession("s1"))

	result, err := p.Chat(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if !strings.HasPrefix(result, "reply:") {
		t.Errorf("Chat() result = %q, want prefix 'reply:'", result)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("Chat() result = %q, should contain 'hello'", result)
	}
}

func TestPoolChatEmptyPool(t *testing.T) {
	p := NewPool()
	_, err := p.Chat(context.Background(), "hello")
	if err == nil {
		t.Fatal("Chat() should error on empty pool")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want 'empty'", err.Error())
	}
}

func TestPoolChatSwitchedSession(t *testing.T) {
	p := NewPool()
	p.Add("first", newMockSession("s1"))
	p.Add("second", newMockSession("s2"))

	p.Switch(1)
	result, err := p.Chat(context.Background(), "test")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if !strings.Contains(result, "test") {
		t.Errorf("result = %q, should contain 'test'", result)
	}
}

func TestPoolPreservesHistory(t *testing.T) {
	p := NewPool()
	p.Add("s", newMockSession("s1"))

	p.Chat(context.Background(), "first message")
	p.Chat(context.Background(), "second message")

	s := p.Current()
	history := s.History()
	if len(history) != 4 {
		t.Errorf("History length = %d, want 4 (2 user + 2 assistant)", len(history))
	}
}

// ── Tests: ChatStream ────────────────────────────────────────────────────────────

func TestPoolChatStream(t *testing.T) {
	p := NewPool()
	p.Add("default", newMockSession("s1"))

	var chunks []string
	result, err := p.ChatStream(context.Background(), "hello", func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if !strings.HasPrefix(result, "stream:") {
		t.Errorf("ChatStream() result = %q, want prefix 'stream:'", result)
	}
	if len(chunks) == 0 {
		t.Error("expected at least one chunk")
	}
	if chunks[0] != "stream:hello" {
		t.Errorf("chunk = %q, want %q", chunks[0], "stream:hello")
	}
}

func TestPoolChatStreamEmptyPool(t *testing.T) {
	p := NewPool()
	_, err := p.ChatStream(context.Background(), "hello", func(string) {})
	if err == nil {
		t.Fatal("ChatStream() should error on empty pool")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error = %q, want 'empty'", err.Error())
	}
}

// ── Tests: Switch Boundaries ─────────────────────────────────────────────────────

func TestPoolSwitchMaintainsBoundaries(t *testing.T) {
	p := NewPool()
	for i := 0; i < 5; i++ {
		p.Add("s", newMockSession("s"))
	}

	for _, idx := range []int{0, 4, 2, 3, 1} {
		if err := p.Switch(idx); err != nil {
			t.Errorf("Switch(%d) error = %v", idx, err)
		}
	}

	for _, idx := range []int{-5, -1, 5, 10} {
		if err := p.Switch(idx); err == nil {
			t.Errorf("Switch(%d) should error", idx)
		}
	}
}
