// Package toolgw 提供工具网关抽象。
//
// Gateway 负责管理 LLM 可见的工具列表、调度工具执行、
// 以及通过插件装配件机制切换工具可见性范围。
package toolgw

import (
	"context"

	"github.com/RedHuang-0622/Seele/types"
)

// Gateway 是工具网关的抽象接口。
type Gateway interface {
	// Tools 返回全量已注册工具列表（不受插件影响）。
	Tools() []types.Tool

	// VisibleTools 返回当前对 LLM 可见的工具列表。
	// 当有激活的插件时返回过滤后的子集，否则返回全量工具。
	VisibleTools(ctx context.Context) []types.Tool

	// Dispatch 根据工具名路由到对应 handler 并执行。
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)

	// ActivatePlugin 激活指定插件，切换工具可见性规则。
	ActivatePlugin(name string) error

	// ActivePlugin 返回当前激活的插件名。"" 表示 all-tools 模式。
	ActivePlugin() string

	// DeactivatePlugin 停用当前插件，回到 all-tools 模式。
	DeactivatePlugin()
}
