package toolgw

import (
	"context"

	holder "github.com/RedHuang-0622/Seele/agent/tool/holder"
	"github.com/RedHuang-0622/Seele/types"
)

// DefaultGateway 是基于 agent/holder.Holder 的默认工具网关实现。
type DefaultGateway struct {
	holder *holder.Holder
}

// NewDefaultGateway 创建基于指定 Holder 的默认工具网关。
func NewDefaultGateway(holder *holder.Holder) *DefaultGateway {
	return &DefaultGateway{holder: holder}
}

// Tools 返回全量已注册工具列表（不受插件影响）。
func (g *DefaultGateway) Tools() []types.Tool {
	return g.holder.Tools()
}

// VisibleTools 返回对 LLM 可见的工具列表。
// 当 Holder 的插件管理器中存在激活插件时，返回插件过滤后的子集；
// 否则返回全量工具列表。
func (g *DefaultGateway) VisibleTools(ctx context.Context) []types.Tool {
	tools := g.holder.Tools()
	if g.holder.IsPluginActive() {
		return g.holder.Plugin().Filter(tools)
	}
	return tools
}

// Dispatch 委托给 Holder.Dispatch 执行工具调用。
func (g *DefaultGateway) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	return g.holder.Dispatch(ctx, name, argsJSON)
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
