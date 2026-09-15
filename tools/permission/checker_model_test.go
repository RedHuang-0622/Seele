package permission

import "testing"

// matrixGroups/matrixSubjects 描述完成判据要求的 subject × group 判定矩阵。
func matrixConfig() PermissionConfig {
	return PermissionConfig{
		Mode: ModeManual,
		Groups: []PermissionGroup{
			{Name: "ro", Match: []string{"read_*", "list_*"}, Mode: BitRead, Default: ActionAllow},
			{Name: "rw", Match: []string{"write_*", "edit_*"}, Mode: BitRead | BitWrite, Default: ActionAllow},
			{Name: "ctl", Match: []string{"ctl_*"}, Mode: BitExecute, Default: ActionAsk},
			{Name: "adm", Match: []string{"admin_*"}, Mode: BitRead | BitWrite | BitExecute, Default: ActionDeny},
		},
		Subjects: map[Subject]SubjectGrant{
			"main": {Bits: map[string]GrantBit{
				"ro":  {Bits: BitRead},
				"rw":  {Bits: BitRead | BitWrite},
				"ctl": {Bits: BitExecute},
				"adm": {Bits: BitRead | BitWrite | BitExecute},
			}},
			"sub": {Bits: map[string]GrantBit{
				"ro":  {Bits: BitRead},
				"rw":  {Bits: BitRead}, // 缺 write
				"ctl": {Bits: 0},       // 缺 execute
				"adm": {Bits: BitRead}, // 缺 write/execute
			}},
		},
	}
}

// TestCheckForSubjectGroupMatrix 覆盖 {main, sub} × {ro, rw, ctl, adm}。
func TestCheckForSubjectGroupMatrix(t *testing.T) {
	checker := NewPermissionChecker(matrixConfig())
	cases := []struct {
		subject Subject
		tool    string
		want    CheckResult
	}{
		{"main", "read_doc", ResultAllow},   // ro：位齐 → 组默认 allow
		{"main", "write_doc", ResultAllow},  // rw：位齐 → allow
		{"main", "ctl_stop", ResultAsk},     // ctl：位齐 → ask
		{"main", "admin_reset", ResultDeny}, // adm：位齐 → deny
		{"sub", "list_files", ResultAllow},  // ro：位齐 → allow
		{"sub", "write_doc", ResultAsk},     // rw：断位 → MissingBit(ask)
		{"sub", "ctl_stop", ResultAsk},      // ctl：断位 → ask
		{"sub", "admin_reset", ResultAsk},   // adm：断位 → ask（即便组默认 deny）
	}
	for _, tc := range cases {
		if got := checker.CheckFor(tc.subject, tc.tool, `{}`); got != tc.want {
			t.Errorf("CheckFor(%q, %q) = %v, want %v", tc.subject, tc.tool, got, tc.want)
		}
	}
}

// TestVisibleForMatchesBits 断位工具不在“PATH”上。
func TestVisibleForMatchesBits(t *testing.T) {
	checker := NewPermissionChecker(matrixConfig())
	cases := []struct {
		subject Subject
		tool    string
		want    bool
	}{
		{"main", "write_doc", true},
		{"sub", "read_doc", true},
		{"sub", "write_doc", false},
		{"sub", "ctl_stop", false},
		{"sub", "unknown_tool", true}, // 未路由到组 → 无位约束
	}
	for _, tc := range cases {
		if got := checker.VisibleFor(tc.subject, tc.tool); got != tc.want {
			t.Errorf("VisibleFor(%q, %q) = %v, want %v", tc.subject, tc.tool, got, tc.want)
		}
	}
}

// TestGroupsOnlyExpressDefaultAction 只写 Groups、不写 Rules 即可表达分组默认动作。
func TestGroupsOnlyExpressDefaultAction(t *testing.T) {
	checker := NewPermissionChecker(PermissionConfig{
		Mode: ModeManual,
		Groups: []PermissionGroup{
			{Name: "ro", Match: []string{"read_*"}, Default: ActionAllow},
			{Name: "danger", Match: []string{"rm_*"}, Default: ActionDeny},
		},
	})
	cases := map[string]CheckResult{
		"read_file":  ResultAllow,
		"rm_all":     ResultDeny,
		"unrouted_x": ResultAsk, // 无组命中 → 保持 ask
	}
	for tool, want := range cases {
		if got := checker.Check(tool, `{}`); got != want {
			t.Errorf("Check(%q) = %v, want %v", tool, got, want)
		}
	}
}

// TestMissingBitZeroValueIsAsk 零值 MissingBit 等价于 ask；配置后按配置生效。
func TestMissingBitZeroValueIsAsk(t *testing.T) {
	base := PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "ro", Match: []string{"read_*"}, Mode: BitRead, Default: ActionAllow}},
		Subjects: map[Subject]SubjectGrant{
			"u": {Bits: map[string]GrantBit{"ro": {Bits: 0}}},
		},
	}
	if got := NewPermissionChecker(base).CheckFor("u", "read_doc", `{}`); got != ResultAsk {
		t.Fatalf("zero MissingBit = %v, want ResultAsk", got)
	}
	base.MissingBit = ActionDeny
	if got := NewPermissionChecker(base).CheckFor("u", "read_doc", `{}`); got != ResultDeny {
		t.Fatalf("configured MissingBit = %v, want ResultDeny", got)
	}
	base.MissingBit = ActionAllow
	if got := NewPermissionChecker(base).CheckFor("u", "read_doc", `{}`); got != ResultAllow {
		t.Fatalf("configured MissingBit = %v, want ResultAllow", got)
	}
}

// TestRulesOverrideGroupDefault Rules 永远最后、最细，覆盖组默认。
func TestRulesOverrideGroupDefault(t *testing.T) {
	var exec = BitExecute
	checker := NewPermissionChecker(PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "ctl", Match: []string{"ctl_*"}, Mode: 0, Default: ActionAllow}},
		Rules: []PermissionRule{
			{ToolName: "ctl_*", Action: ActionDeny},
			{ToolName: "ctl_freeze", Action: ActionAsk, Bits: &exec},
		},
	})
	if got := checker.Check("ctl_stop", `{}`); got != ResultDeny {
		t.Errorf("ctl_stop = %v, want ResultDeny (rule overrides group default)", got)
	}
}

// TestRuleBitsGateRules 带 Bits 的规则只对持有该位的主体生效。
func TestRuleBitsGateRules(t *testing.T) {
	var write = BitWrite
	checker := NewPermissionChecker(PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "rw", Match: []string{"write_*"}, Mode: 0, Default: ActionAsk}},
		Rules:  []PermissionRule{{ToolName: "write_*", Action: ActionAllow, Bits: &write}},
		Subjects: map[Subject]SubjectGrant{
			"ro_user": {Bits: map[string]GrantBit{"rw": {Bits: BitRead}}},
			"rw_user": {Bits: map[string]GrantBit{"rw": {Bits: BitRead | BitWrite}}},
		},
	})
	if got := checker.CheckFor("ro_user", "write_doc", `{}`); got != ResultAsk {
		t.Errorf("ro_user = %v, want ResultAsk (Bits rule skipped)", got)
	}
	if got := checker.CheckFor("rw_user", "write_doc", `{}`); got != ResultAllow {
		t.Errorf("rw_user = %v, want ResultAllow", got)
	}
}

// TestElevationScopeOnceVsSession 提权范围行为差异 + 审计回调。
func TestElevationScopeOnceVsSession(t *testing.T) {
	var events []ElevationEvent
	checker := NewPermissionChecker(matrixConfig())
	checker.SetElevationAuditor(func(event ElevationEvent) { events = append(events, event) })

	// once：不放宽后续调用。
	checker.GrantElevation("sub", ScopeOnce, "write_doc", `{}`)
	if got := checker.CheckFor("sub", "write_doc", `{}`); got != ResultAsk {
		t.Errorf("once must not relax subsequent calls, got %v", got)
	}
	if got := checker.CheckFor("sub", "edit_doc", `{}`); got != ResultAsk {
		t.Errorf("once must not relax other tools, got %v", got)
	}

	// session：同一 subject + group 内复用。
	checker.GrantElevation("sub", ScopeSession, "write_doc", `{}`)
	if got := checker.CheckFor("sub", "edit_doc", `{}`); got != ResultAllow {
		t.Errorf("session elevation should allow same-group tool, got %v", got)
	}
	if got := checker.CheckFor("sub", "ctl_stop", `{}`); got != ResultAsk {
		t.Errorf("session elevation must not leak to other groups, got %v", got)
	}
	if got := checker.CheckFor("other", "write_doc", `{}`); got != ResultAsk {
		t.Errorf("session elevation must not leak to other subjects, got %v", got)
	}

	// 每次提权必发审计。
	if len(events) != 2 {
		t.Fatalf("expected 2 elevation audits, got %d", len(events))
	}
	for _, event := range events {
		if event.Subject != "sub" || !event.Granted || event.At.IsZero() {
			t.Errorf("bad audit event: %+v", event)
		}
	}
	if events[0].Scope != ScopeOnce || events[1].Scope != ScopeSession {
		t.Errorf("audit scopes = %q/%q", events[0].Scope, events[1].Scope)
	}
}

// TestGrantElevationEmptyScopeIsOnce 空 Scope 等价 once。
func TestGrantElevationEmptyScopeIsOnce(t *testing.T) {
	checker := NewPermissionChecker(matrixConfig())
	checker.GrantElevation("sub", "", "write_doc", `{}`)
	if got := checker.CheckFor("sub", "write_doc", `{}`); got != ResultAsk {
		t.Fatalf("empty scope must behave as once, got %v", got)
	}
}

// TestCheckEqualsCheckForAnonymous 兼容性：Check 等价 CheckFor(匿名)。
func TestCheckEqualsCheckForAnonymous(t *testing.T) {
	legacy := NewPermissionChecker(PermissionConfig{Mode: ModeManual, Rules: []PermissionRule{
		{ToolName: "read", Action: ActionAllow},
		{ToolName: "delete", Action: ActionDeny},
	}})
	for _, tool := range []string{"read", "delete", "unknown"} {
		if legacy.Check(tool, `{}`) != legacy.CheckFor(SubjectAnonymous, tool, `{}`) {
			t.Fatalf("Check(%q) != CheckFor(\"\", %q)", tool, tool)
		}
	}
}
