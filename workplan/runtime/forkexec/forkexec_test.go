package forkexec

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

func TestRunRecoversPanicAndCancelsSibling(t *testing.T) {
	blockerStarted := make(chan struct{}, 1)
	panicGate := make(chan struct{})
	var eventsMu sync.Mutex
	events := make(map[BranchState]bool)
	coordinator := Coordinator{
		MaxConcurrent: 2,
		OnEvent: func(event Event) {
			eventsMu.Lock()
			events[event.Type] = true
			eventsMu.Unlock()
		},
	}

	done := make(chan struct {
		results []Result
		err     error
	}, 1)
	go func() {
		results, err := coordinator.Run(context.Background(), types.NewWorkflowContext(), []Spec{
			{
				ID: "blocker", NodeID: "blocker",
				Execute: func(ctx context.Context, _ *BranchContext) (string, error) {
					blockerStarted <- struct{}{}
					<-ctx.Done()
					return "", ctx.Err()
				},
			},
			{
				ID: "panicker", NodeID: "panicker",
				Execute: func(context.Context, *BranchContext) (string, error) {
					<-panicGate
					panic("boom")
				},
			},
		})
		done <- struct {
			results []Result
			err     error
		}{results, err}
	}()

	<-blockerStarted
	close(panicGate)
	outcome := <-done
	if outcome.err == nil {
		t.Fatal("Run() error = nil, want panic failure")
	}
	if outcome.results[0].State != StateCanceled {
		t.Errorf("blocker state = %q, want canceled", outcome.results[0].State)
	}
	if outcome.results[1].State != StatePanicked {
		t.Errorf("panicker state = %q, want panicked", outcome.results[1].State)
	}
	for _, event := range []BranchState{StateQueued, StateStarted, StateCanceled, StatePanicked} {
		if !events[event] {
			t.Errorf("missing %q event", event)
		}
	}
}

func TestBestEffortJoinUsesStableBranchIDs(t *testing.T) {
	coordinator := Coordinator{Policy: PolicyBestEffort}
	parent := types.NewWorkflowContext()
	results, err := coordinator.Run(context.Background(), parent, []Spec{
		{ID: "zeta", NodeID: "zeta", Execute: func(context.Context, *BranchContext) (string, error) { return "", errors.New("failed") }},
		{ID: "alpha", NodeID: "alpha", Execute: func(context.Context, *BranchContext) (string, error) { return "ok", nil }},
	})
	if err != nil {
		t.Fatalf("best-effort Run() error = %v", err)
	}
	if results[0].ID != "alpha" || results[1].ID != "zeta" {
		t.Errorf("results are not sorted by stable branch ID: %#v", results)
	}
	if err := coordinator.Join(parent, results); err != nil {
		t.Fatalf("best-effort Join() error = %v", err)
	}
	if parent.PrevResults["alpha"] != `"ok"` {
		t.Errorf("successful branch result = %q, want ok", parent.PrevResults["alpha"])
	}
	if _, ok := parent.PrevResults["zeta"]; ok {
		t.Error("failed best-effort branch was written to PrevResults")
	}
}
