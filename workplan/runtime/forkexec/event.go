package forkexec

import (
	"context"
	"encoding/json"
	"sync"

	frameworkevent "github.com/RedHuang-0622/Seele/event"
)

// lifecycleObserver preserves the legacy ForkCoordinator callback while
// projecting the same transition into the root event contract when a Recorder
// is attached to the execution context.
type lifecycleObserver struct {
	coordinator *ForkCoordinator
	ctx         context.Context
	recorder    *frameworkevent.Recorder

	mu         sync.Mutex
	heartbeats map[string]frameworkevent.HeartbeatLease
}

func newLifecycleObserver(ctx context.Context, coordinator *ForkCoordinator) *lifecycleObserver {
	return &lifecycleObserver{
		coordinator: coordinator,
		ctx:         ctx,
		recorder:    frameworkevent.RecorderFromContext(ctx),
		heartbeats:  make(map[string]frameworkevent.HeartbeatLease),
	}
}

func (o *lifecycleObserver) emit(branchEvent Event, output string) {
	o.coordinator.emit(branchEvent)
	if o.recorder == nil {
		return
	}

	scope := frameworkevent.Scope{NodeID: branchEvent.NodeID, BranchID: branchEvent.BranchID}
	locator := frameworkevent.LocatorFunc(func() frameworkevent.Location {
		ids := make(map[string]string, 2)
		if branchEvent.NodeID != "" {
			ids["node_id"] = branchEvent.NodeID
		}
		if branchEvent.BranchID != "" {
			ids["branch_id"] = branchEvent.BranchID
		}
		return frameworkevent.Location{Kind: "workplan.node", IDs: ids}
	})

	if isTerminal(branchEvent.Type) {
		o.stopHeartbeat(branchEvent)
	}

	standard := frameworkevent.Event{
		Source:     "workplan.forkexec",
		Type:       frameworkevent.TypeLifecycle,
		Status:     statusFromBranchState(branchEvent.Type),
		Scope:      scope,
		Locations:  []frameworkevent.Location{locator.Locate()},
		OccurredAt: branchEvent.At,
		Failure:    frameworkevent.FailureFrom(branchEvent.Err),
	}
	if output != "" {
		standard.Content = append(json.RawMessage(nil), []byte(output)...)
	}
	o.recorder.Publish(o.ctx, standard)

	if branchEvent.Type == StateStarted {
		o.startHeartbeat(branchEvent, scope, locator)
	}
}

func (o *lifecycleObserver) startHeartbeat(branchEvent Event, scope frameworkevent.Scope, locator frameworkevent.Locator) {
	lease := o.recorder.StartHeartbeat(o.ctx, scope, map[string]string{
		"event_source": "workplan.forkexec",
	}, locator)
	o.mu.Lock()
	o.heartbeats[heartbeatKey(branchEvent)] = lease
	o.mu.Unlock()
}

func (o *lifecycleObserver) stopHeartbeat(branchEvent Event) {
	o.mu.Lock()
	lease, ok := o.heartbeats[heartbeatKey(branchEvent)]
	delete(o.heartbeats, heartbeatKey(branchEvent))
	o.mu.Unlock()
	if ok {
		lease.Stop()
	}
}

func heartbeatKey(event Event) string { return event.NodeID + "\x00" + event.BranchID }

func isTerminal(state BranchState) bool {
	switch state {
	case StateCompleted, StateFailed, StateCanceled, StatePanicked:
		return true
	default:
		return false
	}
}

func statusFromBranchState(state BranchState) frameworkevent.Status {
	switch state {
	case StateQueued:
		return frameworkevent.StatusQueued
	case StateStarted:
		return frameworkevent.StatusRunning
	case StateCompleted:
		return frameworkevent.StatusCompleted
	case StateFailed:
		return frameworkevent.StatusFailed
	case StateCanceled:
		return frameworkevent.StatusCanceled
	case StatePanicked:
		return frameworkevent.StatusPanicked
	default:
		return frameworkevent.StatusFailed
	}
}
