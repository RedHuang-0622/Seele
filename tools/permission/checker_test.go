package permission

import "testing"

func TestPermissionCheckerRules(t *testing.T) {
	checker := NewPermissionChecker(PermissionConfig{Mode: ModeManual, Rules: []PermissionRule{
		{ToolName: "read", Action: ActionAllow},
		{ToolName: "delete", Action: ActionDeny},
	}})
	if got := checker.Check("read", `{}`); got != ResultAllow {
		t.Fatalf("read result = %v", got)
	}
	if got := checker.Check("delete", `{}`); got != ResultDeny {
		t.Fatalf("delete result = %v", got)
	}
	if got := checker.Check("unknown", `{}`); got != ResultAsk {
		t.Fatalf("unknown result = %v", got)
	}
}

func TestFullAccessDefault(t *testing.T) {
	checker := NewPermissionChecker(PermissionConfig{})
	if got := checker.Check("anything", `{}`); got != ResultAllow {
		t.Fatalf("default result = %v", got)
	}
}
