// sdk/api/seele_api.go
//
// # Seele API SDK
//
// Engine 是唯一知道全局架构的地方，负责：
//  1. 初始化 microHub + registry（基础设施）
//  2. 创建 HubProvider 并注册到 Runtime
//  3. 按需创建 MCPProvider 并注册（可选）
//  4. 向外暴露简洁的 Agent、MCP、Skill 管理接口
//
// 分层职责：
//
//	Engine (api.go)   ← 组装层：知道所有具体实现，负责生命周期
//	Runtime           ← 编排层：只知道 ToolProvider 接口
//	Agent             ← 对话层：完全不知道工具怎么来的
package api

import (
	"context"
	"fmt"
	"log"
	"time"

	runtime "github.com/sukasukasuka123/Seele"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	hubbase "github.com/sukasukasuka123/microHub/root_class/hub"
	registry "github.com/sukasukasuka123/microHub/service_registry"
)

// ── Options ──────────────────────────────────────────────────────

type Options struct {
	RegistryPath    string        // registry.yaml 路径（必填）
	LLMConfigPath   string        // config.yaml 路径（必填）
	HubAddr         string        // Hub gRPC 监听地址，默认 ":50051"
	HubStartupDelay time.Duration // 等待 Hub 启动时间，默认 100ms
	ToolCallTimeOut time.Duration // 单次工具调用超时，默认 5s
	Logger          Logger
}

func (o *Options) withDefaults() {
	if o.HubAddr == "" {
		o.HubAddr = ":50051"
	}
	if o.HubStartupDelay == 0 {
		o.HubStartupDelay = 100 * time.Millisecond
	}
	if o.ToolCallTimeOut <= 0 {
		o.ToolCallTimeOut = 5 * time.Second
	}
	if o.Logger == nil {
		o.Logger = &stdLogger{}
	}
}

// ── Logger 接口 ───────────────────────────────────────────────────

type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type stdLogger struct{}

func (l *stdLogger) Infof(f string, a ...interface{}) { log.Printf("[seele/api] "+f, a...) }
func (l *stdLogger) Errorf(f string, a ...interface{}) {
	log.Printf("[seele/api] ERROR "+f, a...)
}

// ── Engine ────────────────────────────────────────────────────────

// Engine 管理完整的 Seele 运行时生命周期。
// 通过 New 创建，通过 Shutdown 关闭。Engine 并发安全。
type Engine struct {
	rt          *runtime.Runtime
	hub         *hubbase.BaseHub
	hubProvider *runtime.HubProvider
	mcpProvider *runtime.MCPProvider // 延迟初始化，首次 AttachMCPServer 时创建
	opts        Options
	shutdown    chan struct{}
}

// New 初始化 Engine。
//
// 启动流程：
//  1. 加载 registry.yaml（skill 列表 + Hub 配置）
//  2. 启动本地 microHub gRPC 服务
//  3. 创建 HubProvider 并注册到 Runtime
//  4. 加载 LLM 配置，创建 Runtime
func New(opts Options) (*Engine, error) {
	opts.withDefaults()

	// 1. 加载 registry
	if err := registry.Init(opts.RegistryPath); err != nil {
		return nil, fmt.Errorf("seele/api: registry init %q: %w", opts.RegistryPath, err)
	}
	registry.ProbeAllOnStartup()
	registry.StartHealthProbe(context.Background(), 15*time.Second)

	// 2. 启动 Hub
	hub := hubbase.New(&registryRouter{})
	go func() {
		if err := hub.ServeAsync(opts.HubAddr, 5); err != nil {
			opts.Logger.Errorf("hub exited: %v", err)
		}
	}()
	time.Sleep(opts.HubStartupDelay)
	opts.Logger.Infof("hub listening on %s", opts.HubAddr)

	// 3. 加载 LLM 配置，创建 Runtime
	llmCfg, err := runtime.LoadConfig(opts.LLMConfigPath)
	if err != nil {
		return nil, fmt.Errorf("seele/api: load llm config %q: %w", opts.LLMConfigPath, err)
	}
	rt, err := runtime.NewRuntime(llmCfg)
	if err != nil {
		return nil, fmt.Errorf("seele/api: new runtime: %w", err)
	}

	// 4. 创建 HubProvider，注册到 Runtime
	hubProv, err := runtime.NewHubProvider(hub, opts.ToolCallTimeOut)
	if err != nil {
		return nil, fmt.Errorf("seele/api: new hub provider: %w", err)
	}
	rt.Register(hubProv)

	eng := &Engine{
		rt:          rt,
		hub:         hub,
		hubProvider: hubProv,
		opts:        opts,
		shutdown:    make(chan struct{}),
	}

	opts.Logger.Infof("engine ready, %d hub skill(s) loaded", len(hubProv.Skills()))
	return eng, nil
}

// Shutdown 关闭 Engine，释放资源。
func (e *Engine) Shutdown() {
	select {
	case <-e.shutdown:
	default:
		close(e.shutdown)
		if e.mcpProvider != nil {
			// 关闭所有 MCP Server 连接
			for _, info := range e.MCPServers() {
				e.mcpProvider.Detach(info)
			}
		}
		e.opts.Logger.Infof("engine shutdown")
	}
}

// ── Agent 管理 ────────────────────────────────────────────────────

// NewAgent 创建新 Agent。
func (e *Engine) NewAgent(systemPrompt string, loopTimes int) *runtime.Agent {
	return e.rt.NewAgent(systemPrompt, loopTimes)
}

// QuickChat 一次性对话，不保留历史。
func (e *Engine) QuickChat(ctx context.Context, systemPrompt, userInput string) (string, error) {
	return e.NewAgent(systemPrompt, 8).Chat(ctx, userInput)
}

// QuickChatStream 一次性流式对话，不保留历史。
func (e *Engine) QuickChatStream(ctx context.Context, systemPrompt, userInput string, onChunk func(string)) (string, error) {
	return e.NewAgent(systemPrompt, 8).ChatStream(ctx, userInput, onChunk)
}

// ── Hub Skill 管理 ────────────────────────────────────────────────

// Skills 返回 Hub 工具的摘要列表。
func (e *Engine) Skills() []runtime.SkillInfo {
	return e.hubProvider.Skills()
}

// Retire 临时屏蔽某个 Hub skill（重启后自动恢复）。
func (e *Engine) Retire(name string) { e.hubProvider.Retire(name) }

// Restore 恢复被 Retire 屏蔽的 Hub skill。
func (e *Engine) Restore(name string) { e.hubProvider.Restore(name) }

// ── MCP Server 管理 ───────────────────────────────────────────────

// AttachMCPServer 连接一个 MCP Server，工具立即对 LLM 可见。
// 可在 Engine 运行期间随时调用（热插拔）。
//
// 示例：
//
//	engine.AttachMCPServer(ctx, Seele.MCPServerConfig{
//	    Name:      "filesystem",
//	    Transport: "stdio",
//	    Command:   "npx",
//	    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
//	    Env:       []string{"LOG_LEVEL=debug"},  // 格式 "KEY=VALUE"
//	})
func (e *Engine) AttachMCPServer(ctx context.Context, cfg runtime.MCPServerConfig) error {
	e.ensureMCPProvider()
	return e.mcpProvider.Attach(ctx, cfg)
}

// DetachMCPServer 断开指定 MCP Server，工具立即不可见。
func (e *Engine) DetachMCPServer(name string) {
	if e.mcpProvider != nil {
		e.mcpProvider.Detach(name)
	}
}

// RefreshMCPTools 重新从指定 server 拉取工具列表（MCP Server 动态增减工具时使用）。
func (e *Engine) RefreshMCPTools(ctx context.Context, serverName string) error {
	if e.mcpProvider == nil {
		return fmt.Errorf("no MCP server attached")
	}
	return e.mcpProvider.RefreshTools(ctx, serverName)
}

// MCPServers 返回当前已连接的 MCP Server 名称列表。
func (e *Engine) MCPServers() []string {
	if e.mcpProvider == nil {
		return nil
	}
	// MCPProvider 未暴露 server 列表，此处通过 Tools() 推断
	// 如需完整列表，可在 MCPProvider 添加 ServerNames() 方法
	return nil // 实际使用时可扩展
}

// ── 底层访问 ──────────────────────────────────────────────────────

// Runtime 暴露底层 Runtime，供需要精细控制的场景使用。
func (e *Engine) Runtime() *runtime.Runtime { return e.rt }

// ── AgentPool ─────────────────────────────────────────────────────

type AgentPool struct {
	engine  *Engine
	agents  []*namedAgent
	current int
}

type namedAgent struct {
	label string
	agent *runtime.Agent
}

func (e *Engine) NewAgentPool() *AgentPool {
	return &AgentPool{engine: e}
}

func (p *AgentPool) Add(label, systemPrompt string) int {
	p.agents = append(p.agents, &namedAgent{label: label, agent: p.engine.NewAgent(systemPrompt, 8)})
	return len(p.agents) - 1
}

func (p *AgentPool) Switch(idx int) error {
	if idx < 0 || idx >= len(p.agents) {
		return fmt.Errorf("index %d out of range [0, %d)", idx, len(p.agents))
	}
	p.current = idx
	return nil
}

func (p *AgentPool) Current() *runtime.Agent {
	if len(p.agents) == 0 {
		return nil
	}
	return p.agents[p.current].agent
}

func (p *AgentPool) CurrentLabel() string {
	if len(p.agents) == 0 {
		return ""
	}
	return p.agents[p.current].label
}

func (p *AgentPool) CurrentIndex() int { return p.current }
func (p *AgentPool) Len() int          { return len(p.agents) }

type AgentSummary struct {
	Index     int
	Label     string
	SessionID string
	MsgCount  int
	IsCurrent bool
}

func (p *AgentPool) All() []AgentSummary {
	result := make([]AgentSummary, len(p.agents))
	for i, na := range p.agents {
		result[i] = AgentSummary{
			Index:     i,
			Label:     na.label,
			SessionID: na.agent.SessionID(),
			MsgCount:  len(na.agent.History()),
			IsCurrent: i == p.current,
		}
	}
	return result
}

func (p *AgentPool) Chat(ctx context.Context, input string) (string, error) {
	a := p.Current()
	if a == nil {
		return "", fmt.Errorf("agentpool is empty, call Add first")
	}
	return a.Chat(ctx, input)
}

func (p *AgentPool) ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error) {
	a := p.Current()
	if a == nil {
		return "", fmt.Errorf("agentpool is empty, call Add first")
	}
	return a.ChatStream(ctx, input, onChunk)
}

// ── 内部工具 ──────────────────────────────────────────────────────

// ensureMCPProvider 延迟初始化 MCPProvider 并注册到 Runtime。
// 首次调用 AttachMCPServer 时触发，避免未使用 MCP 时的额外开销。
func (e *Engine) ensureMCPProvider() {
	if e.mcpProvider != nil {
		return
	}
	e.mcpProvider = runtime.NewMCPProvider()
	e.rt.Register(e.mcpProvider)
	e.opts.Logger.Infof("MCP provider initialized")
}

// ── Hub 路由（SDK 内部）────────────────────────────────────────────

type registryRouter struct{}

func (r *registryRouter) ServiceName() string { return "seele-sdk-hub" }

func (r *registryRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	if req == nil {
		return nil, nil
	}
	for _, t := range registry.GetOnlineTools() {
		if t.Method == req.Method {
			return []hubbase.DispatchTarget{
				{Addr: t.Addr, Request: req, Stream: true},
			}, nil
		}
	}
	return nil, fmt.Errorf("no online tool for method=%q", req.Method)
}

func (r *registryRouter) OnResults(results []hubbase.DispatchResult) {
	for _, ri := range results {
		if ri.Err != nil {
			log.Printf("[%s] addr=%s connection error, marking offline: %v",
				r.ServiceName(), ri.Target.Addr, ri.Err)
			registry.MarkOffline(ri.Target.Addr)
			continue
		}
		for _, resp := range ri.Responses {
			if resp.Status != "ok" && resp.Status != "partial" {
				log.Printf("[%s] tool=%s business error: %s", r.ServiceName(), resp.ToolName, resp.Status)
			}
		}
	}
}

func (r *registryRouter) Addrs() []string {
	tools := registry.GetOnlineTools()
	addrs := make([]string, 0, len(tools))
	for _, t := range tools {
		addrs = append(addrs, t.Addr)
	}
	return addrs
}
