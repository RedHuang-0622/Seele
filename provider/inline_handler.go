package provider

import (
	"context"
)

// InlineToolHandler 直接调用本地 Go 函数，零网络、零序列化开销。
// 实现 ToolHandler 接口。
type InlineToolHandler struct {
	Fn func(ctx context.Context, argsJSON string) (string, error)
}

func (h *InlineToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	return h.Fn(ctx, argsJSON)
}
