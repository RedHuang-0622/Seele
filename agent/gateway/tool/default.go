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
// 支持权限门控（Permission Gate）。
type DefaultGateway struct {
	mu sync.RWMutex

	holder          *holder.Holder
	permChecker     *permission.PermissionChecker
	approvalHandler permission.ApprovalHandler
}

func NewDefaultGateway(holder *holder.Holder) *DefaultGateway {
	return &DefaultGateway{holder: holder}
}

func (g *DefaultGateway) Tools() []types.Tool {
	return g.holder.Tools()
}

func (g *DefaultGateway) VisibleTools(ctx context.Context) []types.Tool {
	tools := g.holder.Tools()
	if g.holder.IsPluginActive() {
		return g.holder.Plugin().Filter(tools)
	}
	return tools
}

// Dispatch 执行前进行权限门控检查。
func (g *DefaultGateway) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	if err := g.checkPermission(ctx, name, argsJSON); err != nil {
		return "", err
	}
	return g.holder.Dispatch(ctx, name, argsJSON)
}

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
			return nil
		}
		req := permission.ApprovalRequest{
			ToolName: name,
			Arguments: argsJSON,
			Preview:  formatPreview(name, argsJSON),
			Risk:     assessRisk(name),
			Options:  permission.DefaultApproveOptions(),
		}
		resp, err := handler(&permission.ApprovalContext{Request: req})
		if err != nil {
			return fmt.Errorf("permission denied: %w", err)
		}
		if resp.Choice == "deny" || resp.Choice == "__CANCEL__" {
			return fmt.Errorf("permission denied by user")
		}
		if resp.Choice == "always" || resp.Remember {
			pc.AddAllowRule(name, argsJSON)
		}
		return nil
	}
	return nil
}

// SetPermissionConfig 设置权限规则和审批处理器。
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

func (g *DefaultGateway) SetApprovalHandler(handler permission.ApprovalHandler) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.approvalHandler = handler
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
