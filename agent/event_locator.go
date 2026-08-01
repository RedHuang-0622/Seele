package agent

import frameworkevent "github.com/RedHuang-0622/Seele/event"

// EventLocator is Agent's typed contribution to a generic event location.
// Agent identity and account ownership remain caller-provided product data.
type EventLocator struct {
	AgentID   string
	SessionID string
	AccountID string
	Model     string
}

// Locate implements event.Locator.
func (l EventLocator) Locate() frameworkevent.Location {
	ids := make(map[string]string, 4)
	if l.AgentID != "" {
		ids["agent_id"] = l.AgentID
	}
	if l.SessionID != "" {
		ids["session_id"] = l.SessionID
	}
	if l.AccountID != "" {
		ids["account_id"] = l.AccountID
	}
	if l.Model != "" {
		ids["model"] = l.Model
	}
	return frameworkevent.Location{Kind: "agent.runtime", IDs: ids}
}
