package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
)

// Handler 通过 stdio/SSE 调用 MCP Server 工具。实现 interfaces.ToolHandler 接口。
type Handler struct {
	Client   *mcpclient.Client
	ToolName string
}

func (h *Handler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("mcp.Handler: parse args for %q: %w", h.ToolName, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = h.ToolName
	req.Params.Arguments = args

	result, err := h.Client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%w: mcp.Handler: call %q: %v", interfaces.ErrToolUnavailable, h.ToolName, err)
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
