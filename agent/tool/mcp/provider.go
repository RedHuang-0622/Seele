// Package mcp 提供 MCP (Model Context Protocol) 工具提供者。
//
// 通过 stdio/SSE 连接 MCP Server，将外部工具暴露为 agent/tool.ToolProvider。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/RedHuang-0622/Seele/agent/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// ServerConfig 单个 MCP Server 的连接配置。
type ServerConfig struct {
	Name      string   // 唯一逻辑名
	Transport string   // "stdio" | "sse"
	Command   string   // stdio: 要启动的命令
	Args      []string // stdio: 命令参数
	Env       []string // stdio: 环境变量 "KEY=VALUE"
	URL       string   // sse: SSE 地址
}

type serverConn struct {
	cfg    ServerConfig
	client *mcpclient.Client
	tools  []types.Tool
}

// Provider 管理多个 MCP Server 连接，实现 tool.ToolProvider 接口。
type Provider struct {
	mu      sync.RWMutex
	servers map[string]*serverConn
}

func NewProvider() *Provider {
	return &Provider{
		servers: make(map[string]*serverConn),
	}
}

func (p *Provider) ProviderName() string { return "mcp" }

func (p *Provider) ServerNames() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.servers))
	for name := range p.servers {
		names = append(names, name)
	}
	return names
}

// Attach 连接并初始化一个 MCP Server。
func (p *Provider) Attach(ctx context.Context, cfg ServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("mcp.Attach: Name must not be empty")
	}

	var c *mcpclient.Client
	var err error

	switch cfg.Transport {
	case "stdio":
		c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
	case "sse":
		c, err = mcpclient.NewSSEMCPClient(cfg.URL)
	default:
		return fmt.Errorf("mcp.Attach: unknown transport %q (use 'stdio' or 'sse')", cfg.Transport)
	}
	if err != nil {
		return fmt.Errorf("mcp.Attach: create client %q: %w", cfg.Name, err)
	}

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "Seele",
		Version: "1.0.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return fmt.Errorf("mcp.Attach: initialize %q: %w", cfg.Name, err)
	}

	tools, err := p.fetchTools(ctx, c)
	if err != nil {
		return fmt.Errorf("mcp.Attach: fetch tools from %q: %w", cfg.Name, err)
	}

	p.mu.Lock()
	p.servers[cfg.Name] = &serverConn{
		cfg:    cfg,
		client: c,
		tools:  tools,
	}
	p.mu.Unlock()

	log.Printf("[mcp] attached server=%q tools=%d", cfg.Name, len(tools))
	return nil
}

// Detach 断开一个 MCP Server。
func (p *Provider) Detach(name string) {
	p.mu.Lock()
	if conn, ok := p.servers[name]; ok {
		_ = conn.client.Close()
		delete(p.servers, name)
	}
	p.mu.Unlock()
	log.Printf("[mcp] detached server=%q", name)
}

// RefreshTools 重新拉取指定 MCP Server 的工具列表。
func (p *Provider) RefreshTools(ctx context.Context, serverName string) error {
	p.mu.RLock()
	conn, ok := p.servers[serverName]
	p.mu.RUnlock()
	if !ok {
		return fmt.Errorf("mcp.RefreshTools: server %q not attached", serverName)
	}

	tools, err := p.fetchTools(ctx, conn.client)
	if err != nil {
		return fmt.Errorf("mcp.RefreshTools: %q: %w", serverName, err)
	}

	p.mu.Lock()
	conn.tools = tools
	p.mu.Unlock()

	log.Printf("[mcp] refreshed server=%q tools=%d", serverName, len(tools))
	return nil
}

// Tools 返回所有 MCP Server 工具的 ToolEntry。
func (p *Provider) Tools() []interfaces.ToolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	multiServer := len(p.servers) > 1
	var result []interfaces.ToolEntry

	for serverName, conn := range p.servers {
		for _, t := range conn.tools {
			entryName := t.Function.Name
			if multiServer {
				entryName = serverName + "__" + t.Function.Name
			}
			result = append(result, interfaces.ToolEntry{
				Definition: types.Tool{
					Type: "function",
					Function: types.ToolFunction{
						Name:        entryName,
						Description: t.Function.Description,
						Parameters:  t.Function.Parameters,
					},
				},
				Handler: &Handler{
					Client:   conn.client,
					ToolName: t.Function.Name,
				},
			})
		}
	}
	return result
}

func (p *Provider) fetchTools(ctx context.Context, c *mcpclient.Client) ([]types.Tool, error) {
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}

	tools := make([]types.Tool, 0, len(resp.Tools))
	for _, mt := range resp.Tools {
		params, err := marshalSchema(mt.InputSchema)
		if err != nil {
			log.Printf("[mcp] fetchTools: serialize schema for %q failed: %v, using empty", mt.Name, err)
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

func marshalSchema(schema mcp.ToolInputSchema) (map[string]interface{}, error) {
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
