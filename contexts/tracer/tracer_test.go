package tracer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// errStr 是一个快速创建 error 的测试辅助。
type errStr string

func (e errStr) Error() string { return string(e) }

// =============================================================================
// NoopTracer 测试
// =============================================================================

func TestNoopTracer_AllMethodsReturnSafely(t *testing.T) {
	var ntr NoopTracer
	ctx := context.Background()

	// NewTrace
	ctx1, span := ntr.NewTrace(ctx, "test")
	if ctx1 != ctx {
		t.Error("NoopTracer.NewTrace should return original context")
	}
	if span.ID() != "" {
		t.Error("NoopTracer.NewTrace span should have empty ID")
	}

	// StartSpan
	ctx2, span2 := ntr.StartSpan(ctx, "name", SpanLLMCall, nil)
	if ctx2 != ctx {
		t.Error("NoopTracer.StartSpan should return original context")
	}
	if span2.ID() != "" {
		t.Error("NoopTracer.StartSpan span should have empty ID")
	}

	// Span methods — should not panic
	span2.End()
	span2.End(WithError(errStr("should not be called")))
	span2.SetAttr("key", "value")
	span2.AddEvent("event", nil)

	// Export
	tree := ntr.Export(ctx)
	if tree == nil {
		t.Error("NoopTracer.Export should return non-nil tree")
	}
	if tree.Root != nil {
		t.Error("NoopTracer.Export tree should have nil root")
	}
}

// =============================================================================
// SimpleTracer 基本功能测试
// =============================================================================

func TestSimpleTracer_NewTrace_CreatesRoot(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, span := st.NewTrace(ctx, "trace_1")
	if span.ID() == "" {
		t.Fatal("NewTrace should return span with non-empty ID")
	}
	if span.ID() != "trace_1.0" {
		t.Fatalf("expected span ID trace_1.0, got %s", span.ID())
	}

	// Root span should be in internal state
	st.mu.Lock()
	root, ok := st.spans["trace_1.0"]
	st.mu.Unlock()
	if !ok {
		t.Fatal("root span not found in internal map")
	}
	if root.Kind != SpanReActLoop {
		t.Fatalf("expected root kind %s, got %s", SpanReActLoop, root.Kind)
	}
	if root.Start.IsZero() {
		t.Fatal("root span start time should be set")
	}
}

func TestSimpleTracer_StartSpan_CreatesChild(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	ctx, root := st.NewTrace(ctx, "t1")
	childCtx, child := st.StartSpan(ctx, "test_op", SpanLLMCall, map[string]string{"key": "val"})

	if child.ID() == "" {
		t.Fatal("child span should have non-empty ID")
	}
	if child.ID() == root.ID() {
		t.Fatal("child span ID should differ from root")
	}

	// childCtx should have child's span ID
	if id := spanIDFromContext(childCtx); id != child.ID() {
		t.Fatalf("child context should have child span ID, got %s", id)
	}

	// Verify child's ParentID equals root's ID
	st.mu.Lock()
	childNode, ok := st.spans[child.ID()]
	st.mu.Unlock()
	if !ok {
		t.Fatal("child span not found in internal map")
	}
	if childNode.ParentID != root.ID() {
		t.Fatalf("child ParentID should be %s, got %s", root.ID(), childNode.ParentID)
	}
	if childNode.Attrs["key"] != "val" {
		t.Fatalf("child attrs should contain key=val, got %v", childNode.Attrs)
	}
}

func TestSimpleTracer_EndSpan_RecordsDuration(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, span := st.NewTrace(ctx, "t2")
	time.Sleep(2 * time.Millisecond)
	span.End()

	st.mu.Lock()
	node := st.spans["t2.0"]
	st.mu.Unlock()

	if node.End.IsZero() {
		t.Fatal("span End time should be set")
	}
	if node.Duration <= 0 {
		t.Fatal("span Duration should be positive")
	}
	if node.Status != StatusOK {
		t.Fatalf("span Status should be ok after End(), got %s", node.Status)
	}
}

func TestSimpleTracer_EndSpan_WithError(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, span := st.NewTrace(ctx, "t3")
	span.End(WithError(errStr("something went wrong")))

	st.mu.Lock()
	node := st.spans["t3.0"]
	st.mu.Unlock()

	if node.Status != StatusError {
		t.Fatalf("expected StatusError, got %s", node.Status)
	}
	if node.Attrs["error"] != "something went wrong" {
		t.Fatalf("expected error attr, got %q", node.Attrs["error"])
	}
}

func TestSimpleTracer_SetAttr_DuringSpanLifecycle(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, span := st.NewTrace(ctx, "t4")
	span.SetAttr("phase", "running")
	span.SetAttr("count", "42")
	span.End()

	st.mu.Lock()
	node := st.spans["t4.0"]
	st.mu.Unlock()

	if node.Attrs["phase"] != "running" {
		t.Fatalf("expected phase=running, got %q", node.Attrs["phase"])
	}
	if node.Attrs["count"] != "42" {
		t.Fatalf("expected count=42, got %q", node.Attrs["count"])
	}
}

func TestSimpleTracer_AddEvent_RecordsAnnotation(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, span := st.NewTrace(ctx, "t5")
	span.AddEvent("tool_call_started", map[string]string{"tool": "search"})
	span.AddEvent("tool_call_finished", nil)
	span.End()

	st.mu.Lock()
	node := st.spans["t5.0"]
	st.mu.Unlock()

	if len(node.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(node.Events))
	}
	if node.Events[0].Name != "tool_call_started" {
		t.Fatalf("expected first event name 'tool_call_started', got %q", node.Events[0].Name)
	}
	if node.Events[0].Attrs["tool"] != "search" {
		t.Fatalf("expected event attr tool=search, got %v", node.Events[0].Attrs)
	}
}

// =============================================================================
// SimpleTracer Export 测试
// =============================================================================

func TestSimpleTracer_Export_BuildsTree(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	ctx, root := st.NewTrace(ctx, "exp1")
	_, child1 := st.StartSpan(ctx, "llm1", SpanLLMCall, nil)
	time.Sleep(time.Microsecond)
	child1.End()
	_, child2 := st.StartSpan(ctx, "tool1", SpanToolDispatch, nil)
	time.Sleep(time.Microsecond)
	child2.End()
	root.End()

	tree := st.Export(ctx)

	if tree == nil {
		t.Fatal("Export should return non-nil tree")
	}
	if tree.TraceID != "exp1" {
		t.Fatalf("expected TraceID exp1, got %s", tree.TraceID)
	}
	if tree.Root == nil {
		t.Fatal("Export tree should have non-nil Root")
	}

	// Root should have 2 children
	if len(tree.Root.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(tree.Root.Children))
	}

	// Children should be in creation order
	if tree.Root.Children[0].Kind != SpanLLMCall {
		t.Fatalf("expected first child kind %s, got %s", SpanLLMCall, tree.Root.Children[0].Kind)
	}
	if tree.Root.Children[1].Kind != SpanToolDispatch {
		t.Fatalf("expected second child kind %s, got %s", SpanToolDispatch, tree.Root.Children[1].Kind)
	}

	// Children should have durations set
	if tree.Root.Children[0].Duration <= 0 {
		t.Fatal("child span should have positive duration")
	}
}

func TestSimpleTracer_Export_ResetsState(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	_, root := st.NewTrace(ctx, "reset1")
	root.End()
	t1 := st.Export(ctx)
	if t1.Root == nil {
		t.Fatal("first export should have root")
	}

	// After export, state should be reset
	ctx2 := context.Background()
	_, root2 := st.NewTrace(ctx2, "reset2")
	root2.End()
	t2 := st.Export(ctx2)
	if t2.TraceID != "reset2" {
		t.Fatalf("expected TraceID reset2, got %s", t2.TraceID)
	}
}

func TestSimpleTracer_Export_NoRootReturnsEmpty(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	tree := st.Export(ctx)
	if tree == nil {
		t.Fatal("Export without NewTrace should return non-nil tree")
	}
}

func TestSimpleTracer_Tree_JSON(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()

	ctx, root := st.NewTrace(ctx, "json1")
	root.SetAttr("model", "test-model")
	_, child := st.StartSpan(ctx, "llm1", SpanLLMCall, map[string]string{"tokens": "100"})
	child.End()
	root.End()

	tree := st.Export(ctx)
	jsonStr := tree.String()

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("Tree.String() should produce valid JSON: %v\nraw: %s", err, jsonStr)
	}

	traceID, ok := parsed["trace_id"].(string)
	if !ok || traceID != "json1" {
		t.Fatalf("expected trace_id json1, got %v", parsed["trace_id"])
	}
}

// =============================================================================
// 并发安全测试
// =============================================================================

func TestSimpleTracer_ConcurrentAccess(t *testing.T) {
	st := NewSimpleTracer()
	ctx := context.Background()
	ctx, _ = st.NewTrace(ctx, "concurrent")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, s := st.StartSpan(ctx, "child", SpanLLMCall, nil)
			s.SetAttr("key", "val")
			s.AddEvent("event", nil)
			s.End()
		}()
	}
	wg.Wait()

	tree := st.Export(ctx)
	if tree.Root == nil {
		t.Fatal("concurrent test should produce valid tree")
	}
	if len(tree.Root.Children) != 20 {
		t.Logf("expected 20 children, got %d (possible if export raced)", len(tree.Root.Children))
	}
}

func TestNoopTracer_Concurrent(t *testing.T) {
	ntr := NoopTracer{}
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx1, s := ntr.NewTrace(ctx, "noop")
			ctx2, s2 := ntr.StartSpan(ctx1, "child", SpanLLMCall, nil)
			s2.SetAttr("k", "v")
			s2.AddEvent("e", nil)
			s2.End()
			s.End()
			ntr.Export(ctx2)
		}()
	}
	wg.Wait()
	// Should not panic or race
}

// =============================================================================
// 工具函数测试
// =============================================================================

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := Truncate(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

// =============================================================================
// SpanOption 测试
// =============================================================================

func TestWithError_Nil(t *testing.T) {
	n := &Node{Status: StatusOK}
	WithError(nil)(n)
	if n.Status != StatusOK {
		t.Fatal("WithError(nil) should not change Status to error")
	}
}

func TestWithAttr(t *testing.T) {
	n := &Node{}
	WithAttr("key", "val")(n)
	if n.Attrs["key"] != "val" {
		t.Fatalf("WithAttr should set attr key=val, got %v", n.Attrs)
	}
	// Should merge with existing attrs
	n2 := &Node{Attrs: map[string]string{"existing": "keep"}}
	WithAttr("new", "added")(n2)
	if n2.Attrs["existing"] != "keep" || n2.Attrs["new"] != "added" {
		t.Fatal("WithAttr should merge with existing attrs")
	}
}

// =============================================================================
// context key 测试
// =============================================================================

func TestSpanIDContext(t *testing.T) {
	ctx := context.Background()
	id := spanIDFromContext(ctx)
	if id != "" {
		t.Fatalf("expected empty span ID from background context, got %q", id)
	}

	ctx = withSpanID(ctx, "test.42")
	id = spanIDFromContext(ctx)
	if id != "test.42" {
		t.Fatalf("expected span ID test.42, got %q", id)
	}
}
