package workplan

import "context"

type SpanKind string

const (
	SpanWorkPlan SpanKind = "workplan"
	SpanNode     SpanKind = "node"
)

type Span interface {
	End(opts ...SpanOption)
	SetAttr(key, value string)
}

type SpanOption func(attrs map[string]string)

func WithSpanError(err error) SpanOption {
	return func(attrs map[string]string) {
		if err != nil {
			attrs["error"] = err.Error()
		}
	}
}

type Tracer interface {
	NewTrace(ctx context.Context, traceID string) (context.Context, Span)
	StartSpan(ctx context.Context, name string, kind SpanKind, attrs map[string]string) (context.Context, Span)
}
