package mcp

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
)

// Handler 通过 stdio/SSE 调用 MCP Server 工具。实现 interfaces.ToolHandler 接口。
type Handler struct {
	Client     *mcpclient.Client
	ToolName   string
	ServerName string       // 所属 MCP server（用于熔断器 key）
	breaker    *mcpBreaker  // 共享熔断器实例
}

func (h *Handler) Execute(ctx context.Context, argsJSON string) (string, error) {
	// ── 熔断器检查 ──
	if h.breaker != nil && h.ServerName != "" {
		if err := h.breaker.beforeCall(h.ServerName); err != nil {
			// 熔断中：返回降级消息，不标记 ErrToolUnavailable
			// 这样 holder 不会重试，LLM 也能看到明确的指引
			return "", fmt.Errorf("mcp: server %q is currently unavailable (%w). please wait or use alternative tools",
				h.ServerName, err)
		}
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("mcp.Handler: parse args for %q: %w", h.ToolName, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = h.ToolName
	req.Params.Arguments = args

	result, err := h.Client.CallTool(ctx, req)
	if err != nil {
		// 区分连接错误与业务逻辑错误
		connErr := isConnectivityError(err)
		// 操作超时 / 用户取消 ≠ 连接故障，不计入熔断
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			connErr = false
		}

		if h.breaker != nil && h.ServerName != "" {
			h.breaker.afterCall(h.ServerName, connErr)
			// 如果是连接错误且熔断器已打开，启动后台恢复 ping
			if connErr && h.breaker.isOpen(h.ServerName) {
				h.breaker.startRecovery(h.ServerName, h.pingServer)
			}
		}

		// 连接错误降级返回（不标记 ErrToolUnavailable 避免 holder 疯狂重试）
		if connErr {
			return "", fmt.Errorf("mcp: server %q connection lost: %v", h.ServerName, err)
		}
		// 业务逻辑错误：正常标记 ErrToolUnavailable（非连接问题，允许重试）
		return "", fmt.Errorf("%w: mcp.Handler: call %q: %v", interfaces.ErrToolUnavailable, h.ToolName, err)
	}

	// 调用成功 → 重置熔断计数器
	if h.breaker != nil && h.ServerName != "" {
		h.breaker.afterCall(h.ServerName, false)
	}

	var parts []string
	for _, c := range result.Content {
		text := mcp.GetTextFromContent(c)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// pingServer 轻量探活：调 MCP Ping 确认 server 恢复。
func (h *Handler) pingServer(ctx context.Context) error {
	return h.Client.Ping(ctx)
}
