package permission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
	"github.com/RedHuang-0622/Seele/types"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func metaTool(name string, kind roottools.ToolKind, cluster string, bits uint8) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: types.Tool{Type: "function", Function: types.ToolFunction{
			Name: name, Description: name, Parameters: map[string]interface{}{"type": "object"},
		}},
		Handler: roottools.HandlerFunc(func(context.Context, string) (string, error) { return "ran:" + name, nil }),
		Meta:    &roottools.ToolMeta{Kind: kind, Groups: []string{cluster}, Bits: bits},
	}
}

// plainTool declares no 簇属 (nil Meta), so the gate must fall back to name routing.
func plainTool(name string) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: types.Tool{Type: "function", Function: types.ToolFunction{
			Name: name, Description: name, Parameters: map[string]interface{}{"type": "object"},
		}},
		Handler: roottools.HandlerFunc(func(context.Context, string) (string, error) { return "ran:" + name, nil }),
	}
}

func gateRegistry(t *testing.T, gate *Gate, entries ...roottools.ToolEntry) *roottools.Registry {
	t.Helper()
	provider, err := roottools.NewFunctionProvider("demo", entries...)
	if err != nil {
		t.Fatalf("NewFunctionProvider: %v", err)
	}
	registry := roottools.NewRegistry(roottools.WithMetaMiddleware(gate.Middleware()))
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return registry
}

func dispatch(registry *roottools.Registry, ctx context.Context, tool string) (string, error) {
	return registry.Dispatch(ctx, roottools.ToolCall{Name: tool, ArgumentsJSON: `{}`})
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestGateAllowsEngineWithClusterBits(t *testing.T) {
	gate := &Gate{Checker: NewPermissionChecker(PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "docs", Match: []string{"read_*", "write_*"}, Mode: BitRead, Default: ActionAllow}},
		Subjects: map[Subject]SubjectGrant{
			"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead | BitWrite}}},
		},
	})}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))

	out, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "write_doc")
	if err != nil || out != "ran:write_doc" {
		t.Fatalf("dispatch = %q, %v; want executed", out, err)
	}
}

func TestGateDeniesWithEnglishMessage(t *testing.T) {
	gate := &Gate{Checker: NewPermissionChecker(PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAllow}},
		Subjects: map[Subject]SubjectGrant{
			"engine-1": {Bits: map[string]GrantBit{"ops": {Bits: BitRead}}},
		},
	})}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))

	_, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "delete_file")
	if err == nil {
		t.Fatal("expected denial")
	}
	want := `permission denied: cannot complete the invocation of tool "delete_file": the tool is not in this engine's namespace`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("errors.Is(ErrToolNotVisible) = false for %v", err)
	}
}

func TestGateDenyRuleReturnsDenialError(t *testing.T) {
	gate := &Gate{Checker: NewPermissionChecker(PermissionConfig{
		Mode:     ModeManual,
		Groups:   []PermissionGroup{{Name: "docs", Match: []string{"write_*"}, Mode: BitWrite, Default: ActionAllow}},
		Rules:    []PermissionRule{{ToolName: "write_doc", Action: ActionDeny}},
		Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead | BitWrite}}}},
	})}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))

	_, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "write_doc")
	if err == nil {
		t.Fatal("expected denial")
	}
	want := `permission denied: cannot complete the invocation of tool "write_doc"`
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, roottools.ErrPermissionDenied) {
		t.Fatalf("errors.Is(ErrPermissionDenied) = false for %v", err)
	}
}

func TestGateControlToolRootOnly(t *testing.T) {
	gate := &Gate{Checker: NewPermissionChecker(PermissionConfig{
		Mode:   ModeManual,
		Groups: []PermissionGroup{{Name: "ops", Match: []string{"ctl_*"}, Mode: BitExecute, Default: ActionAllow}},
		Subjects: map[Subject]SubjectGrant{
			"root":     {Bits: map[string]GrantBit{"ops": {Bits: BitExecute}}},
			"engine-1": {Bits: map[string]GrantBit{"ops": {Bits: BitExecute}}},
		},
	})}
	registry := gateRegistry(t, gate, metaTool("ctl_stop", roottools.ToolKindControl, "ops", BitExecute))

	// Non-root engines cannot route a control tool even when they hold the bits.
	_, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "ctl_stop")
	if !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("engine-1 ctl_stop err = %v, want ErrToolNotVisible", err)
	}

	// root may route it.
	out, err := dispatch(registry, WithEngine(context.Background(), Engine(SubjectRoot)), "ctl_stop")
	if err != nil || out != "ran:ctl_stop" {
		t.Fatalf("root ctl_stop = %q, %v; want executed", out, err)
	}
}

func TestGateApprovalGrantsEngineElevation(t *testing.T) {
	var mu sync.Mutex
	approvals := 0
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "docs", Match: []string{"write_*"}, Mode: BitWrite, Default: ActionAsk}},
			Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead | BitWrite}}}},
		}),
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			mu.Lock()
			approvals++
			mu.Unlock()
			return &ApprovalResponse{Choice: "allow", Scope: ScopeSession}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))
	ctx := WithEngine(context.Background(), "engine-1")

	for i := 0; i < 2; i++ {
		out, err := dispatch(registry, ctx, "write_doc")
		if err != nil || out != "ran:write_doc" {
			t.Fatalf("dispatch #%d = %q, %v; want executed", i+1, out, err)
		}
	}
	mu.Lock()
	count := approvals
	mu.Unlock()
	if count != 1 {
		t.Fatalf("approvals = %d, want 1 (engine elevation not reused)", count)
	}
}

func TestGateMetaLessToolFallsBackToNameRouting(t *testing.T) {
	gate := &Gate{Checker: NewPermissionChecker(PermissionConfig{
		Mode:     ModeManual,
		Groups:   []PermissionGroup{{Name: "docs", Match: []string{"read_*", "legacy_*"}, Mode: BitRead, Default: ActionAllow}},
		Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead}}}},
	})}
	registry := gateRegistry(t, gate, plainTool("legacy_read"), plainTool("legacy_secret"))

	out, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "legacy_read")
	if err != nil || out != "ran:legacy_read" {
		t.Fatalf("legacy_read = %q, %v; want executed via name routing", out, err)
	}
	// A subject with no grant at all stays invisible.
	_, err = dispatch(registry, WithEngine(context.Background(), "engine-2"), "legacy_read")
	if !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("engine-2 legacy_read err = %v, want ErrToolNotVisible", err)
	}
}

func TestGateCustomEngineResolver(t *testing.T) {
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "docs", Match: []string{"read_*"}, Mode: BitRead, Default: ActionAllow}},
			Subjects: map[Subject]SubjectGrant{"resolved-engine": {Bits: map[string]GrantBit{"docs": {Bits: BitRead}}}},
		}),
		Resolve: func(context.Context) Engine { return "resolved-engine" },
	}
	registry := gateRegistry(t, gate, metaTool("read_doc", roottools.ToolKindRead, "docs", BitRead))

	// No WithEngine: the custom resolver supplies the engine.
	out, err := dispatch(registry, context.Background(), "read_doc")
	if err != nil || out != "ran:read_doc" {
		t.Fatalf("dispatch = %q, %v; want executed", out, err)
	}
}

func TestGateNoCheckerAllowsEverything(t *testing.T) {
	gate := &Gate{}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))
	out, err := dispatch(registry, context.Background(), "write_doc")
	if err != nil || !strings.HasPrefix(out, "ran:") {
		t.Fatalf("dispatch = %q, %v; want passthrough execution", out, err)
	}
}

// ── 拒绝 ⇒ 执行选择页面（单次操作的提权） ────────────────────────────────────

func TestGateDenialEscalatesToApprovalOnce(t *testing.T) {
	var mu sync.Mutex
	prompts := 0
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAllow}},
			Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead}}}},
		}),
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			mu.Lock()
			prompts++
			mu.Unlock()
			return &ApprovalResponse{Choice: "allow"}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))
	ctx := WithEngine(context.Background(), "engine-1")

	// 不可见被拒 ⇒ 先给出执行选择页面；通过后只放行本次调用。
	for i := 0; i < 2; i++ {
		out, err := dispatch(registry, ctx, "delete_file")
		if err != nil || out != "ran:delete_file" {
			t.Fatalf("dispatch #%d = %q, %v; want executed after approval", i+1, out, err)
		}
	}
	mu.Lock()
	count := prompts
	mu.Unlock()
	if count != 2 {
		t.Fatalf("prompts = %d, want 2 (single-operation elevation must not persist)", count)
	}
}

func TestGateEscalationHonoursDenyChoice(t *testing.T) {
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAllow}},
			Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead}}}},
		}),
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			return &ApprovalResponse{Choice: "deny"}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))

	_, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "delete_file")
	if err == nil {
		t.Fatal("expected denial after the choice page was declined")
	}
	if !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("errors.Is(ErrToolNotVisible) = false for %v", err)
	}
	if !strings.HasPrefix(err.Error(), `permission denied: cannot complete the invocation of tool "delete_file"`) {
		t.Fatalf("error = %q; want the English refusal", err.Error())
	}
}

// TestGateFullAccessStillPrompts: 即使判定为全权（full_access），控制类调用被拒时
// 仍然要给出执行选择页面，通过审批获得单次操作的提权。
func TestGateFullAccessStillPrompts(t *testing.T) {
	prompts := 0
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeFullAccess,
			Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"ops": {Bits: BitExecute}}}},
		}),
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			prompts++
			return &ApprovalResponse{Choice: "allow"}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("ctl_stop", roottools.ToolKindControl, "ops", BitExecute))

	out, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "ctl_stop")
	if err != nil || out != "ran:ctl_stop" {
		t.Fatalf("ctl_stop = %q, %v; want executed once after approval", out, err)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
}

func TestGateDenyWithoutPromptSkipsChoicePage(t *testing.T) {
	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAllow}},
			Subjects: map[Subject]SubjectGrant{"other-engine": {Bits: map[string]GrantBit{"ops": {Bits: BitExecute}}}},
		}),
		Approval:          func(*ApprovalContext) (*ApprovalResponse, error) { return &ApprovalResponse{Choice: "allow"}, nil },
		DenyWithoutPrompt: true,
	}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))

	_, err := dispatch(registry, WithEngine(context.Background(), "engine-1"), "delete_file")
	if !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("err = %v, want ErrToolNotVisible without a prompt", err)
	}
}

// harnessLedger 模拟 harness 层的提权台账：框架只通过 Elevator 接口读写它。
type harnessLedger struct {
	mu     sync.Mutex
	grants int
	scopes []string
}

func (l *harnessLedger) Elevate(_ Subject, scope, _, _ string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.grants++
	l.scopes = append(l.scopes, scope)
}

// Elevated 用「本 session 内该 subject 提过一次」的 harness 策略回答是否可复用。
func (l *harnessLedger) Elevated(_ Subject, _, _ string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.grants > 0
}

func TestGateHarnessOwnedElevation(t *testing.T) {
	checker := NewPermissionChecker(PermissionConfig{
		Mode:     ModeManual,
		Groups:   []PermissionGroup{{Name: "docs", Match: []string{"write_*"}, Mode: BitWrite, Default: ActionAsk}},
		Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead | BitWrite}}}},
	})
	ledger := &harnessLedger{}
	checker.SetElevationSource(ledger)

	prompts := 0
	gate := &Gate{
		Checker: checker,
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			prompts++
			return &ApprovalResponse{Choice: "allow", Scope: ScopeSession}, nil
		},
		Elevation: ledger, // harness 自己的台账
	}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))
	ctx := WithEngine(context.Background(), "engine-1")

	for i := 0; i < 2; i++ {
		if _, err := dispatch(registry, ctx, "write_doc"); err != nil {
			t.Fatalf("dispatch #%d: %v", i+1, err)
		}
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1 (harness ledger should cover the second call)", prompts)
	}
	ledger.mu.Lock()
	grants, scopes := ledger.grants, append([]string(nil), ledger.scopes...)
	ledger.mu.Unlock()
	if grants != 1 || len(scopes) != 1 || scopes[0] != ScopeSession {
		t.Fatalf("ledger = %d grants %v; want 1 grant with scope %q", grants, scopes, ScopeSession)
	}
}

// ── BitEnforcer 接进 Gate.evaluate ──────────────────────────────────────────

// enforcerFunc 让测试用函数直接实现 BitEnforcer。
type enforcerFunc func(ctx context.Context, subject Subject, meta roottools.ToolMeta, name, argsJSON string) (Action, bool)

func (f enforcerFunc) Enforce(ctx context.Context, subject Subject, meta roottools.ToolMeta, name, argsJSON string) (Action, bool) {
	return f(ctx, subject, meta, name, argsJSON)
}

func TestGateBitEnforcerPrecedesChecker(t *testing.T) {
	newGate := func(enforcer BitEnforcer) *Gate {
		return &Gate{
			Checker: NewPermissionChecker(PermissionConfig{
				Mode:   ModeManual,
				Groups: []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAllow}},
				// engine-1 只有 docs 位：判定器本应判 delete_file 不可见。
				Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"docs": {Bits: BitRead}}}},
			}),
			Enforcer:          enforcer,
			DenyWithoutPrompt: true, // 本组只看动作映射，不掺审批
		}
	}
	entry := metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute)
	ctx := WithEngine(context.Background(), "engine-1")

	t.Run("deny 直接拒绝", func(t *testing.T) {
		gate := newGate(enforcerFunc(func(context.Context, Subject, roottools.ToolMeta, string, string) (Action, bool) {
			return ActionDeny, true
		}))
		_, err := dispatch(gateRegistry(t, gate, entry), ctx, "delete_file")
		if !errors.Is(err, roottools.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
	})

	t.Run("allow 覆盖位检查", func(t *testing.T) {
		gate := newGate(enforcerFunc(func(context.Context, Subject, roottools.ToolMeta, string, string) (Action, bool) {
			return ActionAllow, true
		}))
		out, err := dispatch(gateRegistry(t, gate, entry), ctx, "delete_file")
		if err != nil || out != "ran:delete_file" {
			t.Fatalf("dispatch = %q, %v; want executed (enforcer allow 优先于位检查)", out, err)
		}
	})

	t.Run("ok=false 时退回判定器", func(t *testing.T) {
		gate := newGate(enforcerFunc(func(context.Context, Subject, roottools.ToolMeta, string, string) (Action, bool) {
			return ActionAllow, false
		}))
		_, err := dispatch(gateRegistry(t, gate, entry), ctx, "delete_file")
		if !errors.Is(err, roottools.ErrToolNotVisible) {
			t.Fatalf("err = %v, want ErrToolNotVisible (不干预应回退到位判定)", err)
		}
	})
}

func TestGateEnforcerReceivesContextAndSubject(t *testing.T) {
	var gotSubject Subject
	var gotEngine Engine
	var gotMeta roottools.ToolMeta

	gate := &Gate{
		Enforcer: enforcerFunc(func(ctx context.Context, subject Subject, meta roottools.ToolMeta, _, _ string) (Action, bool) {
			gotSubject, gotEngine, gotMeta = subject, EngineFromContext(ctx), meta
			return ActionAllow, true
		}),
	}
	registry := gateRegistry(t, gate, metaTool("write_doc", roottools.ToolKindWrite, "docs", BitWrite))

	out, err := dispatch(registry, WithEngine(context.Background(), "engine-9"), "write_doc")
	if err != nil || out != "ran:write_doc" {
		t.Fatalf("dispatch = %q, %v; want executed", out, err)
	}
	if gotSubject != "engine-9" || gotEngine != "engine-9" {
		t.Fatalf("enforcer saw subject=%q engine=%q; want engine-9", gotSubject, gotEngine)
	}
	if gotMeta.Kind != roottools.ToolKindWrite || len(gotMeta.Groups) != 1 || gotMeta.Bits != BitWrite {
		t.Fatalf("enforcer saw meta = %+v; want 工具簇属原样透传", gotMeta)
	}
}

// TestGateEnforcerDenyGivesChoicePage: enforcer 的 deny 同样走执行选择页面；
// 通过后只放行本次调用（单次提权）。
func TestGateEnforcerDenyGivesChoicePage(t *testing.T) {
	prompts := 0
	gate := &Gate{
		Enforcer: enforcerFunc(func(context.Context, Subject, roottools.ToolMeta, string, string) (Action, bool) {
			return ActionDeny, true
		}),
		Approval: func(*ApprovalContext) (*ApprovalResponse, error) {
			prompts++
			return &ApprovalResponse{Choice: "allow"}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))
	ctx := WithEngine(context.Background(), "engine-1")

	for i := 0; i < 2; i++ {
		if out, err := dispatch(registry, ctx, "delete_file"); err != nil || out != "ran:delete_file" {
			t.Fatalf("dispatch #%d = %q, %v; want executed after approval", i+1, out, err)
		}
	}
	if prompts != 2 {
		t.Fatalf("prompts = %d, want 2 (单次提权不持久)", prompts)
	}
}

// ── 审批请求透出 ctx + ID/SessionID/Timeout/Risk ────────────────────────────

func TestGateApprovalRequestCarriesContextAndFields(t *testing.T) {
	var mu sync.Mutex
	var reqs []ApprovalRequest
	var reqCtxs []context.Context

	gate := &Gate{
		Checker: NewPermissionChecker(PermissionConfig{
			Mode:     ModeManual,
			Groups:   []PermissionGroup{{Name: "ops", Match: []string{"delete_*"}, Mode: BitExecute, Default: ActionAsk}},
			Subjects: map[Subject]SubjectGrant{"engine-1": {Bits: map[string]GrantBit{"ops": {Bits: BitExecute}}}},
		}),
		Timeout: 90 * time.Second,
		Approval: func(actx *ApprovalContext) (*ApprovalResponse, error) {
			mu.Lock()
			reqs = append(reqs, actx.Request)
			reqCtxs = append(reqCtxs, actx.Context)
			mu.Unlock()
			return &ApprovalResponse{Choice: "allow"}, nil
		},
	}
	registry := gateRegistry(t, gate, metaTool("delete_file", roottools.ToolKindAdmin, "ops", BitExecute))
	ctx := WithSessionID(WithEngine(context.Background(), "engine-1"), "sess-42")

	for i := 0; i < 2; i++ {
		if _, err := dispatch(registry, ctx, "delete_file"); err != nil {
			t.Fatalf("dispatch #%d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 || len(reqCtxs) != 2 {
		t.Fatalf("审批次数 = %d / %d, want 2（allow 不带 Scope ⇒ 单次，第二次仍要询问）", len(reqs), len(reqCtxs))
	}
	first := reqs[0]
	if first.ID == "" || reqs[1].ID == "" || first.ID == reqs[1].ID {
		t.Fatalf("ID 必须非空且唯一：%q / %q", first.ID, reqs[1].ID)
	}
	if first.ToolName != "delete_file" || first.Arguments != `{}` {
		t.Fatalf("request = %+v; want tool/args 原样带出", first)
	}
	if first.SessionID != "sess-42" {
		t.Fatalf("SessionID = %q, want sess-42", first.SessionID)
	}
	if first.Timeout != 90*time.Second {
		t.Fatalf("Timeout = %v, want 90s（Gate.Timeout 透出）", first.Timeout)
	}
	if first.Risk != "high" {
		t.Fatalf("Risk = %q, want high（admin 簇属）", first.Risk)
	}
	if !strings.HasPrefix(first.Preview, "delete_file({})") {
		t.Fatalf("Preview = %q, want 调用预览", first.Preview)
	}
	if len(first.Options) == 0 {
		t.Fatal("Options 为空；执行选择页面需要可选项")
	}

	// ctx 透传：harness 侧能读到 session / engine。
	if got := EngineFromContext(reqCtxs[0]); got != "engine-1" {
		t.Fatalf("approval ctx engine = %q, want engine-1", got)
	}
	if got := SessionIDFromContext(reqCtxs[0]); got != "sess-42" {
		t.Fatalf("approval ctx session = %q, want sess-42", got)
	}
}

func TestNewApprovalRequestFieldsAndDefaults(t *testing.T) {
	// 无簇属：Risk 按名回退；Timeout<=0 取默认值。
	req := NewApprovalRequest(context.Background(), "write_file", roottools.ToolMeta{}, `{}`, 0)
	if req.Timeout != DefaultApprovalTimeout {
		t.Fatalf("Timeout = %v, want %v", req.Timeout, DefaultApprovalTimeout)
	}
	if req.Risk != "medium" {
		t.Fatalf("Risk = %q, want medium（按名回退）", req.Risk)
	}
	if req.ID == "" {
		t.Fatal("ID 为空")
	}

	// 簇属优先 + SessionID 从 ctx 读出 + 显式 Timeout 生效。
	req = NewApprovalRequest(
		WithSessionID(context.Background(), "s1"), "anything",
		roottools.ToolMeta{Kind: roottools.ToolKindControl}, `{}`, time.Minute,
	)
	if req.Risk != "high" || req.SessionID != "s1" || req.Timeout != time.Minute {
		t.Fatalf("request = %+v; want risk=high session=s1 timeout=1m", req)
	}

	// 预览截断：长参数不把请求撑爆。
	long := strings.Repeat("x", 500)
	if got := FormatPreview("t", long); len(got) != len("t(")+80+len("...)") {
		t.Fatalf("preview len = %d, want 截断到 80 字符 + 省略号", len(got))
	}

	// ID 单调唯一。
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewApprovalRequest(context.Background(), "t", roottools.ToolMeta{}, `{}`, 0).ID
		if seen[id] {
			t.Fatalf("重复 ID: %s", id)
		}
		seen[id] = true
	}
}

// ── channel 审批适配器：回执 / 超时 / 取消 / 投递失败 ───────────────────────

func TestChannelApprovalHandler(t *testing.T) {
	t.Run("回执：写入 Response 即放行", func(t *testing.T) {
		reqCh := make(chan ApprovalRequest, 1)
		handler := NewChannelApprovalHandler(reqCh)
		actx := &ApprovalContext{Context: context.Background(), Request: ApprovalRequest{ID: "a1", ToolName: "t", Timeout: time.Second}}

		var resp *ApprovalResponse
		var err error
		done := make(chan struct{})
		go func() { resp, err = handler(actx); close(done) }()

		req := <-reqCh // Response 已在此之前挂好
		if req.ID != "a1" {
			t.Fatalf("req.ID = %q, want a1", req.ID)
		}
		actx.Response <- &ApprovalResponse{RequestID: req.ID, Choice: "allow"}
		<-done

		if err != nil || resp == nil || resp.Choice != "allow" || resp.RequestID != "a1" {
			t.Fatalf("resp = %+v, err = %v; want allow 回执", resp, err)
		}
		if resp.Timestamp.IsZero() {
			t.Fatal("回执未打时间戳")
		}
	})

	t.Run("超时即拒绝", func(t *testing.T) {
		reqCh := make(chan ApprovalRequest, 1)
		handler := NewChannelApprovalHandler(reqCh)
		actx := &ApprovalContext{Context: context.Background(), Request: ApprovalRequest{Timeout: 30 * time.Millisecond}}

		_, err := handler(actx)
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Fatalf("err = %v, want timeout", err)
		}
	})

	t.Run("Timeout=0 取默认值而非立即取消", func(t *testing.T) {
		reqCh := make(chan ApprovalRequest, 1)
		handler := NewChannelApprovalHandler(reqCh)
		actx := &ApprovalContext{Context: context.Background()}

		done := make(chan struct{})
		var err error
		go func() { _, err = handler(actx); close(done) }()
		<-reqCh
		actx.Response <- &ApprovalResponse{Choice: "allow"}
		<-done
		if err != nil {
			t.Fatalf("err = %v, want nil（零值 Timeout 不应立即取消）", err)
		}
	})

	t.Run("ctx 取消终止等待", func(t *testing.T) {
		reqCh := make(chan ApprovalRequest, 1)
		handler := NewChannelApprovalHandler(reqCh)
		ctx, cancel := context.WithCancel(context.Background())
		actx := &ApprovalContext{Context: ctx, Request: ApprovalRequest{Timeout: time.Minute}}

		done := make(chan struct{})
		var err error
		go func() { _, err = handler(actx); close(done) }()
		<-reqCh
		cancel()
		<-done
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("err = %v, want cancelled", err)
		}
	})

	t.Run("请求通道满则投递失败", func(t *testing.T) {
		reqCh := make(chan ApprovalRequest) // 无缓冲且无人接收
		handler := NewChannelApprovalHandler(reqCh)
		actx := &ApprovalContext{Context: context.Background(), Request: ApprovalRequest{Timeout: time.Second}}

		_, err := handler(actx)
		if err == nil || !strings.Contains(err.Error(), "channel full") {
			t.Fatalf("err = %v, want channel full", err)
		}
	})
}
