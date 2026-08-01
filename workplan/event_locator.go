package workplan

import frameworkevent "github.com/RedHuang-0622/Seele/event"

// EventLocator is WorkPlan's typed contribution to a generic event location.
// It carries no Task or UI semantics; product code supplies these identifiers
// when assembling a plan execution.
type EventLocator struct {
	PlanID   string
	RunID    string
	NodeID   string
	BranchID string
}

// Locate implements event.Locator.
func (l EventLocator) Locate() frameworkevent.Location {
	ids := make(map[string]string, 4)
	if l.PlanID != "" {
		ids["plan_id"] = l.PlanID
	}
	if l.RunID != "" {
		ids["run_id"] = l.RunID
	}
	if l.NodeID != "" {
		ids["node_id"] = l.NodeID
	}
	if l.BranchID != "" {
		ids["branch_id"] = l.BranchID
	}
	kind := "workplan.run"
	if l.NodeID != "" || l.BranchID != "" {
		kind = "workplan.node"
	}
	return frameworkevent.Location{Kind: kind, IDs: ids}
}
