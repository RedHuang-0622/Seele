package gateway

import (
	"context"
	"strings"
	"testing"

	roottools "github.com/RedHuang-0622/Seele/tools"
	holder "github.com/RedHuang-0622/Seele/tools/holder"
	"github.com/RedHuang-0622/Seele/tools/permission"
)

// TestGatewayApprovalRequestFields：网关的审批请求与中间件同源（permission.NewApprovalRequest），
// 同样透出 ctx + ID/SessionID/Timeout/Risk/Preview/Options。
func TestGatewayApprovalRequestFields(t *testing.T) {
	cfg := permission.PermissionConfig{
		Mode: permission.ModeManual,
		Groups: []permission.PermissionGroup{
			{Name: "ops", Match: []string{"delete_*"}, Mode: permission.BitExecute, Default: permission.ActionAsk},
		},
		Subjects: map[permission.Subject]permission.SubjectGrant{
			"editor": {Bits: map[string]permission.GrantBit{"ops": {Bits: permission.BitExecute}}},
		},
	}

	h := holder.New()
	h.Register(&mockProvider{name: "prov", tools: []roottools.ToolEntry{
		makeMetaEntry("delete_file", &roottools.ToolMeta{
			Kind: roottools.ToolKindAdmin, Groups: []string{"ops"}, Bits: permission.BitExecute,
		}),
	}})
	g := NewDefaultGateway(h)
	g.SetSubjectResolver(subjectResolver)

	var reqs []permission.ApprovalRequest
	var ctxs []context.Context
	g.SetPermissionConfig(cfg, func(actx *permission.ApprovalContext) (*permission.ApprovalResponse, error) {
		reqs = append(reqs, actx.Request)
		ctxs = append(ctxs, actx.Context)
		return &permission.ApprovalResponse{Choice: "allow"}, nil
	})

	ctx := permission.WithSessionID(ctxWithSubject(context.Background(), "editor"), "sess-7")
	if _, err := g.Dispatch(ctx, "delete_file", `{}`); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(reqs) != 1 || len(ctxs) != 1 {
		t.Fatalf("审批次数 = %d / %d, want 1", len(reqs), len(ctxs))
	}

	req := reqs[0]
	if req.ID == "" {
		t.Fatal("ID 为空")
	}
	if req.SessionID != "sess-7" {
		t.Fatalf("SessionID = %q, want sess-7", req.SessionID)
	}
	if req.Timeout != permission.DefaultApprovalTimeout {
		t.Fatalf("Timeout = %v, want %v", req.Timeout, permission.DefaultApprovalTimeout)
	}
	if req.Risk != "high" {
		t.Fatalf("Risk = %q, want high（ToolMeta.Kind=admin）", req.Risk)
	}
	if !strings.HasPrefix(req.Preview, "delete_file(") || len(req.Options) == 0 {
		t.Fatalf("Preview/Options = %q / %d, want 预览与可选项", req.Preview, len(req.Options))
	}
	if got := permission.SessionIDFromContext(ctxs[0]); got != "sess-7" {
		t.Fatalf("approval ctx session = %q, want sess-7", got)
	}
}

// probeEnforcer 记录 Enforce 收到的 ctx 与主体，用于断言网关把 ctx 透传给位挂载点。
type probeEnforcer struct {
	action permission.Action
	ok     bool

	subject permission.Subject
	engine  permission.Engine
}

func (p *probeEnforcer) Enforce(ctx context.Context, subject permission.Subject, _ roottools.ToolMeta, _, _ string) (permission.Action, bool) {
	p.subject, p.engine = subject, permission.EngineFromContext(ctx)
	return p.action, p.ok
}

func TestGatewayBitEnforcerReceivesContextAndSubject(t *testing.T) {
	// guest 不持有 rw 位：判定器会判 write_doc 不可见，但 enforcer 先放行。
	g := newPermGateway(makeMetaEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}))
	probe := &probeEnforcer{action: permission.ActionAllow, ok: true}
	g.SetBitEnforcer(probe)

	ctx := permission.WithEngine(ctxWithSubject(context.Background(), "guest"), "engine-3")
	if _, err := g.Dispatch(ctx, "write_doc", `{}`); err != nil {
		t.Fatalf("dispatch = %v; want 放行（enforcer allow 优先于位检查）", err)
	}
	if probe.subject != "guest" {
		t.Fatalf("enforcer subject = %q, want guest", probe.subject)
	}
	if probe.engine != "engine-3" {
		t.Fatalf("enforcer engine = %q, want engine-3（ctx 透传）", probe.engine)
	}

	// ok=false ⇒ 框架不干预，回退到位检查（guest 断位 ⇒ 不可见）。
	probe.ok = false
	if _, err := g.Dispatch(ctx, "write_doc", `{}`); err == nil {
		t.Fatal("want 断位拒绝（enforcer 不干预时回退位判定）")
	}
}
