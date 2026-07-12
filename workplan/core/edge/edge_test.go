package edge

import (
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

func trueCond(_ *types.WorkflowContext) bool  { return true }
func falseCond(_ *types.WorkflowContext) bool { return false }

// --- Resolve ---

func TestResolve_Unconditional(t *testing.T) {
	wc := types.NewWorkflowContext()
	edges := []Edge{
		{From: "a", To: "b", Condition: nil, Priority: 0, Label: "unconditional b"},
		{From: "a", To: "c", Condition: trueCond, Priority: 10, Label: "conditional c"},
		{From: "b", To: "c", Condition: nil, Priority: 0, Label: "unconditional c"},
	}

	// Unconditional edge from "a" to "b" should win.
	got := Resolve(edges, "a", wc)
	if got != "b" {
		t.Errorf("Resolve(edges, 'a', wc) = %q, want %q", got, "b")
	}

	// Unconditional edge from "b" to "c".
	got2 := Resolve(edges, "b", wc)
	if got2 != "c" {
		t.Errorf("Resolve(edges, 'b', wc) = %q, want %q", got2, "c")
	}
}

func TestResolve_Conditional(t *testing.T) {
	wc := types.NewWorkflowContext()

	t.Run("first matching condition wins", func(t *testing.T) {
		edges := []Edge{
			{From: "a", To: "false", Condition: falseCond, Priority: 0},
			{From: "a", To: "true", Condition: trueCond, Priority: 10},
		}
		got := Resolve(edges, "a", wc)
		if got != "true" {
			t.Errorf("expected first matching condition 'true', got %q", got)
		}
	})

	t.Run("nil condition before conditional", func(t *testing.T) {
		// nil (unconditional) edges are checked before conditional edges.
		edges := []Edge{
			{From: "a", To: "cond", Condition: trueCond, Priority: 0},
			{From: "a", To: "uncond", Condition: nil, Priority: 0},
		}
		got := Resolve(edges, "a", wc)
		if got != "uncond" {
			t.Errorf("expected unconditional 'uncond', got %q", got)
		}
	})

	t.Run("condition that checks context vars", func(t *testing.T) {
		cond := func(wc2 *types.WorkflowContext) bool {
			return wc2.Vars["approved"] == "true"
		}
		edges := []Edge{
			{From: "a", To: "approved", Condition: cond, Priority: 0},
			{From: "a", To: "default", Condition: trueCond, Priority: 1},
		}

		wc2 := types.NewWorkflowContext()
		wc2.Vars["approved"] = "true"
		got := Resolve(edges, "a", wc2)
		if got != "approved" {
			t.Errorf("expected 'approved', got %q", got)
		}

		wc3 := types.NewWorkflowContext()
		wc3.Vars["approved"] = "false"
		got2 := Resolve(edges, "a", wc3)
		if got2 != "default" {
			t.Errorf("expected fallback 'default', got %q", got2)
		}
	})
}

func TestResolve_PriorityOrdering(t *testing.T) {
	wc := types.NewWorkflowContext()
	edges := []Edge{
		{From: "a", To: "low", Condition: trueCond, Priority: 100},
		{From: "a", To: "high", Condition: trueCond, Priority: 1},
		{From: "a", To: "mid", Condition: trueCond, Priority: 50},
	}

	// Lower number = higher priority.
	got := Resolve(edges, "a", wc)
	if got != "high" {
		t.Errorf("expected highest-priority 'high' (pri=1), got %q", got)
	}

	t.Run("stable within same priority", func(t *testing.T) {
		// When multiple have the same priority, sort.Slice is not stable,
		// but the first in the sorted order wins. Since priorities differ,
		// the sorted order is deterministic.
		edges2 := []Edge{
			{From: "a", To: "first", Condition: trueCond, Priority: 0},
			{From: "a", To: "second", Condition: trueCond, Priority: 0},
		}
		got2 := Resolve(edges2, "a", wc)
		// Both have the same priority and are sorted arbitrarily.
		// But because sort.Slice is used, one will be first; we just
		// verify it's non-empty and matches one of the two.
		if got2 != "first" && got2 != "second" {
			t.Errorf("expected 'first' or 'second', got %q", got2)
		}
	})
}

func TestResolve_NoMatch(t *testing.T) {
	wc := types.NewWorkflowContext()

	t.Run("all conditions false", func(t *testing.T) {
		edges := []Edge{
			{From: "a", To: "b", Condition: falseCond, Priority: 0},
			{From: "a", To: "c", Condition: falseCond, Priority: 10},
		}
		got := Resolve(edges, "a", wc)
		if got != "" {
			t.Errorf("expected empty string, got %q", got)
		}
	})

	t.Run("nil edges slice", func(t *testing.T) {
		got := Resolve(nil, "a", wc)
		if got != "" {
			t.Errorf("expected empty string for nil edges, got %q", got)
		}
	})

	t.Run("empty edges slice", func(t *testing.T) {
		got := Resolve([]Edge{}, "a", wc)
		if got != "" {
			t.Errorf("expected empty string for empty edges, got %q", got)
		}
	})

	t.Run("from not in edges", func(t *testing.T) {
		edges := []Edge{
			{From: "x", To: "y", Condition: nil},
		}
		got := Resolve(edges, "a", wc)
		if got != "" {
			t.Errorf("expected empty string for non-matching from, got %q", got)
		}
	})
}

// --- HasEdgeFrom/HasEdgeTo ---

func TestHasEdgeFrom(t *testing.T) {
	edges := []Edge{
		{From: "a", To: "b", Label: "a->b"},
		{From: "b", To: "c", Label: "b->c"},
		{From: "b", To: "d", Label: "b->d"},
	}

	if !HasEdgeFrom(edges, "a") {
		t.Error("HasEdgeFrom(edges, 'a') = false, want true")
	}
	if !HasEdgeFrom(edges, "b") {
		t.Error("HasEdgeFrom(edges, 'b') = false, want true")
	}
	if HasEdgeFrom(edges, "c") {
		t.Error("HasEdgeFrom(edges, 'c') = true, want false")
	}
	if HasEdgeFrom(edges, "z") {
		t.Error("HasEdgeFrom(edges, 'z') = true, want false")
	}

	t.Run("nil edges", func(t *testing.T) {
		if HasEdgeFrom(nil, "a") {
			t.Error("HasEdgeFrom(nil, 'a') = true, want false")
		}
	})

	t.Run("empty edges", func(t *testing.T) {
		if HasEdgeFrom([]Edge{}, "a") {
			t.Error("HasEdgeFrom([]Edge{}, 'a') = true, want false")
		}
	})
}

func TestHasEdgeTo(t *testing.T) {
	edges := []Edge{
		{From: "a", To: "b", Label: "a->b"},
		{From: "b", To: "c", Label: "b->c"},
		{From: "c", To: "b", Label: "c->b"},
	}

	if !HasEdgeTo(edges, "b") {
		t.Error("HasEdgeTo(edges, 'b') = false, want true")
	}
	if !HasEdgeTo(edges, "c") {
		t.Error("HasEdgeTo(edges, 'c') = false, want true")
	}
	if HasEdgeTo(edges, "a") {
		t.Error("HasEdgeTo(edges, 'a') = true, want false")
	}
	if HasEdgeTo(edges, "z") {
		t.Error("HasEdgeTo(edges, 'z') = true, want false")
	}

	t.Run("nil edges", func(t *testing.T) {
		if HasEdgeTo(nil, "b") {
			t.Error("HasEdgeTo(nil, 'b') = true, want false")
		}
	})
}

// --- Edge struct ---

func TestEdgeStruct(t *testing.T) {
	e := Edge{
		From:      "node_a",
		To:        "node_b",
		Condition: trueCond,
		Priority:  5,
		Label:     "always transition",
	}

	if e.From != "node_a" {
		t.Errorf("From = %q, want %q", e.From, "node_a")
	}
	if e.To != "node_b" {
		t.Errorf("To = %q, want %q", e.To, "node_b")
	}
	if e.Priority != 5 {
		t.Errorf("Priority = %d, want 5", e.Priority)
	}
	if e.Label != "always transition" {
		t.Errorf("Label = %q, want %q", e.Label, "always transition")
	}
	if e.Condition == nil {
		t.Error("Condition should not be nil")
	}
	if !e.Condition(nil) {
		t.Error("Condition should return true")
	}
}
