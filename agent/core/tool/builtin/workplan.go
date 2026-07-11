package builtin

import (
	"context"
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	"github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
)

// ── AgentFactory 适配 ───────────────────────────────────────────────────

// chatAgentFactory 将 types.ChatCompleter 适配为 workplan.AgentFactory。
type chatAgentFactory struct{ client types.ChatCompleter }

// NewChatAgentFactory 创建基于 ChatCompleter 的 AgentFactory。
// 传入 nil 时 NewAgent 返回回显 fallback（不调用 LLM）。
func NewChatAgentFactory(client types.ChatCompleter) workplan.AgentFactory {
	return &chatAgentFactory{client: client}
}

func (f *chatAgentFactory) NewAgent(systemPrompt string) node.Agent {
	if f.client == nil {
		return &echoAgent{}
	}
	return &chatAgent{client: f.client, systemPrompt: systemPrompt}
}

type chatAgent struct {
	client       types.ChatCompleter
	systemPrompt string
}

func (a *chatAgent) Chat(ctx context.Context, input string) (string, error) {
	msg, err := a.client.Complete(ctx, []types.Message{
		{Role: "system", Content: &a.systemPrompt},
		{Role: "user", Content: &input},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("workplan chat: %w", err)
	}
	if msg.Content != nil {
		return *msg.Content, nil
	}
	return "", nil
}

type echoAgent struct{}

func (*echoAgent) Chat(_ context.Context, input string) (string, error) { return input, nil }

// ── WorkPlanTool ─────────────────────────────────────────────────────────

// WorkPlanTool 将 WorkPlan 引擎包装为 ToolProvider。
// LLM 通过 plan_load 用邻接表 JSON 定义 DAG，再 plan_run 执行。
//
// 邻接表格式示例（plan_load 的参数）：
//
//	{
//	  "entry": "start",
//	  "nodes": {
//	    "start":    {"input": "分析需求"},
//	    "design":   {"input": "设计方案: {{.PrevResult}}"},
//	    "code":     {"input": "实现编码: {{.PrevResult}}"}
//	  },
//	  "edges": {
//	    "start":  ["design"],
//	    "design": ["code"]
//	  }
//	}
type WorkPlanTool struct {
	mu      sync.Mutex
	wp      *workplan.WorkPlan
	factory node.AgentFactory
}

// NewWorkPlanTool 创建 WorkPlan 工具。factory 用于子 Agent 创建。
func NewWorkPlanTool(factory node.AgentFactory) *WorkPlanTool {
	return &WorkPlanTool{factory: factory}
}

func (w *WorkPlanTool) ProviderName() string { return "workplan" }

func (w *WorkPlanTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		tool("plan_load",
			`定义或替换完整的 WorkPlan DAG 工作流。接受 JSON 格式的邻接表描述，包含入口节点、节点定义和有向边。`+
				`每次调用 plan_load 原子替换当前整个工作流。`+
				`格式: {entry, nodes: {id: {input}}, edges: {from: [to, ...]}}`,
			obj(
				prop("entry", "string", "入口节点 ID，必须存在于 nodes 中"),
				prop("nodes", "object", "节点定义：{节点ID: {input: 提示词}}，支持 {{.PrevResult}} 引用上游输出"),
				prop("edges", "object", "邻接表：{节点ID: [目标节点ID, ...]}，定义有向边"),
			),
			&planLoadHandler{tool: w}),

		tool("plan_run",
			"执行当前 WorkPlan，从入口节点按图拓扑顺序执行所有节点，返回每个节点的执行结果。",
			obj(), &planRunHandler{tool: w}),

		tool("plan_status",
			"查看 WorkPlan 状态：节点/边数量、入口节点、完整拓扑列表。",
			obj(), &planStatusHandler{tool: w}),

		tool("plan_export",
			"将当前 WorkPlan 导出为 JSON 格式的 Plan 描述（节点列表 + 边列表）。",
			obj(), &planExportHandler{tool: w}),

		tool("plan_clear",
			"清除当前 WorkPlan 所有节点和边，重置为空。",
			obj(), &planClearHandler{tool: w}),
	}
}

// ── 工具定义辅助 ─────────────────────────────────────────────────────────

func tool(name, desc string, params map[string]interface{}, h interfaces.ToolHandler) interfaces.ToolEntry {
	return interfaces.ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		},
		Handler: h,
	}
}

func obj(props ...map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	var required []string
	for _, p := range props {
		for k, v := range p {
			propsMap := m["properties"].(map[string]interface{})
			propsMap[k] = v
			required = append(required, k)
		}
	}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func prop(name, typ, desc string) map[string]interface{} {
	return map[string]interface{}{
		name: map[string]interface{}{
			"type":        typ,
			"description": desc,
		},
	}
}
