package agent

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
	"github.com/sukasukasuka123/Seele/workplan"
)

// WorkflowFunc 是单个工作流的函数签名。
type WorkflowFunc func(workplan.AgentFactory) (*workplan.WorkPlanResult, error)

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

		log.Printf("[%s] Dispatch method=%s", h.role, req.Method)
		result, err := wf(h.factory)
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
