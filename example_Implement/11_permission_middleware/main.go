// 11_permission_middleware/main.go
//
// 场景：装配自由 + 判定交给中间件 + 授权绑定到 session 下的 engine +
// 拒绝时给出「执行选择页面」，提权由 harness 层完成。
//
// 本示例完全离线（不需要 LLM / 凭据），直接驱动 tools.Registry 的装配与分发。
//
// 分层：
//
//	框架层（tools/permission）  判定 + 接口：Gate 中间件、ApprovalHandler、
//	                            Elevator、英文拒绝错误。不定义提权策略。
//	harness 层（宿主）          执行选择页面（UI）+ 提权台账（Elevator 实现）。
//
// 三个要点：
//
//  1. 工具自带簇属：ToolMeta{Kind, Groups, Bits} 声明「我属于哪个簇、需要哪些位」。
//  2. 判定在中间件：permission.Gate.Middleware() 包住每个工具；装配保持自由。
//  3. 拒绝 ⇒ 执行选择页面：通过审批只获得**单次操作的提权**（仅放行本次调用，
//     不写入任何可复用状态），因此即使是全权（full_access）也不会被永久绕过。
//
// 拒绝文案（英文）：
//
//	permission denied: cannot complete the invocation of tool "ctl_stop": the tool is not in this engine's namespace
//	permission denied: cannot complete the invocation of tool "write_doc"
//
// 接进真实会话时，只需把同一个 Gate 装进工具运行时：
//
//	registry := tool.NewRegistry(tool.WithMetaMiddleware(gate.Middleware()))
//	runtime, _ := bridge.NewRegistryRuntime(registry)          // tools.Registry -> agent.ToolRuntime
//	engine, _ := agent.NewWithComponents(agent.Components{Completer: llm, Tools: runtime})
//	sess, _ := session.NewSession(session.SessionComponents{Agent: engine})
//	// 每个回合把本会话的 engine id 注入 ctx，再交给 Session.Chat：
//	//   ctx = permission.WithEngine(ctx, "sess_42/engine_1")
//	//   sess.Chat(ctx, "...")
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	roottools "github.com/RedHuang-0622/Seele/tools"
	"github.com/RedHuang-0622/Seele/tools/permission"
	"github.com/RedHuang-0622/Seele/types"
)

// ── 1. 工具自带簇属 ─────────────────────────────────────────────────────────

func toolEntry(name string, kind roottools.ToolKind, cluster string, bits uint8) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: types.Tool{Type: "function", Function: types.ToolFunction{
			Name:        name,
			Description: "demo tool " + name,
			Parameters:  map[string]interface{}{"type": "object"},
		}},
		Handler: roottools.HandlerFunc(func(_ context.Context, _ string) (string, error) {
			return fmt.Sprintf(`{"ok":true,"tool":%q}`, name), nil
		}),
		// 簇属：Kind 分类、Groups 属于哪些簇、Bits 需要哪些位。
		Meta: &roottools.ToolMeta{Kind: kind, Groups: []string{cluster}, Bits: bits},
	}
}

// ── harness 层：执行选择页面 + 提权台账 ─────────────────────────────────────

// ledger 是 harness 自己的提权台账：实现框架定义的 permission.Elevator 接口。
// 提权是否持久、按什么范围复用，完全由 harness 决定。
type ledger struct {
	mu     sync.Mutex
	grants int
}

func (l *ledger) Elevate(_ permission.Subject, scope, tool, _ string) {
	l.mu.Lock()
	l.grants++
	l.mu.Unlock()
	fmt.Printf("    [harness] 记录提权：tool=%s scope=%s\n", tool, scope)
}

func (l *ledger) Elevated(_ permission.Subject, _, _ string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.grants > 0
}

func (l *ledger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.grants
}

var _ permission.Elevator = (*ledger)(nil)

// prompts 统计执行选择页面被呈现的次数，并把透出的请求字段打印出来。
type prompts struct {
	mu    sync.Mutex
	count int
}

func (p *prompts) choice(decide func(tool string) permission.ApprovalResponse) permission.ApprovalHandler {
	return func(actx *permission.ApprovalContext) (*permission.ApprovalResponse, error) {
		p.mu.Lock()
		p.count++
		p.mu.Unlock()
		req := actx.Request
		fmt.Printf("    [选择页面] id=%s session=%s risk=%s timeout=%v preview=%s\n",
			req.ID, req.SessionID, req.Risk, req.Timeout, req.Preview)
		resp := decide(req.ToolName)
		return &resp, nil
	}
}

func (p *prompts) get() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.count
}

// sandbox 是 harness 提供的位挂载点（permission.BitEnforcer）：框架只调用它。
// 这里示范「只对受保护路径的写操作介入」，其余返回 ok=false 表示不干预。
type sandbox struct{}

func (sandbox) Enforce(_ context.Context, _ permission.Subject, meta roottools.ToolMeta, _ string, argsJSON string) (permission.Action, bool) {
	if meta.Kind == roottools.ToolKindAdmin && strings.Contains(argsJSON, "protected") {
		return permission.ActionDeny, true
	}
	return permission.ActionAllow, false
}

const (
	sessID  = "sess_42"
	engine1 = "sess_42/engine_1"
	engine2 = "sess_42/engine_2"
)

func main() {
	ctx := context.Background()

	// ── 2. 装配自由 + 判定在中间件 ──────────────────────────────────────

	baseConfig := permission.PermissionConfig{
		Mode: permission.ModeManual,
		Groups: []permission.PermissionGroup{
			{Name: "docs", Match: []string{"read_*", "write_*"}, Mode: permission.BitRead | permission.BitWrite, Default: permission.ActionAllow},
			{Name: "ops", Match: []string{"delete_*"}, Mode: permission.BitExecute, Default: permission.ActionAsk},
		},
		// 授权表：engine（会话下的引擎）→ 簇 → 位。
		Subjects: map[permission.Subject]permission.SubjectGrant{
			engine1: {Bits: map[string]permission.GrantBit{"docs": {Bits: permission.BitRead | permission.BitWrite}}},
			engine2: {Bits: map[string]permission.GrantBit{
				"docs": {Bits: permission.BitRead | permission.BitWrite},
				"ops":  {Bits: permission.BitExecute},
			}},
		},
	}

	entries := []roottools.ToolEntry{
		toolEntry("read_doc", roottools.ToolKindRead, "docs", permission.BitRead),
		toolEntry("write_doc", roottools.ToolKindWrite, "docs", permission.BitWrite),
		toolEntry("delete_file", roottools.ToolKindAdmin, "ops", permission.BitExecute),
		toolEntry("ctl_stop", roottools.ToolKindControl, "ops", permission.BitExecute),
	}

	callArgs := func(registry *roottools.Registry, engine, tool, argsJSON string) {
		toolCtx := permission.WithSessionID(permission.WithEngine(ctx, permission.Engine(engine)), sessID)
		out, err := registry.Dispatch(toolCtx, roottools.ToolCall{Name: tool, ArgumentsJSON: argsJSON})
		switch {
		case err == nil:
			fmt.Printf("  %-14s %-12s -> OK   %s\n", engine, tool, out)
		case errors.Is(err, roottools.ErrToolNotVisible):
			fmt.Printf("  %-14s %-12s -> DENIED(不可见) %v\n", engine, tool, err)
		default:
			fmt.Printf("  %-14s %-12s -> DENIED  %v\n", engine, tool, err)
		}
	}
	call := func(registry *roottools.Registry, engine, tool string) {
		callArgs(registry, engine, tool, `{}`)
	}

	// ── A. 拒绝 ⇒ 执行选择页面（审批只给单次提权） ──────────────────────

	fmt.Println("A. 判定为 manual + harness 位挂载点；harness 提供执行选择页面（仅 delete_file 批准）：")

	pageA := &prompts{}
	gateA := &permission.Gate{
		Checker:  permission.NewPermissionChecker(baseConfig),
		Enforcer: sandbox{}, // 位挂载点：只对受保护路径的写操作介入
		Approval: pageA.choice(func(tool string) permission.ApprovalResponse {
			if tool == "delete_file" {
				return permission.ApprovalResponse{Choice: "allow"} // 单次：不传 Scope
			}
			return permission.ApprovalResponse{Choice: "deny"}
		}),
	}
	registryA := assemble(gateA, entries)

	call(registryA, engine1, "read_doc")
	call(registryA, engine1, "write_doc")
	fmt.Println("  -- delete_file 不可见（缺 ops 位）；批准后只放行本次调用 --")
	call(registryA, engine1, "delete_file")
	call(registryA, engine1, "delete_file") // 再次询问：单次提权不持久
	fmt.Println("  -- engine_2 位齐但 sandbox 拒绝（/etc/protected）；enforcer 的 deny 同样走选择页面 --")
	callArgs(registryA, engine2, "delete_file", `{"path":"/etc/protected"}`)
	fmt.Println("  -- 控制类簇属默认仅 root；选择页面被拒 ⇒ 英文错误 --")
	call(registryA, engine1, "ctl_stop")
	call(registryA, "", "read_doc")
	fmt.Printf("  执行选择页面呈现次数 = %d\n", pageA.get())

	// ── B. 即使是全权（full_access）也给出执行选择页面 ──────────────────

	fmt.Println("\nB. 判定为 full_access（全权）；控制类被拒时仍给出执行选择页面：")

	pageB := &prompts{}
	gateB := &permission.Gate{
		Checker: permission.NewPermissionChecker(permission.PermissionConfig{
			Mode:     permission.ModeFullAccess,
			Subjects: baseConfig.Subjects,
		}),
		Approval: pageB.choice(func(string) permission.ApprovalResponse {
			return permission.ApprovalResponse{Choice: "allow"}
		}),
	}
	registryB := assemble(gateB, entries)

	call(registryB, engine1, "delete_file") // 全权放行，不询问
	call(registryB, engine1, "ctl_stop")    // 控制类仍被拒 ⇒ 选择页面 ⇒ 单次放行
	fmt.Printf("  执行选择页面呈现次数 = %d（仅控制类那次）\n", pageB.get())

	// ── C. 提权由 harness 持有：框架只调用 Elevator 接口 ─────────────────

	fmt.Println("\nC. harness 持有提权台账（permission.Elevator）：")
	fmt.Println("   ops 簇默认 ask；首次 ask 走选择页面，Scope=session 的提权记在 harness：")

	harnessLedger := &ledger{}
	checkerC := permission.NewPermissionChecker(permission.PermissionConfig{
		Mode:     permission.ModeManual,
		Groups:   baseConfig.Groups,
		Subjects: baseConfig.Subjects,
	})
	checkerC.SetElevationSource(harnessLedger) // harness 接管提权读写

	pageC := &prompts{}
	gateC := &permission.Gate{
		Checker: checkerC,
		Approval: pageC.choice(func(string) permission.ApprovalResponse {
			return permission.ApprovalResponse{Choice: "allow", Scope: permission.ScopeSession}
		}),
		Elevation: harnessLedger, // harness 自己的台账
	}
	registryC := assemble(gateC, entries)

	call(registryC, engine2, "delete_file")
	call(registryC, engine2, "delete_file") // harness 已提权 ⇒ 不再询问
	fmt.Printf("  执行选择页面呈现次数 = %d；harness 记录提权 = %d 次\n", pageC.get(), harnessLedger.count())
}

// assemble 演示「装配自由」：任意 provider、任意顺序，门槛全在中间件。
func assemble(gate *permission.Gate, entries []roottools.ToolEntry) *roottools.Registry {
	provider, err := roottools.NewFunctionProvider("demo", entries...)
	if err != nil {
		panic(err)
	}
	registry := roottools.NewRegistry(roottools.WithMetaMiddleware(gate.Middleware()))
	if err := registry.Register(provider); err != nil {
		panic(err)
	}
	return registry
}
