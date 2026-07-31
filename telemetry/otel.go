package telemetry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const defaultInstrumentationName = "github.com/RedHuang-0622/Seele/telemetry"

// OTelTracer adapts the telemetry Tracer contract to a native OpenTelemetry provider.
type OTelTracer struct {
	tracer oteltrace.Tracer
}

// NewOTelTracer creates an adapter without installing or mutating global OTel providers.
func NewOTelTracer(provider oteltrace.TracerProvider, instrumentationName string) (*OTelTracer, error) {
	if provider == nil {
		return nil, errors.New("OpenTelemetry tracer provider is required")
	}
	if instrumentationName == "" {
		instrumentationName = defaultInstrumentationName
	}
	return &OTelTracer{tracer: provider.Tracer(instrumentationName)}, nil
}

func (t *OTelTracer) StartTrace(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error) {
	ctx, native := t.tracer.Start(ctx, name,
		oteltrace.WithNewRoot(),
		oteltrace.WithSpanKind(otelSpanKind(kind)),
		oteltrace.WithAttributes(otelAttributes(attributes)...),
	)
	spanContext := native.SpanContext()
	trace := TraceContext{TraceID: spanContext.TraceID().String(), SpanID: spanContext.SpanID().String()}
	ctx = ContextWithTrace(ctx, trace)
	return ctx, &otelSpan{owner: t, native: native, trace: trace}, nil
}

func (t *OTelTracer) StartSpan(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error) {
	parent, ok := TraceFromContext(ctx)
	if !ok {
		spanContext := oteltrace.SpanContextFromContext(ctx)
		if !spanContext.IsValid() {
			return ctx, nil, errors.New("start OpenTelemetry span: trace context is missing")
		}
		parent = TraceContext{TraceID: spanContext.TraceID().String(), SpanID: spanContext.SpanID().String()}
	}
	ctx, native := t.tracer.Start(ctx, name,
		oteltrace.WithSpanKind(otelSpanKind(kind)),
		oteltrace.WithAttributes(otelAttributes(attributes)...),
	)
	spanContext := native.SpanContext()
	trace := TraceContext{
		TraceID:      spanContext.TraceID().String(),
		SpanID:       spanContext.SpanID().String(),
		ParentSpanID: parent.SpanID,
	}
	ctx = ContextWithTrace(ctx, trace)
	return ctx, &otelSpan{owner: t, native: native, trace: trace}, nil
}

// Record maps a structured Seele lifecycle event to an OTel span event.
func (t *OTelTracer) Record(ctx context.Context, event Event) error {
	if trace, ok := TraceFromContext(ctx); ok {
		if event.TraceID == "" {
			event.TraceID = trace.TraceID
		}
		if event.SpanID == "" {
			event.SpanID = trace.SpanID
		}
		if event.ParentSpanID == "" {
			event.ParentSpanID = trace.ParentSpanID
		}
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.Phase == "" {
		event.Phase = phaseForEvent(event.Type)
	}
	if event.Status == "" {
		event.Status = StatusUnset
	}
	event = withSemanticDefaults(event)
	if err := event.Validate(); err != nil {
		return fmt.Errorf("record OpenTelemetry event: %w", err)
	}
	native := oteltrace.SpanFromContext(ctx)
	if !native.SpanContext().IsValid() {
		return errors.New("record OpenTelemetry event: active native span is missing")
	}
	spanContext := native.SpanContext()
	if event.TraceID != spanContext.TraceID().String() || event.SpanID != spanContext.SpanID().String() {
		return fmt.Errorf("record OpenTelemetry event: event trace/span %s/%s does not match active span %s/%s",
			event.TraceID, event.SpanID, spanContext.TraceID(), spanContext.SpanID())
	}
	native.AddEvent(string(event.Type),
		oteltrace.WithTimestamp(event.Timestamp),
		oteltrace.WithAttributes(otelAttributes(event.Attributes)...),
	)
	if event.Error != nil {
		err := errors.New(event.Error.Message)
		native.RecordError(err, oteltrace.WithTimestamp(event.Timestamp))
		native.SetStatus(codes.Error, event.Error.Message)
	} else if event.Status == StatusOK {
		native.SetStatus(codes.Ok, "")
	}
	return nil
}

type otelSpan struct {
	owner  *OTelTracer
	native oteltrace.Span
	trace  TraceContext
}

func (s *otelSpan) TraceID() string  { return s.trace.TraceID }
func (s *otelSpan) ID() string       { return s.trace.SpanID }
func (s *otelSpan) ParentID() string { return s.trace.ParentSpanID }

func (s *otelSpan) SetAttributes(attributes Attributes) {
	s.native.SetAttributes(otelAttributes(attributes)...)
}

func (s *otelSpan) AddEvent(ctx context.Context, event Event) error {
	if event.TraceID == "" {
		event.TraceID = s.trace.TraceID
	}
	if event.SpanID == "" {
		event.SpanID = s.trace.SpanID
	}
	if event.ParentSpanID == "" {
		event.ParentSpanID = s.trace.ParentSpanID
	}
	return s.owner.Record(ctx, event)
}

func (s *otelSpan) End(_ context.Context, status Status, err error) {
	if err != nil {
		s.native.RecordError(err)
		s.native.SetStatus(codes.Error, err.Error())
	} else if status == StatusError {
		s.native.SetStatus(codes.Error, "operation failed")
	} else if status == StatusOK {
		s.native.SetStatus(codes.Ok, "")
	}
	s.native.End()
}

func otelSpanKind(kind SpanKind) oteltrace.SpanKind {
	switch kind {
	case SpanServer:
		return oteltrace.SpanKindServer
	case SpanClient, SpanLLM, SpanTool:
		return oteltrace.SpanKindClient
	default:
		return oteltrace.SpanKindInternal
	}
}

func otelAttributes(attributes Attributes) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attributes))
	for key, value := range attributes {
		result = append(result, otelAttribute(key, value))
	}
	return result
}

func otelAttribute(key string, value any) attribute.KeyValue {
	switch typed := value.(type) {
	case string:
		return attribute.String(key, typed)
	case bool:
		return attribute.Bool(key, typed)
	case int:
		return attribute.Int(key, typed)
	case int32:
		return attribute.Int64(key, int64(typed))
	case int64:
		return attribute.Int64(key, typed)
	case uint:
		return attribute.Int64(key, int64(typed))
	case uint32:
		return attribute.Int64(key, int64(typed))
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return attribute.Int64(key, int64(typed))
		}
		return attribute.String(key, fmt.Sprint(typed))
	case float32:
		return attribute.Float64(key, float64(typed))
	case float64:
		return attribute.Float64(key, typed)
	case []string:
		return attribute.StringSlice(key, typed)
	case []bool:
		return attribute.BoolSlice(key, typed)
	case []int:
		return attribute.IntSlice(key, typed)
	case []int64:
		return attribute.Int64Slice(key, typed)
	case []float64:
		return attribute.Float64Slice(key, typed)
	default:
		return attribute.String(key, fmt.Sprint(value))
	}
}

var _ Tracer = (*OTelTracer)(nil)
