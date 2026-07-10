package workplan

import "context"

// =============================================================================
// Tracer 内部接口 —— 可观测性追踪
// =============================================================================
//
// workplan 包定义自己的 Tracer/Span 接口，不直接导入 contexts/tracer 包，
// 保持零外部依赖。上层（engine）通过适配器将 tracer.Tracer 转换为本接口，
// 或实现此接口直接注入。

// SpanKind 标识 span 的类型。
type SpanKind string

const (
	// SpanWorkPlan 是 WorkPlan 执行根 span 的 kind。
	SpanWorkPlan SpanKind = "workplan"
	// SpanNode 是单个节点执行 span 的 kind。
	SpanNode SpanKind = "node"
)

// Span 是追踪的基本单位。
// StartSpan 返回 Span，生命周期由调用方控制，完成后必须调用 End。
type Span interface {
	// End 结束 span，可选的 SpanOption 用于传递错误或设置属性。
	End(opts ...SpanOption)

	// SetAttr 设置 span 属性（可在 span 生命周期内任意时刻调用）。
	SetAttr(key, value string)
}

// SpanOption 配置 span 的结束行为。
type SpanOption func(attrs map[string]string)

// WithSpanError 标记 span 为错误状态，并记录错误信息。
func WithSpanError(err error) SpanOption {
	return func(attrs map[string]string) {
		if err != nil {
			attrs["error"] = err.Error()
		}
	}
}

// Tracer 是可观测性的核心接口。
type Tracer interface {
	// NewTrace 创建新的追踪根 span。
	// traceID 是全局唯一标识（如 execID）。
	// 返回的 context 包含根 span 信息，Span 用于结束根 span。
	NewTrace(ctx context.Context, traceID string) (context.Context, Span)

	// StartSpan 在当前追踪中创建指定 span 的子 span。
	// ctx 必须包含由 NewTrace 或 StartSpan 设置的 span 信息。
	// 返回的 context 包含新 span 信息，Span 用于结束新 span。
	StartSpan(ctx context.Context, name string, kind SpanKind, attrs map[string]string) (context.Context, Span)
}
