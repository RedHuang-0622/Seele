package tool_holder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/RedHuang-0622/Seele/core/session"
	"github.com/RedHuang-0622/Seele/provider"
	types "github.com/RedHuang-0622/Seele/types"
)

// 编译期断言：*Holder 实现了 session.ToolDispatcher。
var _ session.ToolDispatcher = (*Holder)(nil)

// Tools 返回所有已注册工具的 LLM 可见定义列表。
//
// v0.4 优化：通过 atomic.Pointer 读取预过滤的 toolList，零锁开销。
// 写路径（Register/Unregister）分配新 holderState 后原子替换指针，
// 读路径始终看到一致的快照。
func (h *Holder) Tools() []types.Tool {
	return h.state.Load().toolList
}

// Dispatch 通过 map 查找 handler 并执行。O(1) 路由，策略模式。
//
// v0.4 优化：通过 atomic.Pointer 读取 toolMap，零锁开销。
// 瞬时错误（provider.ErrToolUnavailable）自动重试。
// 业务错误直接返回，不重试。
func (h *Holder) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	st := h.state.Load()
	entry, ok := st.toolMap[name]
	if !ok {
		return "", fmt.Errorf("tool_holder.dispatch: tool %q not found in any provider", name)
	}

	var lastErr error
	for attempt := 0; attempt < h.DispatchRetries; attempt++ {
		result, err := entry.Handler.Execute(ctx, argsJSON)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, provider.ErrToolUnavailable) {
			return "", err
		}
		lastErr = err
		log.Printf("[tool_holder.dispatch] tool %q UNAVAILABLE, retry %d/%d: %v",
			name, attempt+1, h.DispatchRetries, err)
		if attempt < h.DispatchRetries-1 {
			time.Sleep(h.DispatchRetryDelay)
		}
	}

	return "", fmt.Errorf("tool_holder.dispatch: tool %q unavailable after %d retries: %w",
		name, h.DispatchRetries, lastErr)
}
