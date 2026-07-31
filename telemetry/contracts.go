package telemetry

import (
	"context"
	"time"
)

// SpanKind follows OpenTelemetry's execution-role model while retaining agent-specific names.
type SpanKind string

const (
	SpanInternal SpanKind = "internal"
	SpanServer   SpanKind = "server"
	SpanClient   SpanKind = "client"
	SpanAgent    SpanKind = "agent"
	SpanLLM      SpanKind = "llm"
	SpanTool     SpanKind = "tool"
)

// Span is a single unit of work. End must be safe to call more than once.
type Span interface {
	TraceID() string
	ID() string
	ParentID() string
	SetAttributes(attributes Attributes)
	AddEvent(ctx context.Context, event Event) error
	End(ctx context.Context, status Status, err error)
}

// Tracer starts trace trees and records structured lifecycle events.
type Tracer interface {
	StartTrace(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error)
	StartSpan(ctx context.Context, name string, kind SpanKind, attributes Attributes) (context.Context, Span, error)
	Record(ctx context.Context, event Event) error
}

// Metric represents a time-series sample. Token counts use unit "{token}".
type Metric struct {
	Timestamp  time.Time  `json:"timestamp"`
	TraceID    string     `json:"trace_id,omitempty"`
	SpanID     string     `json:"span_id,omitempty"`
	Name       string     `json:"name"`
	Value      float64    `json:"value"`
	Unit       string     `json:"unit,omitempty"`
	Attributes Attributes `json:"attributes,omitempty"`
}

// AuditRecord is an append-only structured event envelope.
type AuditRecord struct {
	Sequence uint64 `json:"sequence"`
	Event    Event  `json:"event"`
}

// TraceSink stores completed or explicitly exported trace snapshots.
type TraceSink interface {
	StoreTrace(ctx context.Context, trace TraceSnapshot) error
}

// MetricSink stores time-series measurements independently from trace storage.
type MetricSink interface {
	RecordMetric(ctx context.Context, metric Metric) error
}

// AuditSink stores append-only lifecycle events for compliance or inspection.
type AuditSink interface {
	AppendAudit(ctx context.Context, record AuditRecord) error
}

// MetricRecorder is an optional capability implemented by tracers that accept direct metrics.
type MetricRecorder interface {
	RecordMetric(ctx context.Context, metric Metric) error
}

// EventSink is the smallest callback contract for hook fan-out.
type EventSink interface {
	Emit(ctx context.Context, event Event) error
}

// EventSinkFunc adapts a callback into an EventSink.
type EventSinkFunc func(context.Context, Event) error

func (f EventSinkFunc) Emit(ctx context.Context, event Event) error { return f(ctx, event) }

// TraceSnapshot is a queryable tree plus its correlated intent/effect operations.
type TraceSnapshot struct {
	TraceID    string       `json:"trace_id"`
	Root       SpanSnapshot `json:"root"`
	Operations []Operation  `json:"operations,omitempty"`
}

// SpanSnapshot is an immutable view of a span tree.
type SpanSnapshot struct {
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Name         string         `json:"name"`
	Kind         SpanKind       `json:"kind"`
	StartedAt    time.Time      `json:"started_at"`
	EndedAt      time.Time      `json:"ended_at,omitempty"`
	Status       Status         `json:"status"`
	Error        *ErrorInfo     `json:"error,omitempty"`
	Attributes   Attributes     `json:"attributes,omitempty"`
	Events       []Event        `json:"events,omitempty"`
	Children     []SpanSnapshot `json:"children,omitempty"`
}

// Query filters trace events, metrics, and audit records for a view.
type Query struct {
	TraceID       string
	SpanID        string
	CorrelationID string
	EventTypes    []EventType
	Status        Status
	From          time.Time
	Until         time.Time
	Attributes    map[string]string
	Limit         int
}

// ViewModel is intentionally UI-neutral and supports waterfall/status renderers.
type ViewModel struct {
	Traces  []TraceSnapshot `json:"traces,omitempty"`
	Events  []Event         `json:"events,omitempty"`
	Metrics []Metric        `json:"metrics,omitempty"`
	Audits  []AuditRecord   `json:"audits,omitempty"`
}

// Queryer exposes read-only visualization and debugging projections.
type Queryer interface {
	Query(ctx context.Context, query Query) (ViewModel, error)
}

// Subscription is a bounded real-time stream. Slow consumers may observe Dropped > 0.
type Subscription interface {
	Events() <-chan Event
	Dropped() uint64
	Close()
}

// Streamer exposes filtered real-time events without prescribing a UI transport.
type Streamer interface {
	Subscribe(ctx context.Context, query Query) (Subscription, error)
}
