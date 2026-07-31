package telemetry

import (
	"context"
	"errors"
)

// NoopTracer preserves propagation contracts while discarding all telemetry.
type NoopTracer struct{}

func (NoopTracer) StartTrace(ctx context.Context, _ string, _ SpanKind, _ Attributes) (context.Context, Span, error) {
	traceID, err := newIdentifier(16)
	if err != nil {
		return ctx, nil, err
	}
	spanID, err := newIdentifier(8)
	if err != nil {
		return ctx, nil, err
	}
	span := noopSpan{traceID: traceID, spanID: spanID}
	return ContextWithTrace(ctx, TraceContext{TraceID: traceID, SpanID: spanID}), span, nil
}

func (NoopTracer) StartSpan(ctx context.Context, _ string, _ SpanKind, _ Attributes) (context.Context, Span, error) {
	parent, ok := TraceFromContext(ctx)
	if !ok {
		return ctx, nil, errors.New("start noop span: trace context is missing")
	}
	spanID, err := newIdentifier(8)
	if err != nil {
		return ctx, nil, err
	}
	span := noopSpan{traceID: parent.TraceID, spanID: spanID, parentID: parent.SpanID}
	trace := TraceContext{TraceID: parent.TraceID, SpanID: spanID, ParentSpanID: parent.SpanID}
	return ContextWithTrace(ctx, trace), span, nil
}

func (NoopTracer) Record(context.Context, Event) error { return nil }

type noopSpan struct {
	traceID  string
	spanID   string
	parentID string
}

func (s noopSpan) TraceID() string                     { return s.traceID }
func (s noopSpan) ID() string                          { return s.spanID }
func (s noopSpan) ParentID() string                    { return s.parentID }
func (noopSpan) SetAttributes(Attributes)              {}
func (noopSpan) AddEvent(context.Context, Event) error { return nil }
func (noopSpan) End(context.Context, Status, error)    {}

var _ Tracer = NoopTracer{}
