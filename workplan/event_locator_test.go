package workplan

import "testing"

func TestEventLocatorLocatesWorkPlanNode(t *testing.T) {
	location := (EventLocator{
		PlanID: "plan-1", RunID: "run-1", NodeID: "node-1", BranchID: "branch-1",
	}).Locate()
	if location.Kind != "workplan.node" {
		t.Fatalf("kind = %q", location.Kind)
	}
	for key, want := range map[string]string{
		"plan_id": "plan-1", "run_id": "run-1", "node_id": "node-1", "branch_id": "branch-1",
	} {
		if got := location.IDs[key]; got != want {
			t.Errorf("IDs[%q] = %q, want %q", key, got, want)
		}
	}
}
