// Package agent 是 Seele 的编排层，负责组装 LLM、工具、会话三大组件。
package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/agent/api"
	apigw "github.com/RedHuang-0622/Seele/agent/gateway/api"
	toolgw "github.com/RedHuang-0622/Seele/agent/gateway/tool"
	holder "github.com/RedHuang-0622/Seele/agent/tool/holder"
	hubprov "github.com/RedHuang-0622/Seele/agent/tool/hub"
	mcp "github.com/RedHuang-0622/Seele/agent/tool/mcp"
	"github.com/RedHuang-0622/Seele/types"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// ── Options ──────────────────────────────────────────────────────────────────

// Options 配置 Agent 的启动参数。
type Options struct {
	RegistryPath       string          // registry.yaml 路径（可选，仅使用 Hub 工具时需要）
	LLMConfig          types.LLMConfig // LLM 配置（由调用方加载后注入）
	ProviderAccountPath string         // Provider 账号 YAML 路径（可选，加载多账号配置）
	HubAddr            string          // Hub gRPC 监听地址，默认 ":0"
	HubStartupDelay    time.Duration   // 等待 Hub 启动时间，默认 100ms
	ToolCallTimeOut    time.Duration   // 单次工具调用超时，默认 5s
	Logger             Logger
}

func (o *Options) withDefaults() {
	if o.HubAddr == "" {
		o.HubAddr = ":0"
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

// ── Logger ───────────────────────────────────────────────────────────────────

// Logger 是 Agent 内部使用的日志接口。
type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type stdLogger struct{}

func (l *stdLogger) Infof(f string, a ...interface{})  { log.Printf("[agent] "+f, a...) }
func (l *stdLogger) Errorf(f string, a ...interface{}) { log.Printf("[agent] ERROR "+f, a...) }

// ── Agent ────────────────────────────────────────────────────────────────────

// Agent 是 Seele 的核心编排器。
//
// 架构：
//
//	Agent
//	  ├── llmClient *api.ChatClient   ← 持有 LLM 客户端，所有 session 共享
//	  ├── tools     *holder.Holder      ← 持有工具注册中心
//	  ├── apiGW     apigw.Gateway     ← API 账号网关
//	  ├── toolGW    toolgw.Gateway    ← 工具网关（含插件过滤）
//	  │
//	  ├── NewSession() *seelectx.Holder
//	  ├── QuickChat()
//	  └── Shutdown()
type Agent struct {
	llmClient      *api.ChatClient
	tools          *holder.Holder
	apiGW          apigw.Gateway
	toolGW         toolgw.Gateway
	hub            *hubbase.BaseHub
	hubProvider    *hubprov.HubProvider
	mcpProvider    *mcp.Provider    // 延迟初始化，mcpMu 保护
	// 延迟初始化
	mcpMu          sync.Mutex
	opts           Options
	shutdown       chan struct{}
	healthCancel   context.CancelFunc // 停止 health probe goroutine
}

// New 创建 Agent 并完成初始化。
//
// 启动流程：
//  1. 加载 registry.yaml（skill 列表 + Hub 配置）
//  2. 启动本地 microHub gRPC 服务
//  3. 创建 AccountPool（单账号或从 YAML 加载）
//  4. 创建 API / Tool 网关
//  5. 创建 api.ChatClient（已连接账号池）
//  6. 创建 HubProvider 并注册到 holder.Holder
func New(opts Options) (*Agent, error) {
	opts.withDefaults()

	// 1. 加载 registry（可选——仅使用 Hub 工具时需要）
	if opts.RegistryPath != "" {
		if err := registry.Init(opts.RegistryPath); err != nil {
			return nil, fmt.Errorf("agent: registry init %q: %w", opts.RegistryPath, err)
		}
		registry.ProbeAllOnStartup()
	}

	// 2. 创建 AccountPool
	//    从 ProviderAccountPath YAML 加载多账号，否则从 LLMConfig 构建单账号
	var pool *api.AccountPool
	if opts.ProviderAccountPath != "" {
		var err error
		pool, err = api.LoadAccountsConfig(opts.ProviderAccountPath)
		if err != nil {
			return nil, fmt.Errorf("agent: load accounts from %q: %w", opts.ProviderAccountPath, err)
		}
	} else {
		pool = api.NewAccountPool(&api.Account{
			Name:     "default",
			Provider: api.ProviderOpenAI,
			BaseURL:  opts.LLMConfig.BaseURL,
			APIKey:   opts.LLMConfig.APIKey,
			Model:    opts.LLMConfig.Model,
			Priority: 1,
		})
	}

	// 3. 创建 API 网关
	apiGW := apigw.NewDefaultGateway(pool)

	// 4. 创建 tool holder
	tools := holder.New()

	// 5. 创建 tool 网关
	toolGW := toolgw.NewDefaultGateway(tools)

	// 6. 创建 LLM 客户端（连接账号池，支持 round-robin）
	llmClient := api.NewChatClient(opts.LLMConfig).WithAccountPool(pool)

	// 7. Hub + HubProvider（构造失败不启动 hub）
	hub := hubbase.New(hubprov.NewHubRouter())
	hubProv, err := hubprov.NewHubProvider(hub, opts.ToolCallTimeOut)
	if err != nil {
		return nil, fmt.Errorf("agent: new hub provider: %w", err)
	}
	tools.Register(hubProv)

	// 8. 所有验证通过，启动 Hub
	go func() {
		if err := hub.ServeAsync(opts.HubAddr, 5); err != nil {
			opts.Logger.Errorf("hub exited: %v", err)
		}
	}()
	time.Sleep(opts.HubStartupDelay)
	opts.Logger.Infof("hub listening on %s", opts.HubAddr)

	// 9. Health probe
	var healthCancel context.CancelFunc
	if opts.RegistryPath != "" {
		healthCtx, cancel := context.WithCancel(context.Background())
		registry.StartHealthProbe(healthCtx, 15*time.Second)
		healthCancel = cancel
	}

	a := &Agent{
		llmClient:    llmClient,
		tools:        tools,
		apiGW:        apiGW,
		toolGW:       toolGW,
		hub:          hub,
		hubProvider:  hubProv,
		opts:         opts,
		shutdown:     make(chan struct{}),
		healthCancel: healthCancel,
	}

	opts.Logger.Infof("ready, %d hub skill(s) loaded", len(hubProv.Skills()))
	return a, nil
}

// ── Provider 访问 ────────────────────────────────────────────────────────────

// Hub 返回 HubProvider，提供 Skills / Retire / Restore 管理。
func (a *Agent) Hub() *hubprov.HubProvider { return a.hubProvider }

// MCP 返回 MCPProvider（延迟初始化），提供 MCP Server 的 Attach / Detach / Refresh。
// 首次调用时自动创建并注册到 tool_holder。若 Shutdown 已开始则返回 nil。
func (a *Agent) MCP() *mcp.Provider {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	if a.mcpProvider == nil {
		select {
		case <-a.shutdown:
			return nil
		default:
		}
		a.mcpProvider = mcp.NewProvider()
		a.tools.Register(a.mcpProvider)
		a.opts.Logger.Infof("MCP provider initialized")
	}
	return a.mcpProvider
}

// RegisterInlineTool 注册一个 Go 函数工具（委托给 Holder.RegisterInline）。
func (a *Agent) RegisterInlineTool(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
	a.tools.RegisterInline(name, desc, inputSchema, handler)
	a.opts.Logger.Infof("inline tool registered: %s", name)
}

// Shutdown 关闭 Agent，释放资源。并发安全。
func (a *Agent) Shutdown() {
	select {
	case <-a.shutdown:
		return
	default:
		close(a.shutdown)
	}
	if a.healthCancel != nil {
		a.healthCancel()
	}
	a.mcpMu.Lock()
	if a.mcpProvider != nil {
		for _, name := range a.mcpProvider.ServerNames() {
			a.mcpProvider.Detach(name)
		}
	}
	a.mcpMu.Unlock()
	a.opts.Logger.Infof("shutdown")
}
