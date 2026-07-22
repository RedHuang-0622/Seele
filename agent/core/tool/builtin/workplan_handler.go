package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	sauto "github.com/RedHuang-0622/Seele/workplan/sugar/auto"
)

// ── plan_load ───────────────────────────────────────────────────────────
//
// 从 JSON 邻接表定义整个 DAG，原子替换当前工作流。
//
// 输入格式：
//
//	{
//	  "entry": "start",
//	  "nodes": {
//	    "start":  {"input": "第一步"},
//	    "step2":  {"input": "第二步: {{.PrevResult}}"}
//	  },
//	  "edges": {
//	    "start": ["step2"]
//	  }
//	}

type planLoadHandler struct{ tool *WorkPlanTool }

type planLoadInput struct {
	Entry string                  `json:"entry"`
	Nodes map[string]planNodeSpec `json:"nodes"`
	Edges map[string][]string     `json:"edges"`
}

type planNodeSpec struct {
	Input string `json:"input"`
}

func (h *planLoadHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input planLoadInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("plan_load: 参数解析失败: %w", err)
	}
	if input.Entry == "" {
		return "", fmt.Errorf("plan_load: entry 不能为空")
	}
	if len(input.Nodes) == 0 {
		return "", fmt.Errorf("plan_load: nodes 不能为空")
	}

	// 验证：entry 必须存在于 nodes 中
	if _, ok := input.Nodes[input.Entry]; !ok {
		return "", fmt.Errorf("plan_load: entry %q 不在 nodes 中", input.Entry)
	}

	// 验证：所有 edge 引用的节点必须存在
	for from, targets := range input.Edges {
		if _, ok := input.Nodes[from]; !ok {
			return "", fmt.Errorf("plan_load: edge 起点 %q 不在 nodes 中", from)
		}
		for _, to := range targets {
			if _, ok := input.Nodes[to]; !ok {
				return "", fmt.Errorf("plan_load: edge 终点 %q 不在 nodes 中", to)
			}
		}
	}

	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()

	// 构建新图
	wp := workplan.New(h.tool.factory)
	g := wp.Graph()

	// 1. 添加所有节点
	for id, spec := range input.Nodes {
		sauto.Add(g, id, spec.Input, h.tool.factory)
	}

	// 2. 设置入口
	g.SetEntry(input.Entry)

	// 3. 添加所有边
	for from, targets := range input.Edges {
		for _, to := range targets {
			g.AddEdge(edge.Edge{From: from, To: to})
		}
	}

	h.tool.wp = wp

	return fmt.Sprintf(`{"status":"loaded","node_count":%d,"edge_count":%d,"entry":"%s"}`,
		len(input.Nodes), countEdges(input.Edges), input.Entry), nil
}

func countEdges(e map[string][]string) int {
	n := 0
	for _, v := range e {
		n += len(v)
	}
	return n
}

// ── plan_run ────────────────────────────────────────────────────────────

type planRunHandler struct{ tool *WorkPlanTool }

func (h *planRunHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	if h.tool.ProgressCallback != nil {
		h.tool.wp.NodeHook = h.tool.ProgressCallback
	}
	result, err := h.tool.wp.Run(ctx)
	h.tool.mu.Unlock()

	if err != nil {
		return fmt.Sprintf(`{"status":"failed","error":"%s"}`, err.Error()), nil
	}
	out := map[string]interface{}{
		"status":       "completed",
		"node_count":   len(result.NodeResults),
		"final_output": result.FinalOutputString(),
	}
	if result.Aborted {
		out["status"] = "aborted"
		out["abort_reason"] = result.AbortReason
	}
	if len(result.NodeResults) > 0 {
		nodes := make([]map[string]interface{}, 0, len(result.NodeResults))
		for _, nr := range result.NodeResults {
			nodeStatus := "completed"
			if nr.Aborted {
				nodeStatus = "aborted"
			} else if nr.Err != nil {
				nodeStatus = "failed"
			} else if nr.Skipped {
				nodeStatus = "skipped"
			}
			elapsed := nr.EndedAt.Sub(nr.StartedAt).String()
			nodes = append(nodes, map[string]interface{}{
				"node_id": nr.NodeID,
				"kind":    nr.Kind,
				"status":  nodeStatus,
				"elapsed": elapsed,
				"skipped": nr.Skipped,
				"aborted": nr.Aborted,
			})
		}
		out["nodes"] = nodes
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// ── plan_status ─────────────────────────────────────────────────────────

type planStatusHandler struct{ tool *WorkPlanTool }

func (h *planStatusHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()

	g := h.tool.wp.Graph()
	var ni []map[string]string
	for _, id := range g.AllNodes() {
		n := g.GetNode(id)
		kind := "unknown"
		if n != nil {
			kind = n.Kind().String()
		}
		ni = append(ni, map[string]string{"id": id, "kind": kind})
	}
	var ei []map[string]string
	for _, e := range g.AllEdges() {
		ei = append(ei, map[string]string{"from": e.From, "to": e.To})
	}
	out := map[string]interface{}{
		"node_count": len(ni),
		"edge_count": len(ei),
		"entry_node": g.Entry(),
		"nodes":      ni,
		"edges":      ei,
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

// ── plan_export ─────────────────────────────────────────────────────────

type planExportHandler struct{ tool *WorkPlanTool }

func (h *planExportHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	defer h.tool.mu.Unlock()
	return h.tool.wp.ExportJSON()
}

// ── plan_clear ──────────────────────────────────────────────────────────

type planClearHandler struct{ tool *WorkPlanTool }

func (h *planClearHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	h.tool.mu.Lock()
	h.tool.wp = workplan.New(h.tool.factory)
	h.tool.mu.Unlock()
	return `{"status":"cleared"}`, nil
}
