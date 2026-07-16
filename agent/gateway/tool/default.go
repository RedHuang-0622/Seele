package toolgw

import (
	"context"
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/agent/core/tool/permission"
	holder "github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	"github.com/RedHuang-0622/Seele/types"
)

// DefaultGateway 是基于 agent/holder.Holder 的默认工具网关实现。
// 支持权限门控（Permission Gate）和审批回调。
type DefaultGateway struct {
	mu sync.RWMutex

	holder          *holder.Holder
	permChecker     *permission.PermissionChecker
	approvalHandler permission.ApprovalHandler
}

// NewDefaultGateway 创建基于指定 Holder 的默认工具网关。
func NewDefaultGateway(holder *holder.Holder) *DefaultGateway {
	return &DefaultGateway{holder: holder}
}

// Tools 返回全量已注册工具列表。
func (g *DefaultGateway) Tools() []types.Tool {
	return g.holder.Tools()
}

// VisibleTools 返回对 LLM 可见的工具列表。
func (g *DefaultGateway) VisibleTools(ctx context.Context) []types.Tool {
	tools := g.holder.Tools()
	if g.holder.IsPluginActive() {
		return g.holder.Plugin().Filter(tools)
	}
	return tools
}

// Dispatch 执行前进行权限门控检查（Permission Gate），通过后委托 Holder 执行。
func (g *DefaultGateway) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	if err := g.checkPermission(ctx, name, argsJSON); err != nil {
		return "", err
	}
	return g.holder.Dispatch(ctx, name, argsJSON)
}

// checkPermission 执行权限检查，ResultAsk 时阻塞等待用户审批。
func (g *DefaultGateway) checkPermission(ctx context.Context, name, argsJSON string) error {
	g.mu.RLock()
	pc := g.permChecker
	g.mu.RUnlock()
	if pc == nil {
		return nil
	}

	switch pc.Check(name, argsJSON) {
	case permission.ResultAllow:
		return nil
	case permission.ResultDeny:
		return fmt.Errorf("permission denied: tool %q is not allowed by policy", name)
	case permission.ResultAsk:
		g.mu.RLock()
		handler := g.approvalHandler
		g.mu.RUnlock()
		if handler == nil {
			return nil // 无审批处理器时降级放行
		}
		req := permission.ApprovalRequest{
			ToolName:  name,
			Arguments: argsJSON,
			Preview:   formatPreview(name, argsJSON),
			Risk:      assessRisk(name),
			Options:   permission.DefaultApproveOptions(),
		}
		resp, err := handler(&permission.ApprovalContext{Request: req})
		if err != nil {
			return fmt.Errorf("permission denied: %w", err)
		}
		if resp.Choice == "deny" || resp.Choice == "__CANCEL__" {
			return fmt.Errorf("permission denied by user")
		}
		// "始终允许" 记忆
		if resp.Choice == "always" || resp.Remember {
			pc.AddAllowRule(name, argsJSON)
		}
		return nil
	}
	return nil
}

// SetPermissionConfig 设置权限规则和审批处理器（线程安全，支持运行时更新）。
func (g *DefaultGateway) SetPermissionConfig(cfg permission.PermissionConfig, handler permission.ApprovalHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(cfg.Rules) > 0 {
		g.permChecker = permission.NewPermissionChecker(cfg)
	} else {
		g.permChecker = nil
	}
	g.approvalHandler = handler
}

// SetApprovalHandler 设置审批处理器（线程安全）。
func (g *DefaultGateway) SetApprovalHandler(handler permission.ApprovalHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.approvalHandler = handler
}

// ActivatePlugin 委托给 Holder.ActivatePlugin 激活插件。
func (g *DefaultGateway) ActivatePlugin(name string) error {
	return g.holder.ActivatePlugin(name)
}

// ActivePlugin 委托给 Holder.ActivePlugin 返回当前激活插件名。
func (g *DefaultGateway) ActivePlugin() string {
	return g.holder.ActivePlugin()
}

// DeactivatePlugin 委托给 Holder.DeactivatePlugin 停用插件。
func (g *DefaultGateway) DeactivatePlugin() {
	g.holder.DeactivatePlugin()
}

// ─── 辅助函数 ──────────────────────────────────────────────────────

func formatPreview(name, args string) string {
	s := name + "("
	if len(args) > 80 {
		s += args[:80] + "..."
	} else {
		s += args
	}
	return s + ")"
}

func assessRisk(name string) string {
	switch name {
	case "bash", "shell_exec", "exec":
		return "high"
	case "edit", "write_file", "create_file", "delete", "rename", "rm", "mv":
		return "medium"
	default:
		return "low"
	}
}
