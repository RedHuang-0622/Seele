package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPToolHandler 通过 stdio/SSE 调用 MCP Server 工具。
// 实现 ToolHandler 接口。
type MCPToolHandler struct {
	Client   *mcpclient.Client
	ToolName string // MCP server 侧的工具名（未加前缀的原始名）
}

func (h *MCPToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("MCPToolHandler: parse args for %q: %w", h.ToolName, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = h.ToolName
	req.Params.Arguments = args

	result, err := h.Client.CallTool(ctx, req)
	if err != nil {
		// 连接断开等传输错误标记为瞬时不可用
		return "", fmt.Errorf("%w: MCPToolHandler: call %q: %v", ErrToolUnavailable, h.ToolName, err)
	}

	// 提取 content 文本
	var parts []string
	for _, c := range result.Content {
		text := mcp.GetTextFromContent(c)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n"), nil
}
