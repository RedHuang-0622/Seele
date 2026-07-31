package builtin

import (
	"context"
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	"github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	workplanTypes "github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
)

// NewChatAgentFactory adapts a ChatCompleter into the factory used by DSL auto
// nodes. A nil client is useful for deterministic local tests.
func NewChatAgentFactory(client types.ChatCompleter) workplan.AgentFactory {
	return &chatAgentFactory{client: client}
}

type chatAgentFactory struct{ client types.ChatCompleter }

func (f *chatAgentFactory) NewAgent(systemPrompt string) node.Agent {
	if f.client == nil {
		return echoAgent{}
	}
	return chatAgent{client: f.client, systemPrompt: systemPrompt}
}

type chatAgent struct {
	client       types.ChatCompleter
	systemPrompt string
}

func (a chatAgent) Chat(ctx context.Context, input string) (string, error) {
	message, err := a.client.Complete(ctx, []types.Message{
		{Role: "system", Content: &a.systemPrompt},
		{Role: "user", Content: &input},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("workplan chat: %w", err)
	}
	if message.Content == nil {
		return "", nil
	}
	return *message.Content, nil
}

type echoAgent struct{}

func (echoAgent) Chat(_ context.Context, input string) (string, error) { return input, nil }

// WorkPlanTool exposes the versioned executable Seele DSL to an LLM. A load
// replaces the assembled Plan kernel atomically; Graph remains a separate
// editing facade owned by the resulting WorkPlan.
type WorkPlanTool struct {
	mu      sync.Mutex
	wp      *workplan.WorkPlan
	factory node.AgentFactory

	ProgressCallback   func(nr *workplanTypes.NodeResult)
	BranchEventHook    func(forkexec.Event)
	BranchRuntimeFor   func(string) forkexec.BranchRuntime
	ForkPolicy         forkexec.Policy
	ForkJoinPolicy     forkexec.JoinPolicy
	MaxForkConcurrency int
}

func NewWorkPlanTool(factory node.AgentFactory) *WorkPlanTool {
	return &WorkPlanTool{factory: factory}
}

func (w *WorkPlanTool) SetBranchEventHook(hook func(forkexec.Event)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.BranchEventHook = hook
}

func (w *WorkPlanTool) SetBranchRuntimeResolver(resolver func(string) forkexec.BranchRuntime) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.BranchRuntimeFor = resolver
}

func (w *WorkPlanTool) SetForkPolicy(policy forkexec.Policy) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ForkPolicy = policy
}

func (w *WorkPlanTool) SetForkJoinPolicy(policy forkexec.JoinPolicy) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ForkJoinPolicy = policy
}

func (w *WorkPlanTool) SetMaxForkConcurrency(maxConcurrent int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.MaxForkConcurrency = maxConcurrent
}

func (w *WorkPlanTool) newWorkPlanLocked() *workplan.WorkPlan {
	return w.newWorkPlanFromPlanLocked(coreplan.New())
}

func (w *WorkPlanTool) newWorkPlanFromPlanLocked(p *coreplan.Plan) *workplan.WorkPlan {
	return workplan.NewFromPlan(p, w.factory,
		workplan.WithBranchEventHook(w.BranchEventHook),
		workplan.WithBranchRuntimeResolver(w.BranchRuntimeFor),
		workplan.WithForkPolicy(w.ForkPolicy),
		workplan.WithForkJoinPolicy(w.ForkJoinPolicy),
		workplan.WithMaxForkConcurrency(w.MaxForkConcurrency),
	)
}

func (w *WorkPlanTool) ProviderName() string { return "workplan" }

func (w *WorkPlanTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		tool("plan_load",
			"Validate and atomically load a Seele WorkPlan DSL v1 document. The document must contain version 1, entry, nodes [{id,input,kind:auto}], and edges [{from,to}]. Every node is a runnable subagent task.",
			planLoadSchema(), &planLoadHandler{tool: w}),
		tool("plan_run",
			"Run the loaded Plan kernel. Independent successor nodes execute as isolated subagent branches.",
			obj(), &planRunHandler{tool: w}),
		tool("plan_status",
			"Return the loaded Plan kernel's entry, nodes, and directed edges.",
			obj(), &planStatusHandler{tool: w}),
		tool("plan_export",
			"Export the loaded Plan kernel as a versioned Seele WorkPlan DSL v1 document.",
			obj(), &planExportHandler{tool: w}),
		tool("plan_clear",
			"Discard the loaded Plan kernel and create an empty WorkPlan.",
			obj(), &planClearHandler{tool: w}),
	}
}

func planLoadSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"version": map[string]interface{}{"type": "integer", "enum": []int{1}},
			"entry":   map[string]interface{}{"type": "string"},
			"nodes": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":    map[string]interface{}{"type": "string"},
						"input": map[string]interface{}{"type": "string"},
						"kind":  map[string]interface{}{"type": "string", "enum": []string{"auto"}},
					},
					"required": []string{"id", "input", "kind"},
				},
			},
			"edges": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"from": map[string]interface{}{"type": "string"},
						"to":   map[string]interface{}{"type": "string"},
					},
					"required": []string{"from", "to"},
				},
			},
		},
		"required": []string{"version", "entry", "nodes", "edges"},
	}
}

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
	result := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	required := make([]string, 0, len(props))
	for _, prop := range props {
		for key, value := range prop {
			result["properties"].(map[string]interface{})[key] = value
			required = append(required, key)
		}
	}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}
