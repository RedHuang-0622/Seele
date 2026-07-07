// Package interfaces 定义工具提供与执行的统一抽象。
//
// ToolHandler、ToolEntry、ToolProvider 是所有工具来源
//（Hub、MCP、内联）共同遵循的契约。
package interfaces

import (
	"context"
	"errors"

	types "github.com/RedHuang-0622/Seele/types"
)

// ErrToolUnavailable 表示工具暂时不可达（连接池满、超时、网络抖动等），
// 与工具返回的业务错误不同。调用方可据此决定重试而非将错误注入对话历史。
var ErrToolUnavailable = errors.New("tool temporarily unavailable")

// ToolHandler 是工具执行的策略接口。
type ToolHandler interface {
	Execute(ctx context.Context, argsJSON string) (string, error)
}

// ToolEntry 是所有 provider 向 holder 暴露的统一结构。
type ToolEntry struct {
	Definition   types.Tool
	Handler      ToolHandler
	OutputSchema map[string]interface{} // nil 表示无结构化输出约束
}

// ToolProvider 是所有工具来源的抽象接口。
type ToolProvider interface {
	ProviderName() string
	Tools() []ToolEntry
}
