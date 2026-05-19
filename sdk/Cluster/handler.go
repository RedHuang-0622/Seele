package cluster

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/sukasukasuka123/Seele/workplan"
	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
)

// WorkflowFunc 是单个工作流的函数签名。
// userInput 是调用方传入的自然语言查询（从 gRPC ToolRequest.Params 中提取）。
type WorkflowFunc func(workplan.AgentFactory, string) (*workplan.WorkPlanResult, error)

// WorkflowMap method 名 → 工作流函数，由应用层注入，框架层不感知业务。
type WorkflowMap map[string]WorkflowFunc

// AgentHandler 实现 microHub tool.Handler 接口，
// 将 gRPC Dispatch 请求路由到对应工作流。
type AgentHandler struct {
	role     string
	registry *AgentRegistry
	factory  workplan.AgentFactory
	wfMap    WorkflowMap
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(role string, reg *AgentRegistry, factory workplan.AgentFactory, wfMap WorkflowMap) *AgentHandler {
	return &AgentHandler{
		role:     role,
		registry: reg,
		factory:  factory,
		wfMap:    wfMap,
	}
}

func (h *AgentHandler) ServiceName() string { return h.role }

func (h *AgentHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	ch := make(chan *pb.ToolResponse, 1)

	go func() {
		defer close(ch)

		wf, ok := h.wfMap[req.Method]
		if !ok {
			ch <- errorResp(h.role, req.TaskId, "UNKNOWN_METHOD",
				fmt.Sprintf("method=%q not found for agent=%q", req.Method, h.role))
			return
		}

		userInput := extractUserInput(req.Params)
		log.Printf("[%s] Dispatch method=%s", h.role, req.Method)
		result, err := wf(h.factory, userInput)
		if err != nil {
			ch <- errorResp(h.role, req.TaskId, "WORKFLOW_ERROR", err.Error())
			return
		}

		resp := map[string]interface{}{
			"output":         result.FinalOutputString(),
			"total_elapsed":  result.TotalElapsed.String(),
			"nodes_executed": len(result.NodeResults),
			"aborted":        result.Aborted,
		}
		respBytes, _ := json.Marshal(resp)

		r, err := pb_api.OKResp(h.role, req.TaskId, json.RawMessage(respBytes))
		if err != nil {
			ch <- errorResp(h.role, req.TaskId, "BUILD_RESP", err.Error())
			return
		}
		ch <- r
	}()

	return ch, nil
}

func errorResp(toolName, taskID, code, msg string) *pb.ToolResponse {
	return pb_api.ErrorResp(toolName, taskID, code, msg, "")
}

// extractUserInput 从 ToolRequest.Params 中提取用户自然语言查询。
// Params 预期是 JSON 对象，优先取 query / prompt / input 字段；
// 若都为空，返回整个 Params 字符串。
func extractUserInput(params []byte) string {
	if len(params) == 0 {
		return ""
	}
	var m map[string]interface{}
	if json.Unmarshal(params, &m) != nil {
		return string(params)
	}
	for _, key := range []string{"query", "prompt", "input"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return string(params)
}
