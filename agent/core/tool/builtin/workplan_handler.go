package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	workplanTypes "github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/serialize"
)

type planLoadHandler struct{ tool *WorkPlanTool }

type planLoadInput = serialize.Plan
type planNodeSpec = serialize.PlanNodeSpec
type planEdgeSpec = serialize.PlanEdgeSpec

// Execute parses, validates, and atomically replaces the executable WorkPlan
// kernel. Parsing errors preserve JSON source positions or DSL paths.
func (h *planLoadHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	input, err := serialize.FromJSON(argsJSON)
	if err != nil {
		return "", fmt.Errorf("plan_load: %w", err)
	}
	compiled, err := serialize.Compile(input, h.tool.factory)
	if err != nil {
		return "", fmt.Errorf("plan_load: %w", err)
	}

	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()
	h.tool.wp = h.tool.newWorkPlanFromPlanLocked(compiled)

	return fmt.Sprintf(`{"status":"loaded","version":%d,"node_count":%d,"edge_count":%d,"entry":%q}`,
		input.Version, len(input.Nodes), len(input.Edges), input.Entry), nil
}

type planRunHandler struct{ tool *WorkPlanTool }

type planRunOutput struct {
	Status      string                      `json:"status"`
	NodeCount   int                         `json:"node_count"`
	FinalOutput string                      `json:"final_output,omitempty"`
	AbortReason string                      `json:"abort_reason,omitempty"`
	Error       string                      `json:"error,omitempty"`
	Nodes       []*workplanTypes.NodeResult `json:"nodes,omitempty"`
}

func (h *planRunHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	if h.tool.wp == nil {
		h.tool.mu.Unlock()
		return `{"status":"failed","error":"no plan loaded - call plan_load first"}`, nil
	}
	if h.tool.ProgressCallback != nil {
		h.tool.wp.NodeHook = h.tool.ProgressCallback
	}
	result, err := h.tool.wp.Run(ctx)
	h.tool.mu.Unlock()

	out := planRunOutput{Status: "completed"}
	if result != nil {
		out.NodeCount = len(result.NodeResults)
		out.FinalOutput = result.FinalOutputString()
		out.Nodes = result.NodeResults
	}
	if err != nil {
		out.Status = "failed"
		out.Error = err.Error()
		b, _ := json.Marshal(out)
		return string(b), nil
	}
	if result.Aborted {
		out.Status = "aborted"
		out.AbortReason = result.AbortReason
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

type planStatusHandler struct{ tool *WorkPlanTool }

func (h *planStatusHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()
	if h.tool.wp == nil {
		return `{"status":"empty","node_count":0,"edge_count":0}`, nil
	}
	p := h.tool.wp.Plan()
	var nodes []map[string]string
	for _, id := range p.AllNodes() {
		n := p.GetNode(id)
		kind := "unknown"
		if n != nil {
			kind = n.Kind().String()
		}
		nodes = append(nodes, map[string]string{"id": id, "kind": kind})
	}
	var edges []map[string]string
	for _, e := range p.AllEdges() {
		edges = append(edges, map[string]string{"from": e.From, "to": e.To})
	}
	out := map[string]interface{}{
		"node_count": len(nodes),
		"edge_count": len(edges),
		"entry":      p.Entry(),
		"nodes":      nodes,
		"edges":      edges,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

type planExportHandler struct{ tool *WorkPlanTool }

func (h *planExportHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()
	if h.tool.wp == nil {
		return "", fmt.Errorf("plan_export: no plan loaded")
	}
	return h.tool.wp.ExportJSON()
}

type planClearHandler struct{ tool *WorkPlanTool }

func (h *planClearHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	h.tool.wp = h.tool.newWorkPlanLocked()
	h.tool.mu.Unlock()
	return `{"status":"cleared"}`, nil
}
