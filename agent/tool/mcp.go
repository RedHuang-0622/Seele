package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	types "github.com/RedHuang-0622/Seele/types"
)

// MCPServerConfig 单个 MCP Server 的连接配置。
type MCPServerConfig struct {
	Name      string   // 唯一逻辑名
	Transport string   // "stdio" | "sse"
	Command   string   // stdio: 要启动的命令
	Args      []string // stdio: 命令参数
	Env       []string // stdio: 环境变量 "KEY=VALUE"
	URL       string   // sse: SSE 地址
}

// mcpServerConn 是单个 MCP Server 的运行时连接状态。
type mcpServerConn struct {
	cfg    MCPServerConfig
	client *mcpclient.Client
	tools  []types.Tool
}

// MCPProvider 管理多个 MCP Server 连接，实现 ToolProvider 接口。
type MCPProvider struct {
	mu      sync.RWMutex
	servers map[string]*mcpServerConn // key: MCPServerConfig.Name
}

func NewMCPProvider() *MCPProvider {
	return &MCPProvider{
		servers: make(map[string]*mcpServerConn),
	}
}

func (p *MCPProvider) ProviderName() string { return "mcp" }

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

func (p *MCPProvider) Attach(ctx context.Context, cfg MCPServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("MCPProvider.Attach: Name must not be empty")
	}

	var c *mcpclient.Client
	var err error

	switch cfg.Transport {
	case "stdio":
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
	case "sse":
		c, err = mcpclient.NewSSEMCPClient(cfg.URL)
	default:
		return fmt.Errorf("MCPProvider.Attach: unknown transport %q (use 'stdio' or 'sse')", cfg.Transport)
	}
	if err != nil {
		return fmt.Errorf("MCPProvider.Attach: create client %q: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "Seele",
		Version: "1.0.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return fmt.Errorf("MCPProvider.Attach: initialize %q: %w", cfg.Name, err)
	}

	tools, err := p.fetchTools(ctx, c)
	if err != nil {
		return fmt.Errorf("MCPProvider.Attach: fetch tools from %q: %w", cfg.Name, err)
	}

	p.mu.Lock()
	p.servers[cfg.Name] = &mcpServerConn{
		cfg:    cfg,
		client: c,
		tools:  tools,
	}
	p.mu.Unlock()

	log.Printf("[MCPProvider] attached server=%q tools=%d", cfg.Name, len(tools))
	return nil
}

func (p *MCPProvider) Detach(name string) {
	p.mu.Lock()
	if conn, ok := p.servers[name]; ok {
		_ = conn.client.Close()
		delete(p.servers, name)
	}
	p.mu.Unlock()
	log.Printf("[MCPProvider] detached server=%q", name)
}

func (p *MCPProvider) RefreshTools(ctx context.Context, serverName string) error {
	p.mu.RLock()
	conn, ok := p.servers[serverName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("MCPProvider.RefreshTools: server %q not attached", serverName)
	}

	tools, err := p.fetchTools(ctx, conn.client)
	if err != nil {
		return fmt.Errorf("MCPProvider.RefreshTools: %q: %w", serverName, err)
	}

	p.mu.Lock()
	conn.tools = tools
	p.mu.Unlock()

	log.Printf("[MCPProvider] refreshed server=%q tools=%d", serverName, len(tools))
	return nil
}

// ── ToolProvider 接口实现 ─────────────────────────────────────────

// Tools 返回所有 MCP Server 工具的 ToolEntry。
// 多 server 时自动加 "serverName__toolName" 前缀防冲突。
func (p *MCPProvider) Tools() []ToolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	multiServer := len(p.servers) > 1
	var result []ToolEntry

	for serverName, conn := range p.servers {
		for _, t := range conn.tools {
			entryName := t.Function.Name
			if multiServer {
				entryName = serverName + "__" + t.Function.Name
			}
			result = append(result, ToolEntry{
				Definition: types.Tool{
					Type: "function",
					Function: types.ToolFunction{
						Name:        entryName,
						Description: t.Function.Description,
						Parameters:  t.Function.Parameters,
					},
				},
				Handler: &MCPToolHandler{
					Client:   conn.client,
					ToolName: t.Function.Name,
				},
			})
		}
	}
	return result
}

// ── 内部工具方法 ───────────────────────────────────────────────────

func (p *MCPProvider) fetchTools(ctx context.Context, c *mcpclient.Client) ([]types.Tool, error) {
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}

	tools := make([]types.Tool, 0, len(resp.Tools))
	for _, mt := range resp.Tools {
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
	}
	return tools, nil
}

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
