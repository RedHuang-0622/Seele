package event

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	seeleerrors "github.com/RedHuang-0622/Seele/errors"
)

type collectedSink struct {
	mu      sync.Mutex
	events  []Event
	onEvent func(Event)
}

func (s *collectedSink) Append(_ context.Context, event Event) error {
	s.mu.Lock()
	s.events = append(s.events, cloneEvent(event))
	onEvent := s.onEvent
	s.mu.Unlock()
	if onEvent != nil {
		onEvent(event)
	}
	return nil
}

func (s *collectedSink) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.events...)
}

func TestRecorderNormalizesScopeLocationsAndSequence(t *testing.T) {
	sink := &collectedSink{}
	ids := 0
	recorder, err := NewRecorder(sink,
		WithSource("workplan"),
		WithScope(Scope{PlanID: "plan-1", RunID: "run-1"}),
		WithLocators(LocatorFunc(func() Location {
			return Location{Kind: "agent.runtime", IDs: map[string]string{"agent_id": "agent-1"}}
		})),
		WithIDGenerator(IDGeneratorFunc(func() (string, error) {
			ids++
			return "id-" + string(rune('0'+ids)), nil
		})),
	)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer recorder.Close()

	recorder.Publish(context.Background(), Event{
		Type: TypeLifecycle, Status: StatusRunning,
		Scope:     Scope{NodeID: "inspect"},
		Locations: []Location{{Kind: "workplan.node", IDs: map[string]string{"node_id": "inspect"}}},
	})
	recorder.Publish(context.Background(), Event{Type: TypeLifecycle, Status: StatusCompleted})

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("events=%d, want 2", len(events))
	}
	if events[0].ID != "evt_id-1" || events[1].ID != "evt_id-2" {
		t.Fatalf("event IDs = %q, %q", events[0].ID, events[1].ID)
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("sequences = %d, %d", events[0].Sequence, events[1].Sequence)
	}
	if events[0].Scope.PlanID != "plan-1" || events[0].Scope.RunID != "run-1" || events[0].Scope.NodeID != "inspect" {
		t.Fatalf("scope = %+v", events[0].Scope)
	}
	if len(events[0].Locations) != 2 || events[0].Locations[0].Kind != "agent.runtime" || events[0].Locations[1].Kind != "workplan.node" {
		t.Fatalf("locations = %#v", events[0].Locations)
	}
}

func TestHeartbeatEmitsForActiveScope(t *testing.T) {
	heartbeats := make(chan Event, 1)
	sink := &collectedSink{onEvent: func(event Event) {
		if event.Type == TypeHeartbeat {
			select {
			case heartbeats <- event:
			default:
			}
		}
	}}
	recorder, err := NewRecorder(sink,
		WithScope(Scope{PlanID: "plan-1", RunID: "run-1"}),
		WithHeartbeatPolicy(HeartbeatPolicy{Interval: time.Millisecond}),
	)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer recorder.Close()

	lease := recorder.StartHeartbeat(context.Background(), Scope{NodeID: "inspect"}, map[string]string{"attempt": "1"})
	defer lease.Stop()

	select {
	case heartbeat := <-heartbeats:
		if heartbeat.Status != StatusRunning || heartbeat.Scope.NodeID != "inspect" {
			t.Fatalf("heartbeat = %#v", heartbeat)
		}
		if heartbeat.Attributes["attempt"] != "1" {
			t.Fatalf("attributes = %#v", heartbeat.Attributes)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for heartbeat")
	}
}

func TestFailureFromDoesNotExposeRawOrCause(t *testing.T) {
	err := seeleerrors.Wrap(errors.New("provider rejected request"), seeleerrors.Context{
		Code: "provider.rejected", Path: "$.model", Raw: map[string]string{"token": "secret"},
	})
	failure := FailureFrom(err)
	if failure == nil {
		t.Fatal("FailureFrom returned nil")
	}
	if failure.Code != "provider.rejected" || failure.Path != "$.model" {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestMultiSinkDeliversToAllSinks(t *testing.T) {
	first := &collectedSink{}
	second := &collectedSink{}
	if err := (MultiSink{first, second}).Append(context.Background(), Event{ID: "event-1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(first.Events()) != 1 || len(second.Events()) != 1 {
		t.Fatalf("delivery counts = %d, %d", len(first.Events()), len(second.Events()))
	}
}
