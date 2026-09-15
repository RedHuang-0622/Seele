package gateway

import (
	"context"
	"errors"
	"testing"

	roottools "github.com/RedHuang-0622/Seele/tools"
	holder "github.com/RedHuang-0622/Seele/tools/holder"
	"github.com/RedHuang-0622/Seele/tools/permission"
	types "github.com/RedHuang-0622/Seele/types"
)

// ── 权限模型测试脚手架 ──────────────────────────────────────────────────

type subjectKey struct{}

func ctxWithSubject(ctx context.Context, subject permission.Subject) context.Context {
	return context.WithValue(ctx, subjectKey{}, subject)
}

func subjectResolver(ctx context.Context) permission.Subject {
	if subject, ok := ctx.Value(subjectKey{}).(permission.Subject); ok {
		return subject
	}
	return permission.SubjectAnonymous
}

func makeMetaEntry(name string, meta *roottools.ToolMeta) roottools.ToolEntry {
	entry := makeEntry(name, nil)
	entry.Meta = meta
	return entry
}

func permConfig() permission.PermissionConfig {
	return permission.PermissionConfig{
		Mode: permission.ModeManual,
		Groups: []permission.PermissionGroup{
			{Name: "ro", Match: []string{"read_*"}, Mode: permission.BitRead, Default: permission.ActionAllow},
			{Name: "rw", Match: []string{"write_*"}, Mode: permission.BitRead | permission.BitWrite, Default: permission.ActionAllow},
		},
		Subjects: map[permission.Subject]permission.SubjectGrant{
			"guest":  {Bits: map[string]permission.GrantBit{"ro": {Bits: permission.BitRead}}},
			"editor": {Bits: map[string]permission.GrantBit{"ro": {Bits: permission.BitRead}, "rw": {Bits: permission.BitRead | permission.BitWrite}}},
			"root":   {Bits: map[string]permission.GrantBit{"ro": {Bits: permission.BitRead}, "rw": {Bits: permission.BitRead | permission.BitWrite}}},
		},
		Rules: []permission.PermissionRule{{ToolName: "write_doc", Action: permission.ActionDeny}},
	}
}

func newPermGateway(entries ...roottools.ToolEntry) *DefaultGateway {
	h := holder.New()
	h.Register(&mockProvider{name: "prov", tools: entries})
	g := NewDefaultGateway(h)
	g.SetSubjectResolver(subjectResolver)
	g.SetPermissionConfig(permConfig(), nil)
	return g
}

// ── R7：两类“不可用”可 errors.Is 辨别 ───────────────────────────────────

func TestGatewayDistinguishesNotVisibleAndDenied(t *testing.T) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
		makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
	)

	// 断位（guest 无 write 位）⇒ ErrToolNotVisible。
	guest := ctxWithSubject(context.Background(), "guest")
	if _, err := g.Dispatch(guest, "write_doc", `{}`); !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("guest write_doc err = %v, want ErrToolNotVisible", err)
	} else if errors.Is(err, roottools.ErrPermissionDenied) {
		t.Fatalf("guest write_doc must not report ErrPermissionDenied")
	}
	if _, err := g.Dispatch(guest, "read_doc", `{}`); err != nil {
		t.Fatalf("guest read_doc err = %v, want nil", err)
	}

	// 位齐但规则拒绝 ⇒ ErrPermissionDenied。
	editor := ctxWithSubject(context.Background(), "editor")
	if _, err := g.Dispatch(editor, "write_doc", `{}`); !errors.Is(err, roottools.ErrPermissionDenied) {
		t.Fatalf("editor write_doc err = %v, want ErrPermissionDenied", err)
	} else if errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("editor write_doc must not report ErrToolNotVisible")
	}
}

// ── R7：VisibleTools 不再返回断位工具 ────────────────────────────────────

func TestGatewayVisibleToolsExcludesMissingBitsAndControl(t *testing.T) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
		makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
		makeMetaEntry("ctl_stop", &roottools.ToolMeta{Kind: roottools.ToolKindControl, Signal: roottools.SignalTerm}),
	)

	guest := ctxWithSubject(context.Background(), "guest")
	names := visibleNames(g.VisibleTools(guest))
	if names["write_doc"] {
		t.Errorf("断位工具 write_doc 不应出现在 guest 可见列表: %v", names)
	}
	if names["ctl_stop"] {
		t.Errorf("控制类工具 ctl_stop 对非 root 不可见: %v", names)
	}
	if !names["read_doc"] {
		t.Errorf("read_doc 应对 guest 可见: %v", names)
	}

	root := ctxWithSubject(context.Background(), permission.SubjectRoot)
	rootNames := visibleNames(g.VisibleTools(root))
	if !rootNames["read_doc"] || !rootNames["write_doc"] || !rootNames["ctl_stop"] {
		t.Errorf("root 应可见全部工具: %v", rootNames)
	}
}

func visibleNames(tools []types.Tool) map[string]bool {
	result := make(map[string]bool, len(tools))
	for _, tool := range tools {
		result[tool.Function.Name] = true
	}
	return result
}

// ── R6：控制类工具仅 root 可路由，Signal 可被读取 ───────────────────────

func TestGatewayControlToolRootOnlyAndSignal(t *testing.T) {
	g := newPermGateway(
		makeMetaEntry("ctl_stop", &roottools.ToolMeta{Kind: roottools.ToolKindControl, Signal: roottools.SignalTerm}),
	)

	meta, ok := g.ToolMeta("ctl_stop")
	if !ok || meta.Kind != roottools.ToolKindControl || meta.Signal != roottools.SignalTerm {
		t.Fatalf("ToolMeta = %+v, ok=%v", meta, ok)
	}

	guest := ctxWithSubject(context.Background(), "guest")
	if _, err := g.Dispatch(guest, "ctl_stop", `{}`); !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("guest ctl_stop err = %v, want ErrToolNotVisible", err)
	}

	root := ctxWithSubject(context.Background(), permission.SubjectRoot)
	result, err := g.Dispatch(root, "ctl_stop", `{}`)
	if err != nil || result != "result:ctl_stop" {
		t.Fatalf("root ctl_stop = %q, %v", result, err)
	}
}

// ── R8：BitEnforcer 挂载点，装 / 不装行为可辨 ────────────────────────────

type fixedEnforcer struct {
	action  permission.Action
	onlyFor string
	ok      bool
}

func (e fixedEnforcer) Enforce(_ context.Context, _ permission.Subject, _ roottools.ToolMeta, toolName, _ string) (permission.Action, bool) {
	if !e.ok {
		return "", false
	}
	if e.onlyFor != "" && e.onlyFor != toolName {
		return "", false
	}
	return e.action, true
}

func TestGatewayBitEnforcer(t *testing.T) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
		makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
	)
	guest := ctxWithSubject(context.Background(), "guest")

	// 不装：正常路径 → 断位不可见。
	if _, err := g.Dispatch(guest, "write_doc", `{}`); !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("without enforcer err = %v, want ErrToolNotVisible", err)
	}

	// 装 deny：位齐或断位都被强制拒绝 → ErrPermissionDenied。
	g.SetBitEnforcer(fixedEnforcer{action: permission.ActionDeny, ok: true})
	if _, err := g.Dispatch(guest, "read_doc", `{}`); !errors.Is(err, roottools.ErrPermissionDenied) {
		t.Fatalf("deny enforcer err = %v, want ErrPermissionDenied", err)
	}

	// 装 allow：放行断位工具（框架不干预路径，只信 hook 动作）。
	g.SetBitEnforcer(fixedEnforcer{action: permission.ActionAllow, onlyFor: "write_doc", ok: true})
	if _, err := g.Dispatch(guest, "write_doc", `{}`); err != nil {
		t.Fatalf("allow enforcer err = %v, want nil", err)
	}

	// 装但 ok=false：不干预 → 回到正常路径。
	g.SetBitEnforcer(fixedEnforcer{ok: false})
	if _, err := g.Dispatch(guest, "write_doc", `{}`); !errors.Is(err, roottools.ErrToolNotVisible) {
		t.Fatalf("inactive enforcer err = %v, want ErrToolNotVisible", err)
	}
}

// ── 顺带清理：assessRisk 优先读 ToolMeta.Kind ────────────────────────────

func TestGatewayRiskFromMeta(t *testing.T) {
	g := newPermGateway(
		makeMetaEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
		makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
		makeMetaEntry("ctl_stop", &roottools.ToolMeta{Kind: roottools.ToolKindControl}),
		makeEntry("bash", nil), // 无 Meta → 回退旧 switch
	)
	cases := map[string]string{
		"read_doc":  "low",
		"write_doc": "medium",
		"ctl_stop":  "high",
		"bash":      "high",
		"unknown":   "low",
	}
	for name, want := range cases {
		if got := g.riskOf(name); got != want {
			t.Errorf("riskOf(%q) = %q, want %q", name, got, want)
		}
	}
}
