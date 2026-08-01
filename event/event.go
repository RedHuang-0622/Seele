// Package event defines the framework-wide, product-neutral observation
// contract. It records immutable execution facts; it does not own logging,
// persistence, task state, or business semantics.
package event

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"time"
)

// Type classifies an observation without assigning product meaning to it.
type Type string

const (
	TypeLifecycle Type = "lifecycle"
	TypeProgress  Type = "progress"
	TypeHeartbeat Type = "heartbeat"
	TypeFault     Type = "fault"
)

// Status describes the state represented by an event.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusPanicked  Status = "panicked"
)

// Scope correlates an event with framework runtime resources. Empty fields
// are intentionally allowed because not every module has every identity.
type Scope struct {
	TraceID    string `json:"trace_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	PlanID     string `json:"plan_id,omitempty"`
	NodeID     string `json:"node_id,omitempty"`
	BranchID   string `json:"branch_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// Location is an extensible, serializable resource address. The Kind is a
// module-owned stable name such as "workplan.node" or "agent.runtime"; IDs
// are module-owned identifiers. Event never interprets either value.
type Location struct {
	Kind string            `json:"kind"`
	IDs  map[string]string `json:"ids"`
}

// Locator lets a module compose its own typed runtime address into a generic
// Event. Agent, WorkPlan, and future modules may define their own locator
// structs without changing the event package.
type Locator interface {
	Locate() Location
}

// LocatorFunc adapts a function to Locator.
type LocatorFunc func() Location

// Locate implements Locator.
func (f LocatorFunc) Locate() Location {
	if f == nil {
		return Location{}
	}
	return f()
}

// Event is the stable, JSON-serializable record delivered to a Sink.
// Content is optional JSON data. Large or sensitive data should be stored by
// the caller and represented through ContentRef instead.
type Event struct {
	ID         string            `json:"id"`
	Sequence   uint64            `json:"sequence"`
	OccurredAt time.Time         `json:"occurred_at"`
	Source     string            `json:"source"`
	Type       Type              `json:"type"`
	Status     Status            `json:"status"`
	Scope      Scope             `json:"scope"`
	Locations  []Location        `json:"locations,omitempty"`
	Content    json.RawMessage   `json:"content,omitempty"`
	ContentRef string            `json:"content_ref,omitempty"`
	Failure    *Failure          `json:"failure,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// JSON encodes an event payload without coupling event to a product-specific
// value type.
func JSON(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// Sink receives already-normalized events. A Sink may persist, forward, or
// log events, but must not mutate the supplied record.
type Sink interface {
	Append(ctx context.Context, event Event) error
}

// SinkFunc adapts a function to Sink.
type SinkFunc func(context.Context, Event) error

// Append implements Sink.
func (f SinkFunc) Append(ctx context.Context, event Event) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

// NoopSink discards all events. It is useful for optional composition.
type NoopSink struct{}

// Append implements Sink.
func (NoopSink) Append(context.Context, Event) error { return nil }

// MultiSink delivers an event to every configured Sink in order. It attempts
// all sinks even when one fails and returns the joined delivery error.
type MultiSink []Sink

// Append implements Sink.
func (s MultiSink) Append(ctx context.Context, event Event) error {
	var errs []error
	for _, sink := range s {
		if sink == nil {
			continue
		}
		if err := sink.Append(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return stdErrors.Join(errs...)
}

func mergeScope(base, override Scope) Scope {
	if override.TraceID != "" {
		base.TraceID = override.TraceID
	}
	if override.RunID != "" {
		base.RunID = override.RunID
	}
	if override.PlanID != "" {
		base.PlanID = override.PlanID
	}
	if override.NodeID != "" {
		base.NodeID = override.NodeID
	}
	if override.BranchID != "" {
		base.BranchID = override.BranchID
	}
	if override.AgentID != "" {
		base.AgentID = override.AgentID
	}
	if override.ToolCallID != "" {
		base.ToolCallID = override.ToolCallID
	}
	return base
}

func cloneEvent(event Event) Event {
	event.Content = append(json.RawMessage(nil), event.Content...)
	if event.Failure != nil {
		failure := *event.Failure
		event.Failure = &failure
	}
	if event.Attributes != nil {
		attributes := event.Attributes
		event.Attributes = make(map[string]string, len(attributes))
		for key, value := range attributes {
			event.Attributes[key] = value
		}
	}
	event.Locations = cloneLocations(event.Locations)
	return event
}

func locationsFrom(locators []Locator) []Location {
	locations := make([]Location, 0, len(locators))
	for _, locator := range locators {
		if locator == nil {
			continue
		}
		location := locator.Locate()
		if location.Kind == "" {
			continue
		}
		locations = append(locations, cloneLocation(location))
	}
	return locations
}

func cloneLocations(source []Location) []Location {
	if source == nil {
		return nil
	}
	clone := make([]Location, len(source))
	for index, location := range source {
		clone[index] = cloneLocation(location)
	}
	return clone
}

func cloneLocation(source Location) Location {
	clone := Location{Kind: source.Kind}
	if source.IDs != nil {
		clone.IDs = make(map[string]string, len(source.IDs))
		for key, value := range source.IDs {
			clone.IDs[key] = value
		}
	}
	return clone
}
