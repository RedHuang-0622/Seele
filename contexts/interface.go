package seelectx

import (
	"context"

	types "github.com/RedHuang-0622/Seele/types"
)

// ApprovalCallback is called when a dispatched tool returns an awaiting_approval
// response. The implementation should present the options to the user, collect
// their choice, and return the choice key.
//
// Returns the user's choice key (e.g., "execute", "skip", "abort").
// Returns an error if the user cancels or input cannot be collected.
type ApprovalCallback func(ctx context.Context, approvalJSON string) (choice string, err error)

// ToolDispatcher 是工具注册与调度的抽象接口。
// tool_holder.Holder 实现此接口。
type ToolDispatcher interface {
	Tools() []types.Tool
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)
}
