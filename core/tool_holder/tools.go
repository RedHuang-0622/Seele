package tool_holder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/sukasukasuka123/Seele/core/session"
	"github.com/sukasukasuka123/Seele/provider"
	types "github.com/sukasukasuka123/Seele/types"
)

// 编译期断言：*Holder 实现了 session.ToolDispatcher。
var _ session.ToolDispatcher = (*Holder)(nil)

// Tools 聚合所有已注册 provider 的工具列表。
// 每次调用都实时读取，支持热更新（如 MCP Server 动态增减工具）。
func (h *Holder) Tools() []types.Tool {
	h.mu.RLock()
	providers := h.providers
	h.mu.RUnlock()

	var result []types.Tool
	for _, p := range providers {
		result = append(result, p.Tools()...)
	}
	return result
}

// Dispatch 根据工具名路由到对应 provider 并执行。
//
// 路由规则：按注册顺序，找到第一个 HasTool 返回 true 的 provider。
//
// 瞬时错误（provider.ErrToolUnavailable）自动重试（次数和间隔由 Holder 配置）。
// 调用方只看到最终成功或永久失败，无需关心重试细节。
func (h *Holder) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	h.mu.RLock()
	providers := h.providers
	h.mu.RUnlock()

	var lastErr error
	for attempt := 0; attempt < h.DispatchRetries; attempt++ {
		for _, p := range providers {
			if p.HasTool(name) {
				result, err := p.Dispatch(ctx, name, argsJSON)
				if err == nil {
					return result, nil
				}
				if !errors.Is(err, provider.ErrToolUnavailable) {
					return "", err
				}
				lastErr = err
				log.Printf("[tool_holder.dispatch] tool %q UNAVAILABLE, retry %d/%d: %v", name, attempt+1, h.DispatchRetries, err)
				break
			}
		}
		if lastErr == nil {
			return "", fmt.Errorf("tool_holder.dispatch: tool %q not found in any provider", name)
		}
		if attempt < h.DispatchRetries-1 {
			time.Sleep(h.DispatchRetryDelay)
		}
	}

	return "", fmt.Errorf("tool_holder.dispatch: tool %q unavailable after %d retries: %w", name, h.DispatchRetries, lastErr)
}
