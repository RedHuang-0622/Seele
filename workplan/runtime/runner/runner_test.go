package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	frameworkevent "github.com/RedHuang-0622/Seele/event"
	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/checkpoint"
)

type eventCapture struct {
	mu         sync.Mutex
	events     []frameworkevent.Event
	heartbeats chan struct{}
}

func (c *eventCapture) Append(_ context.Context, event frameworkevent.Event) error {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	if event.Type == frameworkevent.TypeHeartbeat && event.Scope.NodeID == "start" {
		select {
		case c.heartbeats <- struct{}{}:
		default:
		}
	}
	return nil
}

func (c *eventCapture) Events() []frameworkevent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]frameworkevent.Event(nil), c.events...)
}

type mockNode struct {
	node.BaseNode
	runFn func(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

type rawValueNode struct {
	node.BaseNode
	value types.Value
}

func newRawValueNode(id, raw string) *rawValueNode {
	return &rawValueNode{
		BaseNode: node.NewBaseNode(id, node.KindMethod),
		value:    types.RawValue(raw),
	}
}

func (n *rawValueNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return n.value.RawString(), nil
}

func (n *rawValueNode) RunValue(context.Context, *types.WorkflowContext) (types.Value, error) {
	return n.value, nil
}

func newMockNode(id string, kind node.NodeKind) *mockNode {
	return &mockNode{BaseNode: node.NewBaseNode(id, kind)}
}

func (m *mockNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if m.runFn != nil {
		return m.runFn(ctx, wc)
	}
	return "", nil
}

func TestNew(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.plan != g {
		t.Error("plan not set on runner")
	}
	if r.sched == nil {
		t.Error("scheduler not initialized")
	}
	if r.exec == nil {
		t.Error("executor not initialized")
	}
	if r.checkMgr != nil {
		t.Error("expected nil checkMgr without WithCheckpoint")
	}
}

func TestNewWithPlan(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	if r.Plan() != g {
		t.Fatal("runner must retain the supplied plan")
	}
}

func TestWithCheckpointOption(t *testing.T) {
	g := coreplan.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))
	if r.checkMgr == nil {
		t.Fatal("expected checkMgr to be set")
	}

	// Verify the checkpoint manager works
	wc := types.NewWorkflowContext()
	_, err := r.checkMgr.Save("test", wc)
	if err != nil {
		t.Fatalf("checkpoint save failed: %v", err)
	}
}

func TestRunExecutesPlan(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	r := New(g)
	result, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "end" {
		t.Errorf("expected second node 'end', got %q", result.NodeResults[1].NodeID)
	}
}

func TestRunEmitsNormalizedEventsAndHeartbeat(t *testing.T) {
	g := coreplan.New()
	capture := &eventCapture{heartbeats: make(chan struct{}, 1)}
	start := newMockNode("start", node.KindMethod)
	start.runFn = func(ctx context.Context, _ *types.WorkflowContext) (string, error) {
		select {
		case <-capture.heartbeats:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	g.AddNode(start)
	g.SetEntry("start")

	r := New(g,
		WithEventSink(capture, "plan-1"),
		WithEventRunID("run-1"),
		WithEventHeartbeatPolicy(frameworkevent.HeartbeatPolicy{Interval: time.Millisecond}),
		WithEventLocators(frameworkevent.LocatorFunc(func() frameworkevent.Location {
			return frameworkevent.Location{Kind: "agent.runtime", IDs: map[string]string{"agent_id": "agent-1"}}
		})),
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := r.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinalOutputString() != "done" {
		t.Fatalf("final output = %q", result.FinalOutputString())
	}

	events := capture.Events()
	if len(events) < 5 {
		t.Fatalf("event count = %d, want plan + node lifecycle + heartbeat", len(events))
	}
	seenHeartbeat := false
	seenNodeCompletion := false
	for index, event := range events {
		if event.ID == "" || event.Sequence != uint64(index+1) {
			t.Fatalf("event[%d] identity = %#v", index, event)
		}
		if event.Scope.PlanID != "plan-1" || event.Scope.RunID != "run-1" {
			t.Fatalf("event[%d] scope = %#v", index, event.Scope)
		}
		if event.Type == frameworkevent.TypeHeartbeat {
			seenHeartbeat = event.Status == frameworkevent.StatusRunning && event.Scope.NodeID == "start"
		}
		if event.Status == frameworkevent.StatusCompleted && event.Scope.NodeID == "start" {
			seenNodeCompletion = true
		}
		if len(event.Locations) == 0 || event.Locations[0].Kind != "agent.runtime" {
			t.Fatalf("event[%d] locations = %#v", index, event.Locations)
		}
	}
	if !seenHeartbeat || !seenNodeCompletion {
		t.Fatalf("heartbeat=%t nodeCompletion=%t events=%#v", seenHeartbeat, seenNodeCompletion, events)
	}
}

func TestRunRejectsEventSinkWithoutPlanID(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.SetEntry("start")
	r := New(g, WithEventSink(frameworkevent.NoopSink{}, ""))
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("Run should reject an event sink without a plan ID")
	}
}

func TestRunContinuesAfterEventSinkFailure(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.SetEntry("start")

	deliveryErrors := make(chan error, 1)
	r := New(g,
		WithEventSink(frameworkevent.SinkFunc(func(context.Context, frameworkevent.Event) error {
			return errors.New("event store unavailable")
		}), "plan-1"),
		WithEventErrorHandler(func(_ context.Context, err error) {
			select {
			case deliveryErrors <- err:
			default:
			}
		}),
	)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run must not fail because the event sink failed: %v", err)
	}
	select {
	case <-deliveryErrors:
	case <-time.After(time.Second):
		t.Fatal("expected event delivery error handler invocation")
	}
}

func TestRunValidatesPlan(t *testing.T) {
	g := coreplan.New()
	// Plan has an entry set to a node that does not exist.
	g.SetEntry("nonexistent")
	g.AddNode(newMockNode("start", node.KindMethod))

	r := New(g)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected validation error for missing entry node, got nil")
	}
}

func TestRunDetectsCycle(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("a", node.KindMethod))
	g.AddNode(newMockNode("b", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})
	g.AddEdge(edge.Edge{From: "b", To: "a"}) // cycle

	r := New(g)
	_, err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected cycle validation error, got nil")
	}
}

func TestPlanAccessor(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	returned := r.Plan()
	if returned != g {
		t.Error("Plan() returned a different Plan instance")
	}
}

func TestResumeFromCheckpoint(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newMockNode("start", node.KindMethod))
	g.AddNode(newMockNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "end"})

	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))

	// Manually save a checkpoint for "start"
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"checkpoint-value"`
	r.checkMgr.Save("start", wc)

	// Resume from checkpoint
	result, err := r.Resume(context.Background(), "start")
	if err != nil {
		t.Fatalf("Resume failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Should execute "start" and "end"
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "end" {
		t.Errorf("expected second node 'end', got %q", result.NodeResults[1].NodeID)
	}
}

func TestResumeNormalizesRawValueEventContent(t *testing.T) {
	g := coreplan.New()
	g.AddNode(newRawValueNode("start", "not-json"))
	g.SetEntry("start")

	store := checkpoint.NewMemoryStore()
	capture := &eventCapture{}
	r := New(g, WithCheckpoint(store), WithEventSink(capture, "plan-1"))
	if _, err := r.checkMgr.Save("start", types.NewWorkflowContext()); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}
	if _, err := r.Resume(context.Background(), "start"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	for _, event := range capture.Events() {
		if event.Status == frameworkevent.StatusCompleted && event.Scope.NodeID == "start" {
			if !json.Valid(event.Content) {
				t.Fatalf("node content must be JSON: %q", event.Content)
			}
			if string(event.Content) != `"not-json"` {
				t.Fatalf("node content = %s", event.Content)
			}
			return
		}
	}
	t.Fatal("missing completed event for resumed node")
}

func TestResumeWithoutCheckpoint(t *testing.T) {
	g := coreplan.New()
	r := New(g)
	_, err := r.Resume(context.Background(), "some-id")
	if err == nil {
		t.Fatal("expected error when checkpoint not enabled, got nil")
	}
}

func TestResumeWithMissingSnapshot(t *testing.T) {
	g := coreplan.New()
	store := checkpoint.NewMemoryStore()
	r := New(g, WithCheckpoint(store))

	_, err := r.Resume(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}
