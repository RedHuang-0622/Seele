package agent

import "testing"

func TestEventLocatorLocatesAgentRuntime(t *testing.T) {
	location := (EventLocator{
		AgentID: "agent-1", SessionID: "session-1", AccountID: "account-1", Model: "model-1",
	}).Locate()
	if location.Kind != "agent.runtime" {
		t.Fatalf("kind = %q", location.Kind)
	}
	for key, want := range map[string]string{
		"agent_id": "agent-1", "session_id": "session-1", "account_id": "account-1", "model": "model-1",
	} {
		if got := location.IDs[key]; got != want {
			t.Errorf("IDs[%q] = %q, want %q", key, got, want)
		}
	}
}
