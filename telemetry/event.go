// Package telemetry provides product-neutral hooks, tracing, metrics, and audit contracts.
package telemetry

import (
	"errors"
	"fmt"
	"time"
)

// Attributes contains structured event and span metadata.
// Values should be JSON-serializable scalar values or slices of scalar values.
type Attributes map[string]any

// Status is the normalized outcome of an event or span.
type Status string

const (
	StatusUnset Status = "unset"
	StatusOK    Status = "ok"
	StatusError Status = "error"
)

// Phase identifies whether an event describes intent, effect, or an instant fact.
type Phase string

const (
	PhaseInstant Phase = "instant"
	PhaseBefore  Phase = "before"
	PhaseAfter   Phase = "after"
)

// EventType is a stable, machine-readable lifecycle event name.
type EventType string

const (
	EventAgentStart            EventType = "agent.start"
	EventAgentEnd              EventType = "agent.end"
	EventLLMBefore             EventType = "llm.before"
	EventLLMAfter              EventType = "llm.after"
	EventToolBefore            EventType = "tool.before"
	EventToolAfter             EventType = "tool.after"
	EventHandoffBefore         EventType = "handoff.before"
	EventHandoffAfter          EventType = "handoff.after"
	EventContextAssembleBefore EventType = "context.assemble.before"
	EventContextAssembleAfter  EventType = "context.assemble.after"
	EventContextCompressBefore EventType = "context.compress.before"
	EventContextCompressAfter  EventType = "context.compress.after"
	EventError                 EventType = "error"
)

// ErrorInfo is a serializable error representation suitable for audit records.
type ErrorInfo struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
}

// Event is the common structured payload exchanged by hooks and tracers.
type Event struct {
	Timestamp     time.Time  `json:"timestamp"`
	Type          EventType  `json:"type"`
	Phase         Phase      `json:"phase"`
	Name          string     `json:"name,omitempty"`
	TraceID       string     `json:"trace_id"`
	SpanID        string     `json:"span_id"`
	ParentSpanID  string     `json:"parent_span_id,omitempty"`
	CorrelationID string     `json:"correlation_id,omitempty"`
	Attributes    Attributes `json:"attributes,omitempty"`
	Status        Status     `json:"status"`
	Error         *ErrorInfo `json:"error,omitempty"`
}

// Operation joins an intent event and its eventual effect by correlation ID.
type Operation struct {
	CorrelationID string `json:"correlation_id"`
	Intent        *Event `json:"intent,omitempty"`
	Effect        *Event `json:"effect,omitempty"`
	Status        Status `json:"status"`
}

// Validate checks the fields required for correlation and storage.
func (e Event) Validate() error {
	var errs []error
	if e.Type == "" {
		errs = append(errs, errors.New("event type is required"))
	}
	if e.TraceID == "" {
		errs = append(errs, errors.New("trace ID is required"))
	}
	if e.SpanID == "" {
		errs = append(errs, errors.New("span ID is required"))
	}
	if e.Phase == PhaseBefore || e.Phase == PhaseAfter {
		if e.CorrelationID == "" {
			errs = append(errs, errors.New("correlation ID is required for before/after events"))
		}
	}
	return errors.Join(errs...)
}

func phaseForEvent(eventType EventType) Phase {
	switch eventType {
	case EventAgentStart, EventLLMBefore, EventToolBefore, EventHandoffBefore, EventContextAssembleBefore, EventContextCompressBefore:
		return PhaseBefore
	case EventAgentEnd, EventLLMAfter, EventToolAfter, EventHandoffAfter, EventContextAssembleAfter, EventContextCompressAfter:
		return PhaseAfter
	default:
		return PhaseInstant
	}
}

func matchingAfter(eventType EventType) (EventType, error) {
	switch eventType {
	case EventAgentStart:
		return EventAgentEnd, nil
	case EventLLMBefore:
		return EventLLMAfter, nil
	case EventToolBefore:
		return EventToolAfter, nil
	case EventHandoffBefore:
		return EventHandoffAfter, nil
	case EventContextAssembleBefore:
		return EventContextAssembleAfter, nil
	case EventContextCompressBefore:
		return EventContextCompressAfter, nil
	default:
		return "", fmt.Errorf("event type %q has no matching after event", eventType)
	}
}

func cloneAttributes(src Attributes) Attributes {
	if len(src) == 0 {
		return nil
	}
	dst := make(Attributes, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func cloneEvent(src Event) Event {
	dst := src
	dst.Attributes = cloneAttributes(src.Attributes)
	if src.Error != nil {
		errCopy := *src.Error
		dst.Error = &errCopy
	}
	return dst
}
