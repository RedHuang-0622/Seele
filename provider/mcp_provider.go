package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	types "github.com/sukasukasuka123/Seele/types"
)

// MCPServerConfig 单个 MCP Server 的连接配置。
type MCPServerConfig struct {
	// Name 是此 server 在 MCPProvider 内的唯一逻辑名，如 "filesystem"、"fetch"
	Name string

	// Transport 传输方式："stdio" | "sse"
	Transport string

	// --- stdio 模式 ---
	// Command 是要启动的命令，如 "npx"、"uvx"、"python"
	Command string
	// Args 是命令参数，如 []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"}
	Args []string
	// Env 是附加环境变量，格式 "KEY=VALUE"
	// 对应 NewStdioMCPClient 的第二个参数 []string
	Env []string

	// --- sse 模式 ---
	// URL 是 SSE 服务器地址，如 "http://localhost:8080/sse"
	URL string
}

// mcpServerConn 是单个 MCP Server 的运行时连接状态。
type mcpServerConn struct {
	cfg MCPServerConfig
	// client 是 mcp-go 的统一 Client 接口（Stdio/SSE 均实现此接口）
	client *mcpclient.Client
	// tools 缓存从 MCP Server 拉取的工具列表（转为内部 Tool 格式）
	tools []types.Tool
	// toolSet 是 toolName → struct{} 的快速查找表，随 tools 同步更新
	toolSet map[string]struct{}
}

// MCPProvider 管理多个 MCP Server 连接，实现 ToolProvider 接口。
//
// 特性：
//   - 支持运行时动态 Attach / Detach，线程安全
//   - 多 server 时自动加 "serverName__toolName" 前缀防命名冲突
//   - 单 server 时工具名保持原样，对 LLM 更友好
type MCPProvider struct {
	mu      sync.RWMutex
	servers map[string]*mcpServerConn // key: MCPServerConfig.Name
}

// NewMCPProvider 创建空的 MCPProvider。
// 通过 Attach 添加 MCP Server 后工具才对 LLM 可见。
func NewMCPProvider() *MCPProvider {
	return &MCPProvider{
		servers: make(map[string]*mcpServerConn),
	}
}

func (p *MCPProvider) ProviderName() string { return "mcp" }

// ServerNames 返回当前已连接的所有 MCP Server 名称。
func (p *MCPProvider) ServerNames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.servers))
	for name := range p.servers {
		names = append(names, name)
	}
	return names
}

// ── Attach / Detach ───────────────────────────────────────────────

// Attach 连接一个 MCP Server，完成握手并拉取工具列表。
// 可在 Engine 运行期间随时调用，立即对 LLM 可见。
func (p *MCPProvider) Attach(ctx context.Context, cfg MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("MCPProvider.Attach: Name must not be empty")
	}

	var c *mcpclient.Client
	var err error

	switch cfg.Transport {
	case "stdio":
		// env 格式：[]string{"KEY=VALUE", ...}，与 os.Environ() 一致
		// NewStdioMCPClient 自动启动子进程，不需要再手动 Start
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
	case "sse":
		c, err = mcpclient.NewSSEMCPClient(cfg.URL)
	default:
		return fmt.Errorf("MCPProvider.Attach: unknown transport %q (use 'stdio' or 'sse')", cfg.Transport)
	}
	if err != nil {
		return fmt.Errorf("MCPProvider.Attach: create client %q: %w", cfg.Name, err)
	}

	// MCP 握手：声明客户端信息和协议版本
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "Seele",
		Version: "1.0.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("MCPProvider.Attach: initialize %q: %w", cfg.Name, err)
	}

	// 拉取工具列表（转为内部 Tool 格式）
	tools, toolSet, err := p.fetchTools(ctx, c)
	if err != nil {
		return fmt.Errorf("MCPProvider.Attach: fetch tools from %q: %w", cfg.Name, err)
	}

	p.mu.Lock()
	p.servers[cfg.Name] = &mcpServerConn{
		cfg:     cfg,
		client:  c,
		tools:   tools,
		toolSet: toolSet,
	}
	p.mu.Unlock()

	log.Printf("[MCPProvider] attached server=%q tools=%d", cfg.Name, len(tools))
	return nil
}

// Detach 断开指定 MCP Server，其工具立即对 LLM 不可见。
func (p *MCPProvider) Detach(name string) {
	p.mu.Lock()
	if conn, ok := p.servers[name]; ok {
		// 关闭底层连接（忽略错误，进程可能已退出）
		_ = conn.client.Close()
		delete(p.servers, name)
	}
	p.mu.Unlock()
	log.Printf("[MCPProvider] detached server=%q", name)
}

// RefreshTools 重新从指定 server 拉取工具列表（热更新）。
// 适用于 MCP Server 在运行期间动态增减工具的场景。
func (p *MCPProvider) RefreshTools(ctx context.Context, serverName string) error {
	p.mu.RLock()
	conn, ok := p.servers[serverName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("MCPProvider.RefreshTools: server %q not attached", serverName)
	}

	tools, toolSet, err := p.fetchTools(ctx, conn.client)
	if err != nil {
		return fmt.Errorf("MCPProvider.RefreshTools: %q: %w", serverName, err)
	}

	p.mu.Lock()
	conn.tools = tools
	conn.toolSet = toolSet
	p.mu.Unlock()

	log.Printf("[MCPProvider] refreshed server=%q tools=%d", serverName, len(tools))
	return nil
}

// ── ToolProvider 接口实现 ─────────────────────────────────────────

// Tools 汇总所有 MCP Server 的工具。
// 多 server 时加 "serverName__toolName" 前缀防冲突；
// 单 server 时保持原始工具名，LLM 提示更简洁。
func (p *MCPProvider) Tools() []types.Tool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	multiServer := len(p.servers) > 1
	var result []types.Tool

	for serverName, conn := range p.servers {
		for _, t := range conn.tools {
			tool := t // 复制，避免后续修改影响缓存
			if multiServer {
				tool.Function.Name = serverName + "__" + t.Function.Name
			}
			result = append(result, tool)
		}
	}
	return result
}

// HasTool 判断是否包含指定工具（考虑多 server 前缀规则）。
func (p *MCPProvider) HasTool(name string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	multiServer := len(p.servers) > 1
	if multiServer {
		// 格式：serverName__toolName
		serverName, toolName, ok := splitToolName(name)
		if !ok {
			return false
		}
		conn, exists := p.servers[serverName]
		if !exists {
			return false
		}
		_, has := conn.toolSet[toolName]
		return has
	}
	// 单 server：在唯一的那个里查
	for _, conn := range p.servers {
		if _, has := conn.toolSet[name]; has {
			return true
		}
	}
	return false
}

// Dispatch 路由到对应 MCP Server 并调用工具。
func (p *MCPProvider) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	serverName, toolName, err := p.resolveRoute(name)
	if err != nil {
		return "", fmt.Errorf("MCPProvider.Dispatch: %w", err)
	}

	p.mu.RLock()
	conn, ok := p.servers[serverName]
	p.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("MCPProvider.Dispatch: server %q not attached", serverName)
	}

	// 解析 LLM 生成的参数 JSON
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("MCPProvider.Dispatch: parse args for %q: %w", name, err)
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	result, err := conn.client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("MCPProvider.Dispatch: call %q on server %q: %w", toolName, serverName, err)
	}

	return extractMCPContent(result), nil
}

// ── 内部工具方法 ───────────────────────────────────────────────────

// fetchTools 从已连接的 MCP Server 拉取工具列表，转为内部 Tool 格式。
func (p *MCPProvider) fetchTools(ctx context.Context, c *mcpclient.Client) ([]types.Tool, map[string]struct{}, error) {
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, nil, err
	}

	tools := make([]types.Tool, 0, len(resp.Tools))
	toolSet := make(map[string]struct{}, len(resp.Tools))

	for _, mt := range resp.Tools {
		// ToolInputSchema → map[string]interface{}（标准 JSON Schema）
		// 通过 JSON 序列化/反序列化完成类型转换，保留所有字段
		params, err := toolInputSchemaToMap(mt.InputSchema)
		if err != nil {
			log.Printf("[MCPProvider] fetchTools: serialize schema for %q failed: %v, using empty schema", mt.Name, err)
			params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
		}

		tools = append(tools, types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        mt.Name,
				Description: mt.Description,
				Parameters:  params,
			},
		})
		toolSet[mt.Name] = struct{}{}
	}

	return tools, toolSet, nil
}

// toolInputSchemaToMap 将 mcp.ToolInputSchema 序列化为 map[string]interface{}。
// mcp-go 的 ToolInputSchema 实现了自定义 MarshalJSON，可以直接序列化为标准 JSON Schema。
func toolInputSchemaToMap(schema mcp.ToolInputSchema) (map[string]interface{}, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveRoute 根据工具名解析出 serverName 和原始 toolName。
func (p *MCPProvider) resolveRoute(name string) (serverName, toolName string, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.servers) > 1 {
		sn, tn, ok := splitToolName(name)
		if !ok {
			return "", "", fmt.Errorf("tool %q has no server prefix (expected 'serverName__toolName')", name)
		}
		return sn, tn, nil
	}

	// 单 server：直接用唯一的那个
	for k := range p.servers {
		return k, name, nil
	}
	return "", "", fmt.Errorf("no MCP server attached")
}

// splitToolName 从 "serverName__toolName" 中解析两部分。
// 匹配第一个 "__" 子串（双下划线），允许 toolName 本身含单下划线。
func splitToolName(name string) (serverName, toolName string, ok bool) {
	idx := strings.Index(name, "__")
	if idx < 0 {
		return "", "", false
	}
	return name[:idx], name[idx+2:], true
}

// extractMCPContent 将 mcp.CallToolResult 的多种 content block 合并为字符串。
func extractMCPContent(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	// mcp.GetTextFromContent 是官方提供的 helper，可安全处理各种 content 类型
	// 对于 ImageContent / AudioContent 等非文本类型，返回其字符串表示
	var parts []string
	for _, c := range result.Content {
		text := mcp.GetTextFromContent(c)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
