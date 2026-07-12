// Package tracer 提供 Seele 的可观测性追踪能力。
//
// 核心概念：Trace Tree（追踪树）
//
//	Trace: 一次 Chat / ChatStream 的完整执行链路
//	  └─ Node: 一个执行步骤（span），是树的一个节点
//	        ├─ kind="react_loop"   — 根节点，整体 ReAct 循环
//	        ├─ kind="llm_call"     — 单次 LLM 调用
//	        ├─ kind="tool_dispatch" — 单次工具调度
//	        └─ kind="cache_op"     — 缓存读写操作
//
// 用法：
//
//	ctx, span := tracer.StartSpan(ctx, "llm_call", SpanLLMCall, attrs)
//	defer span.End()
//
// 线程安全：SimpleTracer 所有方法支持并发调用。
// 零开销：默认 NoopTracer，所有方法空实现，编译器可内联消除。
package tracer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// =============================================================================
// SpanKind — span 的类型常量
// =============================================================================

// SpanKind 标识 span 的类型，用于区分不同的执行步骤。
type SpanKind string

const (
	SpanReActLoop    SpanKind = "react_loop"
	SpanLLMCall      SpanKind = "llm_call"
	SpanToolDispatch SpanKind = "tool_dispatch"
	SpanCacheOp      SpanKind = "cache_op"
)

// =============================================================================
// SpanStatus — span 的结束状态
// =============================================================================

// SpanStatus 标识 span 的结束状态。
type SpanStatus string

const (
	StatusOK    SpanStatus = "ok"
	StatusError SpanStatus = "error"
)

// =============================================================================
// Event — span 内部的注解事件
// =============================================================================

// Event 是 span 内部在某个时间点产生的注解。
// 用于记录 span 生命周期中的关键中间状态（如：tool_call 接收、流式 chunk 到达）。
type Event struct {
	Time  time.Time         `json:"time"`
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// =============================================================================
// Node — 追踪树的节点
// =============================================================================

// Node 是追踪树中的一个节点，代表一次执行步骤。
//
// 通过 ParentID 确定父子关系，Export 时构建 Children 切片形成树结构。
type Node struct {
	ID       string            `json:"id"`
	ParentID string            `json:"parent_id,omitempty"`
	Name     string            `json:"name"`
	Kind     SpanKind          `json:"kind"`
	Start    time.Time         `json:"start"`
	End      time.Time         `json:"end,omitempty"`
	Duration time.Duration     `json:"duration_ns,omitempty"`
	Status   SpanStatus        `json:"status"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Events   []Event           `json:"events,omitempty"`
	Children []*Node           `json:"children,omitempty"` // Export 时构建
}

// =============================================================================
// Tree — 完整的追踪树
// =============================================================================

// Tree 是一次完整执行的全链路追踪树。
// 由 SimpleTracer.Export() 生成，可序列化为 JSON 供外部系统消费。
type Tree struct {
	TraceID string `json:"trace_id"`
	Root    *Node  `json:"root"`
}

// String 返回美化后的 JSON 字符串。
func (t *Tree) String() string {
	if t == nil || t.Root == nil {
		return "{}"
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return string(b)
}

// =============================================================================
// Span 接口 — 可观测性跨度
// =============================================================================

// Span 是追踪的基本单位。
// StartSpan 返回 Span，生命周期由调用方控制，完成后必须调用 End。
type Span interface {
	// End 结束 span，记录结束时间和状态。
	// 可选的 SpanOption 用于传递错误或设置属性。
	End(opts ...SpanOption)

	// ID 返回 span 的唯一标识。
	ID() string

	// SetAttr 设置 span 属性（可在 span 生命周期内任意时刻调用）。
	// 属性用于记录关键维度数据（如 token 计数、模型名、工具名）。
	SetAttr(key, value string)

	// AddEvent 在 span 上添加一个注解事件。
	// 用于记录 span 内部的中间状态（非耗时操作，而是标记事件）。
	AddEvent(name string, attrs map[string]string)
}

// =============================================================================
// SpanOption — End 的可选参数
// =============================================================================

// SpanOption 配置 span 的结束行为。
type SpanOption func(*Node)

// WithError 标记 span 为错误状态，并记录错误信息。
// 无论 err 是否为 nil，span 都会被标记。err 为 nil 时标记为 ok。
func WithError(err error) SpanOption {
	return func(n *Node) {
		if err != nil {
			n.Status = StatusError
			if n.Attrs == nil {
				n.Attrs = make(map[string]string)
			}
			n.Attrs["error"] = err.Error()
		}
	}
}

// WithAttr 为 span 添加一个额外属性（在 End 时设置）。
// 与 Span.SetAttr 功能相同，用于 defer 场景中绑定延迟计算的属性。
func WithAttr(key, value string) SpanOption {
	return func(n *Node) {
		if n.Attrs == nil {
			n.Attrs = make(map[string]string)
		}
		n.Attrs[key] = value
	}
}

// =============================================================================
// Tracer 接口 — 追踪提供者
// =============================================================================

// Tracer 是可观测性的核心接口。
//
// 注册到 Engine 后自动埋点，无需业务代码额外调用。
// 多种实现可互换：
//   - NoopTracer — 零开销，适用于生产环境无追踪需求时
//   - SimpleTracer — JSON 格式输出，适用于开发和调试
type Tracer interface {
	// NewTrace 创建新的追踪根 span。
	// traceID 是全局唯一标识（如 sessionID）。
	// 返回的 context 包含根 span 信息，Span 用于结束根 span。
	NewTrace(ctx context.Context, traceID string) (context.Context, Span)

	// StartSpan 在当前追踪中创建指定 span 的子 span。
	// ctx 必须包含由 NewTrace 或 StartSpan 设置的 span 信息。
	// 返回的 context 包含新 span 信息，Span 用于结束新 span。
	StartSpan(ctx context.Context, name string, kind SpanKind, attrs map[string]string) (context.Context, Span)

	// Export 导出完整的 trace tree 并重置内部状态。
	// 每次 Chat/ChatStream 结束后调用一次。
	Export(ctx context.Context) *Tree
}

// =============================================================================
// noopSpan — Span 的空实现（编译器零开销）
// =============================================================================

type noopSpan struct{}

func (noopSpan) End(_ ...SpanOption)                        {}
func (noopSpan) ID() string                                 { return "" }
func (noopSpan) SetAttr(_, _ string)                        {}
func (noopSpan) AddEvent(_ string, _ map[string]string)     {}

// =============================================================================
// NoopTracer — Tracer 的空实现（编译器零开销）
// =============================================================================

// NoopTracer 是 Tracer 的空实现。
// 所有方法空实现，调用方无需 nil 检查。编译器可将所有调用优化为零开销。
type NoopTracer struct{}

// 编译期检查 NoopTracer 和 *SimpleTracer 满足 Tracer 接口。
var (
	_ Tracer = NoopTracer{}
	_ Tracer = (*SimpleTracer)(nil)
)

func (NoopTracer) NewTrace(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (NoopTracer) StartSpan(ctx context.Context, _ string, _ SpanKind, _ map[string]string) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (NoopTracer) Export(_ context.Context) *Tree {
	return &Tree{}
}

// =============================================================================
// SimpleTracer — 基于内存的追踪实现
// =============================================================================

// SimpleTracer 是 Tracer 的简单实现。
//
// 特性：
//   - 内存存储：所有 span 保存在 map 中，Export 时构建树
//   - 线程安全：sync.Mutex 保护所有写操作
//   - JSON 导出：Tree.String() 输出美化后的 JSON
//   - 自动重置：Export 后清空内部状态
//
// 每个 span 的 ID 格式：{traceID}.{seq}
// 根 span 的 ID 固定为 {traceID}.0
// 子 span 按创建顺序递增 seq。
type SimpleTracer struct {
	mu      sync.Mutex
	traceID string
	spans   map[string]*Node
	rootID  string
	seq     int

	// OTel（可选）：非 nil 时在 span.End() 时额外发射 OTel span
	otelTracer  oteltrace.Tracer  // nil = 不发射 OTel
	otelTp      oteltrace.TracerProvider
}

// WithOTelTracerProvider 设置可选的 OTel TracerProvider。
// 设置后，每个 span.End() 会额外发射一个 OTel span。
// 不设置（默认）则完全不产生 OTel 开销。
func (t *SimpleTracer) WithOTelTracerProvider(tp oteltrace.TracerProvider) {
	t.otelTp = tp
	t.otelTracer = tp.Tracer("github.com/RedHuang-0622/Seele/contexts/tracer")
}

// NewSimpleTracer 创建 SimpleTracer。
func NewSimpleTracer() *SimpleTracer {
	return &SimpleTracer{
		spans: make(map[string]*Node),
	}
}

func (t *SimpleTracer) NewTrace(ctx context.Context, traceID string) (context.Context, Span) {
	t.mu.Lock()
	t.traceID = traceID
	t.seq = 0
	t.spans = make(map[string]*Node)

	spanID := traceID + ".0"
	t.rootID = spanID

	node := &Node{
		ID:     spanID,
		Name:   "root",
		Kind:   SpanReActLoop,
		Start:  time.Now(),
		Status: StatusOK,
		Attrs:  make(map[string]string),
	}
	t.spans[spanID] = node
	t.mu.Unlock()

	ctx = withSpanID(ctx, spanID)
	return ctx, &simpleSpan{tracer: t, id: spanID, ctx: ctx}
}

func (t *SimpleTracer) StartSpan(ctx context.Context, name string, kind SpanKind, attrs map[string]string) (context.Context, Span) {
	parentID := spanIDFromContext(ctx)

	t.mu.Lock()
	t.seq++
	seq := t.seq
	spanID := t.traceID + "." + fmt.Sprint(seq)

	node := &Node{
		ID:       spanID,
		ParentID: parentID,
		Name:     name,
		Kind:     kind,
		Start:    time.Now(),
		Status:   StatusOK,
	}
	if len(attrs) > 0 {
		node.Attrs = make(map[string]string, len(attrs))
		for k, v := range attrs {
			node.Attrs[k] = v
		}
	}
	t.spans[spanID] = node
	t.mu.Unlock()

	ctx = withSpanID(ctx, spanID)
	return ctx, &simpleSpan{tracer: t, id: spanID, ctx: ctx}
}

func (t *SimpleTracer) Export(_ context.Context) *Tree {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.rootID == "" {
		return &Tree{}
	}

	root, ok := t.spans[t.rootID]
	if !ok {
		return &Tree{}
	}
	// 构建 Children 树（根 span 的 End 可能还未调用，补上 end time）
	if root.End.IsZero() {
		root.End = time.Now()
		root.Duration = root.End.Sub(root.Start)
	}
	t.buildChildren(root)

	tree := &Tree{
		TraceID: t.traceID,
		Root:    root,
	}

	// 重置内部状态
	t.traceID = ""
	t.rootID = ""
	t.spans = make(map[string]*Node)
	t.seq = 0

	return tree
}

// buildChildren 递归构建 Node.Children 切片。
// 遍历所有 span，将 ParentID 匹配的 span 加入 Children 列表，按创建顺序排序。
func (t *SimpleTracer) buildChildren(parent *Node) {
	parent.Children = nil // 重置，避免多次 Export 累积
	for _, span := range t.spans {
		if span.ParentID == parent.ID && span.ID != parent.ID {
			// 子 span 的 End 可能还未调用，补上 end time
			if span.End.IsZero() {
				span.End = time.Now()
				span.Duration = span.End.Sub(span.Start)
			}
			parent.Children = append(parent.Children, span)
		}
	}
	// 按 span ID 排序（ID 后缀是递增 seq，保证创建顺序）
	// 使用 stable sort 保持同 seq 前缀的相对顺序
	sort.SliceStable(parent.Children, func(i, j int) bool {
		return parent.Children[i].ID < parent.Children[j].ID
	})
	for _, child := range parent.Children {
		t.buildChildren(child)
	}
}

// =============================================================================
// simpleSpan — SimpleTracer 的 Span 实现
// =============================================================================

type simpleSpan struct {
	tracer *SimpleTracer
	id     string
	ctx    context.Context // 用于 OTel 父 span 传播
}

func (s *simpleSpan) ID() string { return s.id }

func (s *simpleSpan) End(opts ...SpanOption) {
	s.tracer.mu.Lock()
	node, ok := s.tracer.spans[s.id]
	s.tracer.mu.Unlock()
	if !ok {
		return
	}

	now := time.Now()
	node.End = now
	node.Duration = now.Sub(node.Start)

	for _, opt := range opts {
		opt(node)
	}

	// OTel 发射（可选）：只有设置了 OTel TracerProvider 时才发射
	if s.tracer.otelTracer != nil {
		s.emitOTelSpan(node)
	}
}

// emitOTelSpan 将本地 span 发射为 OTel span（低精度模式）。
//
// 注意：这是在 span.End() 时创建并立即结束 OTel span，而非在 StartSpan 时创建。
// 这意味者 OTel 父-子关系无法通过 OTel context 传播，所有 span 均为平级。
// 如需精确的 OTel span 树，应在 StartSpan 时就创建 OTel span。
func (s *simpleSpan) emitOTelSpan(node *Node) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	// 映射 Local SpanKind → OTel SpanKind
	var spanKind oteltrace.SpanKind
	switch node.Kind {
	case SpanReActLoop:
		spanKind = oteltrace.SpanKindInternal
	case SpanLLMCall:
		spanKind = oteltrace.SpanKindClient
	case SpanToolDispatch:
		spanKind = oteltrace.SpanKindClient
	case SpanCacheOp:
		spanKind = oteltrace.SpanKindInternal
	default:
		spanKind = oteltrace.SpanKindInternal
	}

	// 将本地 Attrs 转为 OTel attributes
	attrs := make([]attribute.KeyValue, 0, len(node.Attrs)+4)
	for k, v := range node.Attrs {
		attrs = append(attrs, attribute.String(k, v))
	}
	// 补充标准属性便于在 OTel 后端检索
	attrs = append(attrs,
		attribute.String("local.span.id", node.ID),
		attribute.String("local.span.kind", string(node.Kind)),
		attribute.String("local.node.name", node.Name),
		attribute.String("local.node.status", string(node.Status)),
		attribute.String("local.duration_ns", fmt.Sprintf("%d", node.Duration)),
		attribute.Int64("local.duration_ms", node.Duration.Milliseconds()),
	)

	// 创建并立即结束 OTel span
	_, span := s.tracer.otelTracer.Start(ctx, node.Name,
		oteltrace.WithAttributes(attrs...),
		oteltrace.WithSpanKind(spanKind),
	)
	span.End()
}

func (s *simpleSpan) SetAttr(key, value string) {
	if key == "" {
		return
	}
	s.tracer.mu.Lock()
	node, ok := s.tracer.spans[s.id]
	s.tracer.mu.Unlock()
	if !ok {
		return
	}
	if node.Attrs == nil {
		node.Attrs = make(map[string]string)
	}
	node.Attrs[key] = value
}

func (s *simpleSpan) AddEvent(name string, attrs map[string]string) {
	if name == "" {
		return
	}
	s.tracer.mu.Lock()
	node, ok := s.tracer.spans[s.id]
	s.tracer.mu.Unlock()
	if !ok {
		return
	}
	node.Events = append(node.Events, Event{
		Time:  time.Now(),
		Name:  name,
		Attrs: attrs,
	})
}

// =============================================================================
// context key — 存储当前 span ID 到 context.Context
// =============================================================================

type spanIDKey struct{}

func withSpanID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, spanIDKey{}, id)
}

func spanIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(spanIDKey{}).(string)
	return id
}

// =============================================================================
// 工具函数
// =============================================================================

// RootID 返回当前 trace 的根 span ID（调试用）。
func (t *SimpleTracer) RootID() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rootID
}

// SpanCount 返回当前 trace 的 span 数量（调试用）。
func (t *SimpleTracer) SpanCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.spans)
}

// Truncate 截断字符串到指定长度。
func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// JoinAttrs 将 map 拼接成逗号分隔的 k=v 字符串。
// 用于 attrs 中存储简短的结构化信息。
func JoinAttrs(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, v := range m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}
