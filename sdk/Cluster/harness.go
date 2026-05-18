// Package agent 提供通用 Agent 进程 Harness。
//
// Harness 负责 Agent 进程的完整生命周期（框架层）：
//  1. 读取环境变量
//  2. 解析个性注册表
//  3. 裁剪合并注册表（uses.tools + peers）→ 写入临时文件
//  4. 初始化 Seele Engine（LLM + 裁剪后工具集）
//  5. 启动 microHub gRPC Server，等待 Dispatch 调用
//  6. Dispatch → WorkflowMap 路由 → 执行工作流 → 返回结果
//
// 应用层只需提供 WorkflowMap + HarnessConfig，其余全部由框架处理：
//
//	func main() {
//	    agent.Run(agent.WorkflowMap{
//	        "respond_to_alert": workflows.AlertResponseWorkflow,
//	        "diagnose":         workflows.IncidentDiagnosisWorkflow,
//	    }, &agent.HarnessConfig{MaxLoops: 50})
//	}
//
// 环境变量（框架读取，应用层覆盖默认路径）：
//
//	AGENT_ROLE        角色名（必填）
//	REGISTRY_PATH     个性注册表路径（默认由应用层设置）
//	HUB_REGISTRY      主注册表路径
//	TOOLS_REGISTRY    工具地址簿路径
//	LLM_CONFIG        LLM 配置文件路径
//	AGENT_PORT        覆盖监听端口（默认读 registry network.port）
package agent

import (
	"fmt"
	"log"
	"os"
	"time"

	tool "github.com/sukasukasuka123/microHub/root_class/tool"
	seeleapi "github.com/sukasukasuka123/Seele/sdk/api"
	"github.com/sukasukasuka123/Seele/workplan"
	"gopkg.in/yaml.v3"
)

// ── HarnessConfig ────────────────────────────────────────────────

// HarnessConfig 应用层注入的配置，覆盖框架默认行为。
// 零值字段使用框架默认值。
type HarnessConfig struct {
	// MaxLoops 单次 Agent.Chat 最大 ReAct 循环次数，默认 8。
	MaxLoops int

	// RegistryPath 个性注册表路径，空时从环境变量 REGISTRY_PATH 读取。
	RegistryPath string

	// HubRegistry 主注册表路径，空时从环境变量 HUB_REGISTRY 读取。
	HubRegistry string

	// ToolsRegistry 工具地址簿路径，空时从环境变量 TOOLS_REGISTRY 读取。
	ToolsRegistry string

	// LLMConfigPath LLM 配置文件路径，空时从环境变量 LLM_CONFIG 读取。
	LLMConfigPath string
}

func (c *HarnessConfig) withDefaults() {
	if c == nil {
		return
	}
	if c.MaxLoops <= 0 {
		c.MaxLoops = 4
	}
}

// ── EngineFactory ────────────────────────────────────────────────

// EngineFactory 将 seeleapi.Engine 适配为 workplan.AgentFactory。
type EngineFactory struct {
	Engine   *seeleapi.Engine
	MaxLoops int
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	return f.Engine.NewAgent(systemPrompt, f.MaxLoops)
}

// ── Run ──────────────────────────────────────────────────────────

// Run 是 Harness 的唯一入口。阻塞直到进程退出。
// wfMap 由应用层注入（method → workflow function）。
// cfg 为 nil 时使用框架默认值。
func Run(wfMap WorkflowMap, cfg *HarnessConfig) {
	// 0. 初始化配置默认值
	if cfg == nil {
		cfg = &HarnessConfig{}
	}
	cfg.withDefaults()

	// 1. 读环境变量（框架读取，应用层可在 cfg 中覆盖默认路径）
	role := os.Getenv("AGENT_ROLE")
	if role == "" {
		log.Fatal("[agent] AGENT_ROLE 未设置")
	}

	registryPath := firstNonEmpty(cfg.RegistryPath,
		os.Getenv("REGISTRY_PATH"),
		"ops_tools/registries/registry_"+role+".yaml")
	hubRegistry := firstNonEmpty(cfg.HubRegistry,
		os.Getenv("HUB_REGISTRY"),
		"ops_tools/registry.yaml")
	toolsRegistry := firstNonEmpty(cfg.ToolsRegistry,
		os.Getenv("TOOLS_REGISTRY"),
		"ops_tools/registry.tools.yaml")
	llmConfig := firstNonEmpty(cfg.LLMConfigPath,
		os.Getenv("LLM_CONFIG"),
		"ops_tools/config.yaml")

	// 2. 解析个性注册表
	reg := &AgentRegistry{}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		log.Fatalf("[%s] 读取注册表 %q 失败: %v", role, registryPath, err)
	}
	if err := yaml.Unmarshal(data, reg); err != nil {
		log.Fatalf("[%s] 解析 YAML %q 失败: %v", role, registryPath, err)
	}

	logRegistrySummary(role, reg)

	// 3. 裁剪合并注册表
	mergedPath, err := BuildAgentRegistry(reg, hubRegistry, toolsRegistry)
	if err != nil {
		log.Fatalf("[%s] 构建合并注册表失败: %v", role, err)
	}
	log.Printf("[%s] 合并注册表 → %s", role, mergedPath)
	defer os.Remove(mergedPath)

	// 4. 初始化 Engine
	engine, err := seeleapi.New(seeleapi.Options{
		RegistryPath:    mergedPath,
		LLMConfigPath:   llmConfig,
		ToolCallTimeOut: 120 * time.Second,
	})
	if err != nil {
		log.Fatalf("[%s] Engine 初始化失败: %v", role, err)
	}
	defer engine.Shutdown()

	// 5. 构建 Handler 并启动 gRPC Server
	factory := &EngineFactory{Engine: engine, MaxLoops: cfg.MaxLoops}
	handler := NewAgentHandler(role, reg, factory, wfMap)

	port := fmt.Sprintf(":%d", reg.Network.Port)
	if p := os.Getenv("AGENT_PORT"); p != "" {
		port = p
	}

	log.Printf("[%s] microHub 监听 %s (tools=%d)", role, port, len(engine.Skills()))
	log.Printf("[%s] === Agent 就绪 ===", role)

	if err := tool.New(handler).Serve(port); err != nil {
		log.Fatalf("[%s] Serve: %v", role, err)
	}
}

// ── 内部工具 ────────────────────────────────────────────────────

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func logRegistrySummary(role string, reg *AgentRegistry) {
	log.Printf("[%s] 注册表: %s (tier=%s)", role, reg.Role.Display, reg.Role.Tier)
	log.Printf("[%s] 能力: %d 个", role, len(reg.Provides.Capabilities))
	for _, c := range reg.Provides.Capabilities {
		log.Printf("[%s]   - %s: %s", role, c.Name, c.Description)
	}
	log.Printf("[%s] 原子工具: %v", role, reg.Uses.Tools)
	log.Printf("[%s] Peers: %d 个", role, len(reg.Peers))
	for _, p := range reg.Peers {
		log.Printf("[%s]   - %s: %v", role, p.Name, p.Capabilities)
	}
}
