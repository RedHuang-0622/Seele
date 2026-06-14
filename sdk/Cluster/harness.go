package cluster

// Package cluster 提供 Agent 进程的通用 Harness（启动框架）。
//
// Harness 负责最小化的启动流程：
//  1. 读取自包含注册表（工具地址直接内联，无需合并）
//  2. 初始化 Seele Engine
//  3. 创建 Handler 并启动 gRPC Server
//
// 应用层只需提供 WorkflowMap + HarnessConfig：
//
//	func main() {
//	    agent.Run(agent.WorkflowMap{
//	        "model_report": workflows.ModelReportWorkflow,
//	    }, agent.HarnessConfig{
//	        Name:         "model_workflow",
//	        Port:         51111,
//	        RegistryPath: "model_agent/registries/registry_workflow.yaml",
//	        LLMConfigPath: "model_agent/config.yaml",
//	    })
//	}
//
// 注册表是自包含的：services.tools + pool 直接内联，文件本身即权限边界。
// 物理隔离：每个 Agent 独立持有自己的注册表文件。
//
// 日后多 Agent 调度时，通过注册表的 subagents 字段声明可调度的子 Agent。
import (
	"fmt"
	"log"
	"time"

	seeleapi "github.com/RedHuang-0622/Seele/sdk/api"
	"github.com/RedHuang-0622/Seele/workplan"
	tool "github.com/RedHuang-0622/microHub/root_class/tool"
)

// ── HarnessConfig ────────────────────────────────────────────────

// HarnessConfig 应用层显式注入的配置，框架不读环境变量。
type HarnessConfig struct {
	// Name 是本 Agent 的唯一标识，同时作为 gRPC 服务名。
	Name string

	// Port 是 gRPC 监听端口。
	Port int

	// RegistryPath 自包含注册表路径（services.tools + pool，直接给 Engine 用）。
	RegistryPath string

	// LLMConfigPath LLM 配置文件路径。
	LLMConfigPath string

	// MaxLoops 单次 Agent.Chat 最大 ReAct 循环次数，默认 8。
	MaxLoops int

	// MaxConcurrentWorkPlans 全局最大并发 WorkPlan 数，0 表示不限。
	MaxConcurrentWorkPlans int
}

func (c *HarnessConfig) withDefaults() {
	if c.MaxLoops <= 0 {
		c.MaxLoops = 8
	}
}

// ── EngineFactory ────────────────────────────────────────────────

// EngineFactory 将 seeleapi.Engine 适配为 workplan.AgentFactory。
type EngineFactory struct {
	Engine   *seeleapi.Engine
	MaxLoops int
}

func (f *EngineFactory) NewAgent(systemPrompt string) workplan.Agent {
	return f.Engine.NewSession(systemPrompt, f.MaxLoops)
}

// ── Run ──────────────────────────────────────────────────────────

// Run 是 Harness 的唯一入口。阻塞直到进程退出。
func Run(wfMap WorkflowMap, cfg HarnessConfig) {
	cfg.withDefaults()
	workplan.SetMaxConcurrentWorkPlans(cfg.MaxConcurrentWorkPlans)

	if cfg.Name == "" {
		log.Fatal("[agent] Name is required")
	}
	if cfg.Port <= 0 {
		log.Fatal("[agent] Port is required")
	}
	if cfg.RegistryPath == "" {
		log.Fatal("[agent] RegistryPath is required")
	}

	log.Printf("[%s] 启动，端口 :%d，注册表 %s", cfg.Name, cfg.Port, cfg.RegistryPath)

	// 1. 初始化 Engine（注册表自包含，直接传入）
	engine, err := seeleapi.New(seeleapi.Options{
		RegistryPath:    cfg.RegistryPath,
		LLMConfigPath:   cfg.LLMConfigPath,
		ToolCallTimeOut: 120 * time.Second,
	})
	if err != nil {
		log.Fatalf("[%s] Engine 初始化失败: %v", cfg.Name, err)
	}
	defer engine.Shutdown()

	// 2. 构建 Handler 并启动 gRPC Server
	// [workplangate] 创建 NetworkApprovalGate 支持两段式审批
	gate := workplan.NewNetworkApprovalGate()

	factory := &EngineFactory{Engine: engine, MaxLoops: cfg.MaxLoops}
	handler := NewAgentHandler(cfg.Name, factory, wfMap)
	handler.SetApprovalGate(gate)
	defer handler.Shutdown()

	port := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("[%s] 就绪，工具数=%d", cfg.Name, len(engine.Hub().Skills()))

	if err := tool.New(handler).Serve(port); err != nil {
		log.Fatalf("[%s] Serve: %v", cfg.Name, err)
	}
}
