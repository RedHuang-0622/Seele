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

func TestRunRecoversPreparePanic(t *testing.T) {
	coordinator := ForkCoordinator{}
	results, err := coordinator.Run(context.Background(), types.NewWorkflowContext(), []Spec{{
		ID: "prepare-panics", NodeID: "prepare-panics",
		Prepare: func(*BranchContext) { panic("prepare boom") },
		Execute: func(context.Context, *BranchContext) (string, error) {
			t.Fatal("Execute must not run after Prepare panic")
			return "", nil
		},
	}})
	if err == nil {
		t.Fatal("Run() error = nil, want Prepare panic failure")
	}
	if len(results) != 1 || results[0].State != StatePanicked {
		t.Fatalf("results = %#v, want panicked branch result", results)
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

func TestContextManagerFreezesParentSnapshot(t *testing.T) {
	parent := types.NewWorkflowContext()
	parent.Vars["source"] = "parent"
	manager := NewContextManager(parent)

	parent.Vars["source"] = "mutated-parent"
	left := manager.NewBranchContext("left", BranchRuntime{})
	right := manager.NewBranchContext("right", BranchRuntime{})
	left.Workflow.Vars["source"] = "mutated-left"

	if left.BranchID != "left" || right.BranchID != "right" {
		t.Fatalf("branch IDs = %q, %q", left.BranchID, right.BranchID)
	}
	if got := right.Workflow.Vars["source"]; got != "parent" {
		t.Errorf("right branch inherited %q, want frozen parent", got)
	}
	if got := parent.Vars["source"]; got != "mutated-parent" {
		t.Errorf("parent was changed through branch context: %q", got)
	}
}

func TestForkCoordinatorPassesBranchRuntime(t *testing.T) {
	runtime := BranchRuntime{SessionID: "session-1", Role: "reviewer", AccountID: "account-1"}
	manager := NewContextManager(types.NewWorkflowContext())
	coordinator := ForkCoordinator{}
	results, err := coordinator.RunWithContextManager(context.Background(), manager, []Spec{{
		ID: "review", NodeID: "review", Runtime: runtime,
		Execute: func(_ context.Context, branch *BranchContext) (string, error) {
			if branch.BranchID != "review" {
				t.Fatalf("branch ID = %q", branch.BranchID)
			}
			if branch.Runtime.Role != runtime.Role || branch.Runtime.AccountID != runtime.AccountID {
				t.Fatalf("runtime = %#v, want injected role/account", branch.Runtime)
			}
			return branch.Runtime.Role + ":" + branch.Runtime.AccountID, nil
		},
	}})
	if err != nil {
		t.Fatalf("RunWithContextManager() error = %v", err)
	}
	if len(results) != 1 || results[0].Output == "" {
		t.Fatalf("results = %#v", results)
	}
}
