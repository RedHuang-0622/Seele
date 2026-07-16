// Package agent 是 Seele 的编排层，负责组装 LLM、工具、会话三大组件。
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool/permission"
	holder "github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	hubprov "github.com/RedHuang-0622/Seele/agent/core/tool/hub"
	mcp "github.com/RedHuang-0622/Seele/agent/core/tool/mcp"
	apigw "github.com/RedHuang-0622/Seele/agent/gateway/api"
	toolgw "github.com/RedHuang-0622/Seele/agent/gateway/tool"
	"github.com/RedHuang-0622/Seele/types"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// ── Options ──────────────────────────────────────────────────────────────────

// Options 配置 Agent 的启动参数。
type Options struct {
	RegistryPath        string          // registry.yaml 路径（可选，仅使用 Hub 工具时需要）
	LLMConfig           types.LLMConfig // LLM 配置（由调用方加载后注入）
	ProviderAccountPath string          // Provider 账号 YAML 路径（可选，加载多账号配置）
	HubAddr             string          // Hub gRPC 监听地址，默认 ":0"
	HubStartupDelay     time.Duration   // 已废弃：不再使用，保留兼容
	ToolCallTimeOut     time.Duration   // 单次工具调用超时，默认 5s
	Logger              Logger
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

// Logger 是 Agent 内部使用的日志接口，与 log/slog 签名兼容。
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

type stdLogger struct{}

func (l *stdLogger) Info(msg string, args ...any)  { slog.Default().Info(msg, args...) }
func (l *stdLogger) Error(msg string, args ...any) { slog.Default().Error(msg, args...) }

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
//	  ├── pool      *api.AccountPool  ← 账号池
//	  │
//	  ├── NewSession() *seelectx.Holder
//	  ├── QuickChat()
//	  ├── Shutdown()
//	  └── ...
type Agent struct {
	llmClient   *api.ChatClient
	tools       *holder.Holder
	apiGW       apigw.Gateway
	toolGW      toolgw.Gateway
	pool        *api.AccountPool
	hub         *hubbase.BaseHub
	hubProvider *hubprov.HubProvider
	mcpProvider *mcp.Provider // 延迟初始化，mcpMu 保护
	// 延迟初始化
	mcpMu        sync.Mutex
	opts         Options
	shutdown     chan struct{}
	done         chan struct{}      // closed 表示 Shutdown() 完成
	healthCancel context.CancelFunc // 停止 health probe goroutine
	wg           sync.WaitGroup     // 追踪 in-flight 操作
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

	// 8. 所有验证通过，启动 Hub（同步等待就绪，最多 5s）
	hubReady := make(chan struct{})
	go func() {
		if err := hub.ServeAsync(opts.HubAddr, 5); err != nil {
			opts.Logger.Error("hub exited", "error", err)
		}
		close(hubReady)
	}()
	select {
	case <-hubReady:
		opts.Logger.Info("hub ready", "addr", opts.HubAddr)
	case <-time.After(5 * time.Second):
		opts.Logger.Info("hub startup timeout, continuing anyway")
	}

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
		pool:         pool,
		hub:          hub,
		hubProvider:  hubProv,
		opts:         opts,
		shutdown:     make(chan struct{}),
		done:         make(chan struct{}),
		healthCancel: healthCancel,
	}

	opts.Logger.Info("ready, hub skills loaded", "count", len(hubProv.Skills()))
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
		a.opts.Logger.Info("MCP provider initialized")
	}
	return a.mcpProvider
}

// RegisterTool 注册一个 Go 函数工具（委托给 Holder.RegisterInline）。
// outputSchema 可选，指定输出 struct 的 SchemaOf()。
func (a *Agent) RegisterTool(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error), outputSchema ...map[string]interface{}) {
	a.tools.RegisterInline(name, desc, inputSchema, handler, outputSchema...)
	a.opts.Logger.Info("inline tool registered", "name", name)
}

// ── 新增访问器 ──────────────────────────────────────────────────────────

// AccountPool 返回账号池。
func (a *Agent) AccountPool() *api.AccountPool { return a.pool }

// LLM 返回 LLM 客户端（实现 types.ChatCompleter 接口）。
func (a *Agent) LLM() types.ChatCompleter { return a.llmClient }

// VisibleTools 返回当前对 LLM 可见的工具列表。
func (a *Agent) VisibleTools(ctx context.Context) []types.Tool {
	return a.toolGW.VisibleTools(ctx)
}

// Dispatch 委托工具网关执行工具调用。在 wg 中追踪，支持 Graceful Shutdown。
func (a *Agent) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	select {
	case <-a.shutdown:
		return "", fmt.Errorf("agent: shutting down")
	default:
	}
	a.wg.Add(1)
	defer a.wg.Done()
	return a.toolGW.Dispatch(ctx, name, argsJSON)
}

// DirectDispatch 直接调度工具调用（绕过 LLM 循环）。在 wg 中追踪，支持 Graceful Shutdown。
func (a *Agent) DirectDispatch(ctx context.Context, name, argsJSON string) (string, error) {
	select {
	case <-a.shutdown:
		return "", fmt.Errorf("agent: shutting down")
	default:
	}
	a.wg.Add(1)
	defer a.wg.Done()
	return a.toolGW.Dispatch(ctx, name, argsJSON)
}

// Tools 暴露底层 Holder，供精细控制使用。
func (a *Agent) Tools() *holder.Holder { return a.tools }

func (a *Agent) ToolGateway() toolgw.Gateway { return a.toolGW }

func (a *Agent) SetPermissionConfig(cfg permission.PermissionConfig, handler permission.ApprovalHandler) {
	if gw, ok := a.toolGW.(*toolgw.DefaultGateway); ok {
		gw.SetPermissionConfig(cfg, handler)
	}
}

func (a *Agent) SetApprovalHandler(handler permission.ApprovalHandler) {
	if gw, ok := a.toolGW.(*toolgw.DefaultGateway); ok {
		gw.SetApprovalHandler(handler)
	}
}

// Shutdown 关闭 Agent，释放资源。并发安全。
// 先发送关闭信号，等待所有 in-flight 操作完成，再清理资源。
func (a *Agent) Shutdown() {
	select {
	case <-a.shutdown:
		return
	default:
		close(a.shutdown)
	}

	// 等待所有 in-flight Dispatch 完成
	a.wg.Wait()

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
	close(a.done)
	a.opts.Logger.Info("shutdown complete")
}
