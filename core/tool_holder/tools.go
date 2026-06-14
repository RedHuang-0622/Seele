package tool_holder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/core/session"
	"github.com/RedHuang-0622/Seele/provider"
	types "github.com/RedHuang-0622/Seele/types"
)

// 编译期断言：*Holder 实现了 session.ToolDispatcher。
var _ session.ToolDispatcher = (*Holder)(nil)

// Tools 聚合所有已注册 provider 的工具列表。
// 每次调用实时重建 map（支持热更新），统一过滤 _ 前缀内部工具。
func (h *Holder) Tools() []types.Tool {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.rebuildLocked()

	var result []types.Tool
	for name, entry := range h.toolMap {
		if strings.HasPrefix(name, "_") {
			continue // 框架内部工具，LLM 不可见
		}
		result = append(result, entry.Definition)
	}
	return result
}

// Dispatch 通过 map 查找 handler 并执行。O(1) 路由，策略模式。
//
// 瞬时错误（provider.ErrToolUnavailable）自动重试。
// 业务错误直接返回，不重试。
func (h *Holder) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	h.mu.RLock()
	entry, ok := h.toolMap[name]
	h.mu.RUnlock()

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
