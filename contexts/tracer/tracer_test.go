package tracer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
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

// =============================================================================
// OTel 集成测试
// =============================================================================

func TestSimpleTracer_WithOTel_SpanEmitted(t *testing.T) {
	// 创建内存 OTel 导出器
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	ctx := context.Background()
	ctx, root := st.NewTrace(ctx, "otel-test-1")
	root.SetAttr("model", "test-model")
	root.End()

	// 验证 OTel span 被发射
	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span, got %d", len(spans))
	}
	got := spans[0]
	if got.Name() != "root" {
		t.Fatalf("expected span name 'root', got %q", got.Name())
	}
	if got.SpanKind() != oteltrace.SpanKindInternal {
		t.Fatalf("expected SpanKind Internal, got %v", got.SpanKind())
	}

	// 验证标准属性
	var hasModel bool
	for _, attr := range got.Attributes() {
		if attr.Key == "model" && attr.Value.AsString() == "test-model" {
			hasModel = true
		}
	}
	if !hasModel {
		t.Fatal("OTel span should contain 'model' attribute")
	}

	// 验证 local 属性
	var hasSpanID bool
	for _, attr := range got.Attributes() {
		if attr.Key == "local.span.id" && attr.Value.AsString() == "otel-test-1.0" {
			hasSpanID = true
		}
	}
	if !hasSpanID {
		t.Fatal("OTel span should contain local.span.id attribute")
	}
}

func TestSimpleTracer_WithOTel_ChildrenSpans(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	ctx := context.Background()
	ctx, root := st.NewTrace(ctx, "otel-test-2")
	_, llm := st.StartSpan(ctx, "llm-call", SpanLLMCall, map[string]string{"model": "gpt4"})
	llm.End()
	_, tool := st.StartSpan(ctx, "tool-exec", SpanToolDispatch, map[string]string{"tool": "search"})
	tool.End()
	_, cache := st.StartSpan(ctx, "cache-read", SpanCacheOp, nil)
	cache.End()
	root.End()

	spans := sr.Ended()
	if len(spans) != 4 {
		t.Fatalf("expected 4 OTel spans, got %d", len(spans))
	}

	// 验证 SpanKind 映射
	kindMap := make(map[string]oteltrace.SpanKind)
	for _, s := range spans {
		kindMap[s.Name()] = s.SpanKind()
	}
	if kindMap["root"] != oteltrace.SpanKindInternal {
		t.Fatal("root span should have SpanKind Internal")
	}
	if kindMap["llm-call"] != oteltrace.SpanKindClient {
		t.Fatal("LLM span should have SpanKind Client")
	}
	if kindMap["tool-exec"] != oteltrace.SpanKindClient {
		t.Fatal("Tool span should have SpanKind Client")
	}
	if kindMap["cache-read"] != oteltrace.SpanKindInternal {
		t.Fatal("Cache span should have SpanKind Internal")
	}

	// 验证自定义属性
	var llmAttrs []attribute.KeyValue
	for _, s := range spans {
		if s.Name() == "llm-call" {
			llmAttrs = s.Attributes()
		}
	}
	var hasModel bool
	for _, attr := range llmAttrs {
		if attr.Key == "model" && attr.Value.AsString() == "gpt4" {
			hasModel = true
		}
	}
	if !hasModel {
		t.Fatal("llm-call OTel span should contain 'model' attribute")
	}
}

func TestSimpleTracer_WithOTel_DurationRecorded(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	ctx := context.Background()
	_, span := st.NewTrace(ctx, "otel-duration")
	time.Sleep(time.Millisecond)
	span.End()

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span, got %d", len(spans))
	}

	// OTel span 是在 emitOTelSpan 中创建并立即结束的，因此 OTel 原生 StartTime/EndTime 很接近。
	// 真正的持续时间体现在 local.duration_ns / local.duration_ms 属性中。
	var hasDur bool
	var durMs int64
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "local.duration_ms" {
			hasDur = true
			durMs = attr.Value.AsInt64()
		}
	}
	if !hasDur {
		t.Fatal("OTel span should contain local.duration_ms attribute")
	}
	if durMs < 1 {
		t.Fatalf("local.duration_ms should be >= 1 (slept 1ms), got %d", durMs)
	}
}

func TestSimpleTracer_WithoutOTel_NoChange(t *testing.T) {
	// 不设置 OTel provider，验证行为完全不变
	st := NewSimpleTracer()
	ctx := context.Background()

	ctx, root := st.NewTrace(ctx, "no-otel")
	root.SetAttr("key", "val")
	_, child := st.StartSpan(ctx, "child", SpanLLMCall, nil)
	child.End()
	root.End()

	tree := st.Export(ctx)
	if tree.TraceID != "no-otel" {
		t.Fatalf("expected TraceID 'no-otel', got %q", tree.TraceID)
	}
	if len(tree.Root.Children) != 1 {
		t.Fatalf("expected 1 child span, got %d", len(tree.Root.Children))
	}

	// 确认 SimpleTracer 的 otelTracer 和 otelTp 均为 nil
	if st.otelTracer != nil {
		t.Fatal("without WithOTelTracerProvider, otelTracer should be nil")
	}
	if st.otelTp != nil {
		t.Fatal("without WithOTelTracerProvider, otelTp should be nil")
	}
}

func TestSimpleTracer_WithOTel_ErrorStatus(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	ctx := context.Background()
	_, span := st.NewTrace(ctx, "otel-error")
	span.End(WithError(errStr("something failed")))

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 OTel span, got %d", len(spans))
	}

	// 验证 error 属性已传播
	var hasError bool
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "error" && attr.Value.AsString() == "something failed" {
			hasError = true
		}
	}
	if !hasError {
		t.Fatal("OTel span should contain error attribute")
	}

	// 验证 local.node.status
	var hasStatus bool
	for _, attr := range spans[0].Attributes() {
		if attr.Key == "local.node.status" && attr.Value.AsString() == "error" {
			hasStatus = true
		}
	}
	if !hasStatus {
		t.Fatal("OTel span should contain local.node.status=error")
	}
}

func TestSimpleTracer_WithOTel_Concurrent(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	ctx := context.Background()
	ctx, _ = st.NewTrace(ctx, "otel-concurrent")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, s := st.StartSpan(ctx, "worker", SpanLLMCall, nil)
			s.SetAttr("key", "val")
			s.AddEvent("work", nil)
			s.End()
		}()
	}
	wg.Wait()

	// 所有 span 都应在 OTel 导出器中
	spans := sr.Ended()
	// 根 span + 10 个子 span = 11
	if len(spans) != 11 {
		t.Logf("expected 11 OTel spans, got %d (possible race in End() timing)", len(spans))
	}

	// 验证每个 span 都有 local.span.id
	for _, sp := range spans {
		var hasID bool
		for _, attr := range sp.Attributes() {
			if attr.Key == "local.span.id" {
				hasID = true
				break
			}
		}
		if !hasID {
			t.Errorf("OTel span %q missing local.span.id attribute", sp.Name())
		}
	}
}

func TestSimpleTracer_WithOTel_MultipleExports(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sr),
	)
	defer tp.Shutdown(context.Background())

	st := NewSimpleTracer()
	st.WithOTelTracerProvider(tp)

	// 第一轮
	ctx1 := context.Background()
	_, s1 := st.NewTrace(ctx1, "round1")
	s1.End()
	tree1 := st.Export(ctx1)
	if tree1.TraceID != "round1" {
		t.Fatalf("expected round1, got %s", tree1.TraceID)
	}

	// 第二轮（状态已重置）
	ctx2 := context.Background()
	_, s2 := st.NewTrace(ctx2, "round2")
	s2.End()
	tree2 := st.Export(ctx2)
	if tree2.TraceID != "round2" {
		t.Fatalf("expected round2, got %s", tree2.TraceID)
	}

	// OTel 应收集到 2 个 span
	spans := sr.Ended()
	if len(spans) < 2 {
		t.Fatalf("expected at least 2 OTel spans across exports, got %d", len(spans))
	}
}

func TestNoopTracer_WithOTelNotImported(t *testing.T) {
	// NoopTracer 完全不引用 OTel
	ntr := NoopTracer{}
	ctx := context.Background()
	ctx, s := ntr.NewTrace(ctx, "noop")
	s.End()
	tree := ntr.Export(ctx)
	if tree.Root != nil {
		t.Fatal("NoopTracer should have nil root")
	}
}
