package permission

import (
	"context"
	"fmt"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

// 拒绝时返回给模型 / 宿主的英文错误。判定在中间件完成，因此这两条消息也是
// 产品面看到的最终文案；两条共享同一英文前缀，errors.Is 仍能区分两类拒绝。
const (
	deniedFormat     = `permission denied: cannot complete the invocation of tool %q`
	notVisibleFormat = `permission denied: cannot complete the invocation of tool %q: the tool is not in this engine's namespace`
)

// DenialError 是策略拒绝（EPERM）：可见但被规则 / 组默认拒绝。它 Unwrap 到
// tools.ErrPermissionDenied，因此 errors.Is 判定成立，Error() 是可直出的英文。
type DenialError struct{ Tool string }

func (e *DenialError) Error() string { return fmt.Sprintf(deniedFormat, e.Tool) }
func (e *DenialError) Unwrap() error { return roottools.ErrPermissionDenied }

// InvisibilityError 表示工具不在该引擎的命名空间（断位，类似「不在 PATH」）。
// 它 Unwrap 到 tools.ErrToolNotVisible。
type InvisibilityError struct{ Tool string }

func (e *InvisibilityError) Error() string { return fmt.Sprintf(notVisibleFormat, e.Tool) }
func (e *InvisibilityError) Unwrap() error { return roottools.ErrToolNotVisible }

// Engine 标识授权所绑定的执行引擎。授权永远发给 (session, engine)：会话把
// engine id 注入调用 context，中间件读回它并用作授权主体（Subject）。
type Engine string

type engineCtxKey struct{}

// WithEngine 把当前会话的 engine id 装入 context。
func WithEngine(ctx context.Context, engine Engine) context.Context {
	return context.WithValue(ctx, engineCtxKey{}, engine)
}

// EngineFromContext 读回 WithEngine 装入的 engine id；没有时返回 ""。
func EngineFromContext(ctx context.Context) Engine {
	if ctx == nil {
		return ""
	}
	if engine, ok := ctx.Value(engineCtxKey{}).(Engine); ok {
		return engine
	}
	return ""
}

type sessionIDCtxKey struct{}

// WithSessionID 把当前会话 id 装入 context：审批请求的 SessionID 由此透出给 harness。
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, sessionIDCtxKey{}, sessionID)
}

// SessionIDFromContext 读回 WithSessionID 装入的会话 id；没有时返回 ""。
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(sessionIDCtxKey{}).(string); ok {
		return id
	}
	return ""
}

// EngineResolver 解析一次调用所属的 engine（即授权主体）。返回 "" 等价于匿名主体。
type EngineResolver func(ctx context.Context) Engine

// Gate 是「中间件形态」的权限判定：装配保持自由（任意 provider、任意顺序），
// 只有 Gate 决定一次调用能否执行。工具自带的簇属（ToolMeta.Groups/Kind/Bits）
// 优先；工具未声明簇属时退回按名字路由。
type Gate struct {
	Checker  *PermissionChecker
	Approval ApprovalHandler
	Resolve  EngineResolver

	// Enforcer 是位与沙箱 / 路径的挂载点（BitEnforcer）：最先参与判定，ok=false 时
	// 框架不干预。框架只调用接口，本身不含任何路径 / 命令解析逻辑。
	Enforcer BitEnforcer

	// Elevation 是提权台账的挂载点。框架只调用它；提权是否持久、按什么范围复用
	// 由 harness 决定。为 nil 时退回 Checker 自带的默认台账（CheckerElevator）。
	Elevation Elevator

	// Timeout 是审批请求的超时：写入 ApprovalRequest.Timeout，<=0 时取
	// DefaultApprovalTimeout。框架只透出该值，超时语义由 harness 执行。
	Timeout time.Duration

	// DenyWithoutPrompt 关闭「拒绝也走执行选择页面」的默认行为：置 true 时拒绝
	// 直接返回英文错误。零值 false 是默认行为——只要安装了 Approval，任何拒绝
	// （不可见 / 策略拒绝 / 控制类）都会先呈现执行选择页面；通过审批只获得
	// **单次操作的提权**（仅放行本次调用，不写入任何可复用状态），因此即便是
	// 全权（full_access）或 root 之外的控制类调用也不会被永久绕过。
	DenyWithoutPrompt bool
}

// Middleware 返回可注册进 tools.Registry 的 MetaMiddleware：它在调用处理器之前
// 完成判定，拒绝时给出执行选择页面或英文错误，且不会执行该工具。
func (g *Gate) Middleware() roottools.MetaMiddleware {
	return func(name string, meta roottools.ToolMeta, next roottools.ToolHandler) roottools.ToolHandler {
		return roottools.HandlerFunc(func(ctx context.Context, argsJSON string) (string, error) {
			if err := g.Decide(ctx, name, meta, argsJSON); err != nil {
				return "", err
			}
			return next.Execute(ctx, argsJSON)
		})
	}
}

// Decide 判定一次调用。导出以便自定义中间件复用同一套语义。
func (g *Gate) Decide(ctx context.Context, name string, meta roottools.ToolMeta, argsJSON string) error {
	subject := g.subject(ctx)
	switch g.evaluate(ctx, subject, name, meta, argsJSON) {
	case outcomeAllow:
		return nil
	case outcomeAsk:
		// 组默认 ask：审批按响应里的 Scope 复用（session/tool/args）。
		return g.approve(ctx, subject, name, meta, argsJSON)
	case outcomeInvisible:
		return g.denyOrPrompt(ctx, name, meta, argsJSON, &InvisibilityError{Tool: name})
	default:
		return g.denyOrPrompt(ctx, name, meta, argsJSON, &DenialError{Tool: name})
	}
}

// outcome 是一次判定的原始结果。
type outcome int

const (
	outcomeAllow outcome = iota
	outcomeAsk
	outcomeDeny
	outcomeInvisible
)

// evaluate 只做判定，不触发任何审批副作用。顺序与网关一致：
// 位挂载点（enforcer）→ 控制类簇属 → 判定器。
func (g *Gate) evaluate(ctx context.Context, subject Subject, name string, meta roottools.ToolMeta, argsJSON string) outcome {
	// 位与沙箱 / 路径的挂载点：框架只调用它，ok=false 表示不干预。
	if g.Enforcer != nil {
		if action, ok := g.Enforcer.Enforce(ctx, subject, meta, name, argsJSON); ok {
			return outcomeOf(action)
		}
	}
	// 控制类簇属：默认仅 root 可路由。
	if meta.Kind == roottools.ToolKindControl && subject != SubjectRoot {
		return outcomeInvisible
	}
	if g.Checker == nil {
		return outcomeAllow
	}
	result, visible := g.Checker.DecideForMeta(subject, name, meta, argsJSON)
	switch {
	case !visible:
		return outcomeInvisible
	case result == ResultDeny:
		return outcomeDeny
	case result == ResultAllow:
		return outcomeAllow
	default:
		return outcomeAsk
	}
}

// outcomeOf 把位模型动作映射为判定结果（与 Rules / 网关的动作语义一致）。
func outcomeOf(action Action) outcome {
	switch action {
	case ActionAllow:
		return outcomeAllow
	case ActionDeny:
		return outcomeDeny
	default:
		return outcomeAsk
	}
}

func (g *Gate) subject(ctx context.Context) Subject {
	if g.Resolve != nil {
		return Subject(g.Resolve(ctx))
	}
	return Subject(EngineFromContext(ctx))
}

// approvalContext 组装透出给 harness 的审批上下文：ctx 原样透传（会话 id、追踪器、
// 截止时间等均可读），请求字段由 NewApprovalRequest 填齐。
func (g *Gate) approvalContext(ctx context.Context, name string, meta roottools.ToolMeta, argsJSON string) *ApprovalContext {
	return &ApprovalContext{
		Context: ctx,
		Request: NewApprovalRequest(ctx, name, meta, argsJSON, g.Timeout),
	}
}

// approve 走组默认 ask 的审批：批准时把提权交给 harness 的台账（框架只调用接口；
// 未注入 Elevation 时用 Checker 自带的默认台账）。
func (g *Gate) approve(ctx context.Context, subject Subject, name string, meta roottools.ToolMeta, argsJSON string) error {
	if g.Approval == nil {
		return &DenialError{Tool: name}
	}
	resp, err := g.Approval(g.approvalContext(ctx, name, meta, argsJSON))
	if err != nil {
		return fmt.Errorf("%w (approval failed: %v)", &DenialError{Tool: name}, err)
	}
	if resp == nil || resp.Choice == "deny" || resp.Choice == "__CANCEL__" {
		return &DenialError{Tool: name}
	}
	if (resp.Choice == "always" || resp.Remember) && g.Checker != nil {
		g.Checker.AddAllowRule(name, argsJSON)
	}
	g.elevator().Elevate(subject, resp.Scope, name, argsJSON)
	return nil
}

// elevator 返回生效的提权台账：harness 注入的优先，否则退回 Checker 默认台账。
func (g *Gate) elevator() Elevator {
	if g.Elevation != nil {
		return g.Elevation
	}
	if g.Checker != nil {
		return CheckerElevator{Checker: g.Checker}
	}
	return nopElevator{}
}

// nopElevator 用于既无判定器也无 harness 台账的纯放行装配。
type nopElevator struct{}

func (nopElevator) Elevate(Subject, string, string, string) {}
func (nopElevator) Elevated(Subject, string, string) bool   { return false }

// denyOrPrompt 实现「出现拒绝 ⇒ 给出执行选择页面」：只要安装了 Approval 且未显式
// 关闭，拒绝（含不可见、控制类）都会先询问一次；通过审批只放行本次调用，即
// **单次操作的提权**——不写 allow 缓存、不记提权，因此下一次同簇调用会再次询问。
func (g *Gate) denyOrPrompt(ctx context.Context, name string, meta roottools.ToolMeta, argsJSON string, denial error) error {
	if g.DenyWithoutPrompt || g.Approval == nil {
		return denial
	}
	resp, err := g.Approval(g.approvalContext(ctx, name, meta, argsJSON))
	if err != nil {
		return fmt.Errorf("%w (approval failed: %v)", denial, err)
	}
	if resp == nil || resp.Choice == "deny" || resp.Choice == "__CANCEL__" {
		return denial
	}
	return nil
}
