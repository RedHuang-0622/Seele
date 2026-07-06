// Package tool 定义工具提供与执行的统一抽象。
//
// ToolHandler、ToolEntry、ToolProvider 是所有工具来源
//（Hub、MCP、Inline）共同遵循的契约，消除 provider 对 tool_holder 的隐式耦合。
package tool

import (
	"context"
	"errors"

	types "github.com/RedHuang-0622/Seele/types"
)

// ErrToolUnavailable 表示工具暂时不可达（连接池满、超时、网络抖动等），
// 与工具返回的业务错误不同。调用方可据此决定重试而非将错误注入对话历史。
var ErrToolUnavailable = errors.New("tool temporarily unavailable")

// ToolHandler 是工具执行的策略接口。
//
// 三种实现：
//   - HubToolHandler    — gRPC 调用远程 microHub Skill 进程
//   - MCPToolHandler    — stdio/SSE 调用 MCP Server
//   - InlineToolHandler — 直接调用本地 Go 函数
type ToolHandler interface {
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// ToolEntry 是所有 provider 向 tool_holder 暴露的统一结构。
// 不管工具来源是 gRPC、MCP 还是 Go 函数，tool_holder 看到的都是
// 一样的 {Definition + Handler} 组合。
type ToolEntry struct {
	Definition types.Tool
	Handler    ToolHandler
}

// ToolProvider 是所有工具来源的抽象接口。
//
// 每次调用 Tools() 实时查询，支持热更新（MCP 动态增减、Hub 在线/离线变化）。
// tool_holder 通过此接口聚合所有来源，agent 层完全不接触该接口。
type ToolProvider interface {
	ProviderName() string
	Tools() []ToolEntry
}
