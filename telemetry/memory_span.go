package telemetry

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type memorySpan struct {
	tracer  *MemoryTracer
	traceID string
	spanID  string
	ended   atomic.Bool
}

func (s *memorySpan) TraceID() string { return s.traceID }
func (s *memorySpan) ID() string      { return s.spanID }

func (s *memorySpan) ParentID() string {
	s.tracer.mu.RLock()
	defer s.tracer.mu.RUnlock()
	if trace := s.tracer.traces[s.traceID]; trace != nil {
		if span := trace.spans[s.spanID]; span != nil {
			return span.parentID
		}
	}
	return ""
}

func (s *memorySpan) SetAttributes(attributes Attributes) {
	if len(attributes) == 0 || s.ended.Load() {
		return
	}
	s.tracer.mu.Lock()
	defer s.tracer.mu.Unlock()
	trace := s.tracer.traces[s.traceID]
	if trace == nil || trace.spans[s.spanID] == nil {
		return
	}
	span := trace.spans[s.spanID]
	if span.attributes == nil {
		span.attributes = make(Attributes)
	}
	for key, value := range attributes {
		span.attributes[key] = value
	}
}

func (s *memorySpan) AddEvent(ctx context.Context, event Event) error {
	if event.TraceID == "" {
		event.TraceID = s.traceID
	}
	if event.SpanID == "" {
		event.SpanID = s.spanID
	}
	if event.ParentSpanID == "" {
		event.ParentSpanID = s.ParentID()
	}
	return s.tracer.Record(ctx, event)
}

func (s *memorySpan) End(ctx context.Context, status Status, err error) {
	if !s.ended.CompareAndSwap(false, true) {
		return
	}
	if err != nil {
		status = StatusError
	} else if status == "" || status == StatusUnset {
		status = StatusOK
	}
	var snapshot *TraceSnapshot
	var duration Metric
	s.tracer.mu.Lock()
	trace := s.tracer.traces[s.traceID]
	if trace == nil || trace.spans[s.spanID] == nil {
		s.tracer.mu.Unlock()
		return
	}
	span := trace.spans[s.spanID]
	span.endedAt = s.tracer.clock()
	if span.endedAt.Before(span.startedAt) {
		span.endedAt = span.startedAt
	}
	span.status = status
	span.err = errorInfo(err)
	duration = Metric{
		Timestamp: span.endedAt,
		TraceID:   span.traceID,
		SpanID:    span.id,
		Name:      MetricSpanDuration,
		Value:     float64(span.endedAt.Sub(span.startedAt).Nanoseconds()) / float64(time.Millisecond),
		Unit:      "ms",
		Attributes: Attributes{
			"span.name": span.name,
			"span.kind": string(span.kind),
		},
	}
	traceSinks := append([]TraceSink(nil), s.tracer.traceSinks...)
	if trace.rootID == span.id {
		copySnapshot := snapshotTrace(s.traceID, trace)
		snapshot = &copySnapshot
	}
	s.tracer.mu.Unlock()
	_ = s.tracer.RecordMetric(ctx, duration)
	if snapshot != nil {
		for _, sink := range traceSinks {
			_ = sink.StoreTrace(ctx, *snapshot)
		}
	}
}

type memorySubscription struct {
	owner   *MemoryTracer
	id      uint64
	query   Query
	events  chan Event
	dropped atomic.Uint64
	once    sync.Once
}

func (s *memorySubscription) Events() <-chan Event { return s.events }
func (s *memorySubscription) Dropped() uint64      { return s.dropped.Load() }

func (s *memorySubscription) Close() {
	s.once.Do(func() {
		s.owner.mu.Lock()
		if _, exists := s.owner.subscribers[s.id]; exists {
			delete(s.owner.subscribers, s.id)
			close(s.events)
		}
		s.owner.mu.Unlock()
	})
}

func snapshotTrace(traceID string, trace *memoryTrace) TraceSnapshot {
	operations := make([]Operation, 0, len(trace.operations))
	for _, operation := range trace.operations {
		copyOperation := *operation
		if operation.Intent != nil {
			intent := cloneEvent(*operation.Intent)
			copyOperation.Intent = &intent
		}
		if operation.Effect != nil {
			effect := cloneEvent(*operation.Effect)
			copyOperation.Effect = &effect
		}
		operations = append(operations, copyOperation)
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].CorrelationID < operations[j].CorrelationID })
	return TraceSnapshot{
		TraceID:    traceID,
		Root:       snapshotSpan(trace, trace.rootID),
		Operations: operations,
	}
}

func snapshotSpan(trace *memoryTrace, spanID string) SpanSnapshot {
	span := trace.spans[spanID]
	if span == nil {
		return SpanSnapshot{}
	}
	snapshot := SpanSnapshot{
		TraceID:      span.traceID,
		SpanID:       span.id,
		ParentSpanID: span.parentID,
		Name:         span.name,
		Kind:         span.kind,
		StartedAt:    span.startedAt,
		EndedAt:      span.endedAt,
		Status:       span.status,
		Error:        span.err,
		Attributes:   cloneAttributes(span.attributes),
	}
	for _, event := range span.events {
		snapshot.Events = append(snapshot.Events, cloneEvent(event))
	}
	for _, childID := range span.children {
		snapshot.Children = append(snapshot.Children, snapshotSpan(trace, childID))
	}
	sort.Slice(snapshot.Children, func(i, j int) bool {
		left, right := snapshot.Children[i], snapshot.Children[j]
		if left.StartedAt.Equal(right.StartedAt) {
			return left.SpanID < right.SpanID
		}
		return left.StartedAt.Before(right.StartedAt)
	})
	return snapshot
}

func matchesEvent(event Event, query Query) bool {
	if query.TraceID != "" && event.TraceID != query.TraceID {
		return false
	}
	if query.SpanID != "" && event.SpanID != query.SpanID {
		return false
	}
	if query.CorrelationID != "" && event.CorrelationID != query.CorrelationID {
		return false
	}
	if query.Status != "" && event.Status != query.Status {
		return false
	}
	if !query.From.IsZero() && event.Timestamp.Before(query.From) {
		return false
	}
	if !query.Until.IsZero() && event.Timestamp.After(query.Until) {
		return false
	}
	if len(query.EventTypes) > 0 {
		matched := false
		for _, eventType := range query.EventTypes {
			if event.Type == eventType {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return matchesAttributes(event.Attributes, query.Attributes)
}

func matchesMetric(metric Metric, query Query) bool {
	if query.TraceID != "" && metric.TraceID != query.TraceID {
		return false
	}
	if query.SpanID != "" && metric.SpanID != query.SpanID {
		return false
	}
	if !query.From.IsZero() && metric.Timestamp.Before(query.From) {
		return false
	}
	if !query.Until.IsZero() && metric.Timestamp.After(query.Until) {
		return false
	}
	return matchesAttributes(metric.Attributes, query.Attributes)
}

func matchesAttributes(attributes Attributes, filters map[string]string) bool {
	for key, expected := range filters {
		if fmt.Sprint(attributes[key]) != expected {
			return false
		}
	}
	return true
}

func metricsFromEvent(event Event) []Metric {
	metrics := make([]Metric, 0, 2)
	for key, name := range map[string]string{
		AttributeGenAIUsageInput:  MetricGenAIInputTokens,
		AttributeGenAIUsageOutput: MetricGenAIOutputTokens,
	} {
		if value, ok := numericValue(event.Attributes[key]); ok {
			metrics = append(metrics, Metric{
				Timestamp: event.Timestamp,
				TraceID:   event.TraceID,
				SpanID:    event.SpanID,
				Name:      name,
				Value:     value,
				Unit:      "{token}",
			})
		}
	}
	return metrics
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func applyLimit(view *ViewModel, limit int) {
	if limit <= 0 {
		return
	}
	if len(view.Events) > limit {
		view.Events = view.Events[:limit]
	}
	if len(view.Metrics) > limit {
		view.Metrics = view.Metrics[:limit]
	}
	if len(view.Audits) > limit {
		view.Audits = view.Audits[:limit]
	}
}
