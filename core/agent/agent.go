// Package agent 是 Seele 的编排层，负责组装 LLM、工具、会话三大组件。
//
// 架构：
//
//	Agent (编排层，本包)
//	  ├── llm  *llm.ChatClient         ← 持有 LLM 客户端，创建时传入，所有 session 共享
//	  ├── tools *tool_holder.Holder   ← 持有工具注册中心
//	  │
//	  ├── NewSession() *session.Holder ← 创建对话会话
//	  ├── QuickChat()                  ← 一次性对话，不留历史
//	  └── Shutdown()                   ← 生命周期管理
//
// 命名约定（避免"Agent"一词包揽三层含义）：
//
//	core/agent/Agent       ← 编排层，真正意义上的"AI Agent"
//	core/session/Holder    ← 单次对话会话的持有者
//	core/tool_holder/Holder ← 工具提供者的注册中心
package agent

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/core/tool_holder"
	"github.com/RedHuang-0622/Seele/llm"
	"github.com/RedHuang-0622/Seele/provider"
	types "github.com/RedHuang-0622/Seele/types"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// ── Options ──────────────────────────────────────────────────────────

// Options 配置 Agent 的启动参数。
type Options struct {
	RegistryPath    string          // registry.yaml 路径（可选，仅使用 Hub 工具时需要）
	LLMConfig       types.LLMConfig // LLM 配置（由调用方加载后注入，不在此包内读取文件）
	HubAddr         string          // Hub gRPC 监听地址，默认 ":0"
	HubStartupDelay time.Duration   // 等待 Hub 启动时间，默认 100ms
	ToolCallTimeOut time.Duration   // 单次工具调用超时，默认 5s
	Logger          Logger
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

// ── Logger ───────────────────────────────────────────────────────────

// Logger 是 Agent 内部使用的日志接口。
type Logger interface {
	Infof(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type stdLogger struct{}

func (l *stdLogger) Infof(f string, a ...interface{})  { log.Printf("[agent] "+f, a...) }
func (l *stdLogger) Errorf(f string, a ...interface{}) { log.Printf("[agent] ERROR "+f, a...) }

// ── Agent ────────────────────────────────────────────────────────────

// Agent 是 Seele 的核心编排器。
//
// 创建方式：
//
//	llmCfg, _ := config.LoadConfig("./config.yaml")
//	a, err := agent.New(agent.Options{
//	    RegistryPath: "./registry.yaml",
//	    LLMConfig:    llmCfg,   // 由调用方加载后注入，框架不读文件
//	})
//	defer a.Shutdown()
//	sess := a.NewSession("you are helpful", 8)
//	reply, _ := sess.Chat(ctx, "hello")
type Agent struct {
	llm            *llm.ChatClient
	tools          *tool_holder.Holder
	hub            *hubbase.BaseHub
	hubProvider    *provider.HubProvider
	mcpProvider    *provider.MCPProvider    // 延迟初始化，mcpMu 保护
	inlineProvider *provider.InlineProvider // 延迟初始化，首次 RegisterInlineTool 时创建
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
//  3. 使用调用方注入的 LLMConfig 创建 ChatClient
//  4. 创建 tool_holder，注册 HubProvider
//
// 启动流程已重新排序（修复 B11）：配置验证前置，hub 在验证通过后才启动，
// 避免配置加载失败时 hub goroutine 泄漏。
func New(opts Options) (*Agent, error) {
	opts.withDefaults()

	// 1. 加载 registry（可选——仅使用 Hub 工具时需要）
	if opts.RegistryPath != "" {
		if err := registry.Init(opts.RegistryPath); err != nil {
			return nil, fmt.Errorf("agent: registry init %q: %w", opts.RegistryPath, err)
		}
		registry.ProbeAllOnStartup()
	}

	// 2. LLM 配置由调用方注入（已加载），直接创建客户端
	llmClient := llm.NewChatClient(opts.LLMConfig)

	// 3. 创建 tool_holder + HubProvider（构造失败不启动 hub）
	tools := tool_holder.New()
	hub := hubbase.New(provider.NewHubRouter())
	hubProv, err := provider.NewHubProvider(hub, opts.ToolCallTimeOut)
	if err != nil {
		return nil, fmt.Errorf("agent: new hub provider: %w", err)
	}
	tools.Register(hubProv)

	// 4. 所有验证通过，启动 Hub
	go func() {
		if err := hub.ServeAsync(opts.HubAddr, 5); err != nil {
			opts.Logger.Errorf("hub exited: %v", err)
		}
	}()
	time.Sleep(opts.HubStartupDelay)
	opts.Logger.Infof("hub listening on %s", opts.HubAddr)

	var healthCancel context.CancelFunc
	if opts.RegistryPath != "" {
		healthCtx, cancel := context.WithCancel(context.Background())
		registry.StartHealthProbe(healthCtx, 15*time.Second)
		healthCancel = cancel
	}

	a := &Agent{
		llm:          llmClient,
		tools:        tools,
		hub:          hub,
		hubProvider:  hubProv,
		opts:         opts,
		shutdown:     make(chan struct{}),
		healthCancel: healthCancel,
	}

	opts.Logger.Infof("ready, %d hub skill(s) loaded", len(hubProv.Skills()))
	return a, nil
}

// ── Provider 访问 ──────────────────────────────────────────────────

// Hub 返回 HubProvider，提供 Skills / Retire / Restore 管理。
func (a *Agent) Hub() *provider.HubProvider { return a.hubProvider }

// MCP 返回 MCPProvider（延迟初始化），提供 MCP Server 的 Attach / Detach / Refresh。
// 首次调用时自动创建并注册到 tool_holder。若 Shutdown 已开始则返回 nil。
// 并发安全：mcpMu 保护 mcpProvider 的读写，与 Shutdown 互斥。
func (a *Agent) MCP() *provider.MCPProvider {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()

	if a.mcpProvider == nil {
		select {
		case <-a.shutdown:
			return nil // Shutdown 正在进行，不再创建
		default:
		}
		a.mcpProvider = provider.NewMCPProvider()
		a.tools.Register(a.mcpProvider)
		a.opts.Logger.Infof("MCP provider initialized")
	}
	return a.mcpProvider
}

// RegisterInlineTool 注册一个 Go 函数工具。首次调用时自动创建 InlineProvider。
// 预期在启动阶段调用（单 goroutine），运行期动态注册需自备同步。
func (a *Agent) RegisterInlineTool(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
	if a.inlineProvider == nil {
		a.inlineProvider = provider.NewInlineProvider()
		a.tools.Register(a.inlineProvider)
		a.opts.Logger.Infof("Inline provider initialized")
	}
	a.inlineProvider.Register(name, desc, inputSchema, handler)
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
