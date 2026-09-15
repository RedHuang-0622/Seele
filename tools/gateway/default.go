package gateway

import (
	"context"
	"fmt"
	"sync"

	roottools "github.com/RedHuang-0622/Seele/tools"
	holder "github.com/RedHuang-0622/Seele/tools/holder"
	"github.com/RedHuang-0622/Seele/tools/permission"
	"github.com/RedHuang-0622/Seele/types"
)

// DefaultGateway 是基于 agent/holder.Holder 的默认工具网关实现。
// 支持权限门控（Permission Gate）。
type DefaultGateway struct {
	mu sync.RWMutex

	holder          *holder.Holder
	permChecker     *permission.PermissionChecker
	approvalHandler permission.ApprovalHandler

	// v0.3.0：主体解析与位挂载点，均为可选（nil = 旧行为）。
	subjectResolver permission.SubjectResolver
	bitEnforcer     permission.BitEnforcer
}

func NewDefaultGateway(holder *holder.Holder) *DefaultGateway {
	return &DefaultGateway{holder: holder}
}

func (g *DefaultGateway) Tools() []types.Tool {
	return g.holder.Tools()
}

// VisibleTools 在插件可见性之上再叠加主体可见性：断位工具（不在 PATH）与
// 非 root 的控制类工具不会出现在返回列表中。
func (g *DefaultGateway) VisibleTools(ctx context.Context) []types.Tool {
	tools := g.holder.Tools()
	if g.holder.IsPluginActive() {
		tools = g.holder.Plugin().Filter(tools)
	}

	g.mu.RLock()
	pc := g.permChecker
	g.mu.RUnlock()

	subject := g.Subject(ctx)
	filtered := make([]types.Tool, 0, len(tools))
	for _, tool := range tools {
		name := tool.Function.Name
		if meta, ok := g.toolMeta(name); ok && meta.Kind == roottools.ToolKindControl && subject != permission.SubjectRoot {
			continue
		}
		if pc != nil && !pc.VisibleFor(subject, name) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}

// Dispatch 执行前进行权限门控检查。
func (g *DefaultGateway) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	if err := g.checkPermission(ctx, name, argsJSON); err != nil {
		return "", err
	}
	return g.holder.Dispatch(ctx, name, argsJSON)
}

// checkPermission 把调用判定映射为可 errors.Is 辨别的两类错误：
// 断位 ⇒ tools.ErrToolNotVisible（不在 PATH）；位齐被拒 ⇒ tools.ErrPermissionDenied。
func (g *DefaultGateway) checkPermission(ctx context.Context, name, argsJSON string) error {
	g.mu.RLock()
	pc := g.permChecker
	enforcer := g.bitEnforcer
	g.mu.RUnlock()

	subject := g.Subject(ctx)
	meta, hasMeta := g.toolMeta(name)

	// R8：位与沙箱 / 路径的挂载点。框架只调用它，本身不含路径解析逻辑。
	if enforcer != nil && hasMeta {
		if action, ok := enforcer.Enforce(ctx, subject, meta, name, argsJSON); ok {
			return g.applyDecision(ctx, subject, name, argsJSON, resultOf(action))
		}
	}

	// R6：控制类工具默认仅 root 可路由。
	if hasMeta && meta.Kind == roottools.ToolKindControl && subject != permission.SubjectRoot {
		return invisibleErr(name)
	}

	if pc == nil {
		return nil
	}
	// R7：按工具自带簇属（ToolMeta.Groups/Kind/Bits）判定；工具未声明簇属时
	// DecideForMeta 退回名字路由，行为与旧路径一致。断位 ⇒ 不可见（不在 PATH）。
	result, visible := pc.DecideForMeta(subject, name, meta, argsJSON)
	if !visible {
		return invisibleErr(name)
	}
	return g.applyDecision(ctx, subject, name, argsJSON, result)
}

// invisibleErr 返回工具不在主体命名空间时的英文错误（断位，类似「不在 PATH」）。
func invisibleErr(name string) error {
	return &permission.InvisibilityError{Tool: name}
}

// applyDecision 执行动作：allow 放行、deny 返回 ErrPermissionDenied、ask 走审批。
func (g *DefaultGateway) applyDecision(ctx context.Context, subject permission.Subject, name, argsJSON string, result permission.CheckResult) error {
	switch result {
	case permission.ResultAllow:
		return nil
	case permission.ResultAsk:
		return g.approve(ctx, subject, name, argsJSON)
	default: // ResultDeny
		return &permission.DenialError{Tool: name}
	}
}

// approve 走审批流程；批准时按 Scope 记录提权并发出审计事件。
func (g *DefaultGateway) approve(ctx context.Context, subject permission.Subject, name, argsJSON string) error {
	g.mu.RLock()
	pc := g.permChecker
	handler := g.approvalHandler
	g.mu.RUnlock()
	if handler == nil {
		return nil
	}
	req := permission.NewApprovalRequest(ctx, name, g.metaOf(name), argsJSON, 0)
	resp, err := handler(&permission.ApprovalContext{Context: ctx, Request: req})
	if err != nil {
		return fmt.Errorf("%w (approval failed: %v)", &permission.DenialError{Tool: name}, err)
	}
	if resp == nil || resp.Choice == "deny" || resp.Choice == "__CANCEL__" {
		return &permission.DenialError{Tool: name}
	}
	if resp.Choice == "always" || resp.Remember {
		if pc != nil {
			pc.AddAllowRule(name, argsJSON)
		}
	}
	if pc != nil {
		pc.GrantElevation(subject, resp.Scope, name, argsJSON)
	}
	return nil
}

// resultOf 把位模型动作映射为判定结果。
func resultOf(action permission.Action) permission.CheckResult {
	switch action {
	case permission.ActionAllow:
		return permission.ResultAllow
	case permission.ActionDeny:
		return permission.ResultDeny
	default:
		return permission.ResultAsk
	}
}

// SetPermissionConfig 设置权限规则和审批处理器。只要 Rules、Groups 或
// Subjects 任一非空就启用权限门控，因此「只写 Groups 不写 Rules」也生效。
func (g *DefaultGateway) SetPermissionConfig(cfg permission.PermissionConfig, handler permission.ApprovalHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(cfg.Rules) > 0 || len(cfg.Groups) > 0 || len(cfg.Subjects) > 0 {
		g.permChecker = permission.NewPermissionChecker(cfg)
	} else {
		g.permChecker = nil
	}
	g.approvalHandler = handler
}

func (g *DefaultGateway) SetApprovalHandler(handler permission.ApprovalHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.approvalHandler = handler
}

// SetSubjectResolver 安装主体解析器（R1）。未安装时主体为匿名。
func (g *DefaultGateway) SetSubjectResolver(resolver permission.SubjectResolver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.subjectResolver = resolver
}

// SetBitEnforcer 安装位与沙箱 / 路径的挂载点（R8）。传 nil 卸载。
func (g *DefaultGateway) SetBitEnforcer(enforcer permission.BitEnforcer) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bitEnforcer = enforcer
}

// Subject 返回当前调用主体；未安装 resolver 时为空（匿名）。
func (g *DefaultGateway) Subject(ctx context.Context) permission.Subject {
	g.mu.RLock()
	resolver := g.subjectResolver
	g.mu.RUnlock()
	if resolver == nil {
		return permission.SubjectAnonymous
	}
	return resolver(ctx)
}

// ToolMeta 按工具名返回元数据（含控制信号），供上层 loop 读取 Signal。
func (g *DefaultGateway) ToolMeta(name string) (roottools.ToolMeta, bool) {
	return g.toolMeta(name)
}

func (g *DefaultGateway) toolMeta(name string) (roottools.ToolMeta, bool) {
	entry, ok := g.holder.Entry(name)
	if !ok || entry.Meta == nil {
		return roottools.ToolMeta{}, false
	}
	return *entry.Meta, true
}

// metaOf 返回工具簇属；未声明时为零值（判定与风险推断退回按名回退）。
func (g *DefaultGateway) metaOf(name string) roottools.ToolMeta {
	meta, _ := g.toolMeta(name)
	return meta
}

// riskOf 由簇属推断风险等级（委托 permission.RiskOf），无 Meta 时按名回退。
func (g *DefaultGateway) riskOf(name string) string {
	return permission.RiskOf(name, g.metaOf(name))
}

func (g *DefaultGateway) ActivatePlugin(name string) error {
	return g.holder.ActivatePlugin(name)
}

func (g *DefaultGateway) ActivePlugin() string {
	return g.holder.ActivePlugin()
}

func (g *DefaultGateway) DeactivatePlugin() {
	g.holder.DeactivatePlugin()
}
