package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/sukasukasuka123/Seele/workplan"
	"github.com/sukasukasuka123/microHub/pb_api"
	pb "github.com/sukasukasuka123/microHub/proto/gen/proto"
)

// WorkflowFunc 是单个工作流的函数签名。
// userInput 是调用方传入的自然语言查询（从 gRPC ToolRequest.Params 中提取）。
type WorkflowFunc func(workplan.AgentFactory, string) (*workplan.WorkPlanResult, error)

// WorkflowMap method 名 → 工作流函数，由应用层注入，框架层不感知业务。
type WorkflowMap map[string]WorkflowFunc

// ── [workplangate] 暂停执行 ──────────────────────────────────────

// pausedExecution 存储暂停等待审批的 WorkPlan 实例。
type pausedExecution struct {
	wp       *workplan.WorkPlan
	question workplan.Question
	storedAt time.Time
}

// AgentHandler 实现 microHub tool.Handler 接口，
// 将 gRPC Dispatch 请求路由到对应工作流。
//
// [workplangate] 支持两段式审批：
//   - 工作流遇到 Approve 节点时暂停，返回 Question 给 CLI
//   - CLI 通过 _decide 方法回传用户选择，恢复执行
type AgentHandler struct {
	role     string
	registry *AgentRegistry
	factory  workplan.AgentFactory
	wfMap    WorkflowMap

	// [workplangate] 审批支持
	gate *workplan.NetworkApprovalGate // nil 时不启用两段式审批

	mu         sync.Mutex
	executions map[string]*pausedExecution // questionID → paused execution
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(role string, reg *AgentRegistry, factory workplan.AgentFactory, wfMap WorkflowMap) *AgentHandler {
	return &AgentHandler{
		role:       role,
		registry:   reg,
		factory:    factory,
		wfMap:      wfMap,
		executions: make(map[string]*pausedExecution),
	}
}

// [workplangate] SetApprovalGate 注入 NetworkApprovalGate 以启用两段式审批。
// 不调用此方法则 Approve 节点使用 WorkPlan 内部 gate（如 CLIApprovalGate）阻塞执行。
func (h *AgentHandler) SetApprovalGate(gate *workplan.NetworkApprovalGate) {
	h.gate = gate
}

func (h *AgentHandler) ServiceName() string { return h.role }

func (h *AgentHandler) Execute(req *pb.ToolRequest) (<-chan *pb.ToolResponse, error) {
	ch := make(chan *pb.ToolResponse, 1)

	// [workplangate] _decide 方法：框架级审批恢复，不在 wfMap 中注册
	if h.isDecideMethod(req.Method) {
		go h.handleDecide(req, ch)
		return ch, nil
	}

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

		// [workplangate] 检查是否需要审批
		if result != nil && result.PausedWorkPlan != nil {
			h.sendQuestion(result.PausedWorkPlan, ch)
			return
		}

		ch <- h.buildResultResp(result)
	}()
	return ch, nil
}

// ── [workplangate] 审批方法 ───────────────────────────────────────

// isDecideMethod 判断方法名是否为 _decide 后缀。
func (h *AgentHandler) isDecideMethod(method string) bool {
	return strings.HasSuffix(method, "_decide")
}

// sendQuestion 从暂停的 WorkPlan 提取 Question 并推送到 CLI。
func (h *AgentHandler) sendQuestion(wp *workplan.WorkPlan, ch chan<- *pb.ToolResponse) {
	q := wp.PendingQuestion()

	// 通过 gate 推送到 CLI（设置 OnQuestion 回调）
	if h.gate != nil && h.gate.OnQuestion != nil {
		if err := h.gate.OnQuestion(q); err != nil {
			ch <- errorResp(h.role, "", "APPROVAL_PUSH_ERROR", err.Error())
			return
		}
	}

	// 存储暂停的执行
	h.mu.Lock()
	h.executions[q.ID] = &pausedExecution{
		wp:       wp,
		question: q,
		storedAt: time.Now(),
	}
	h.mu.Unlock()

	log.Printf("[%s] workflow paused, question=%s options=%d", h.role, q.ID, len(q.Options))

	// 构建审批响应发送给 CLI
	resp := h.buildQuestionResp(q)
	ch <- resp
}

// handleDecide 处理 _decide 调用：匹配 K→V，恢复 WorkPlan 执行。
func (h *AgentHandler) handleDecide(req *pb.ToolRequest, ch chan<- *pb.ToolResponse) {
	defer close(ch)

	var params struct {
		QuestionID string `json:"question_id"`
		Choice     string `json:"choice"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		ch <- errorResp(h.role, req.TaskId, "BAD_PARAMS",
			fmt.Sprintf("_decide params parse error: %v", err))
		return
	}
	if params.QuestionID == "" || params.Choice == "" {
		ch <- errorResp(h.role, req.TaskId, "BAD_PARAMS",
			"question_id and choice are required")
		return
	}

	h.mu.Lock()
	exec, ok := h.executions[params.QuestionID]
	if ok {
		delete(h.executions, params.QuestionID)
	}
	h.mu.Unlock()

	if !ok {
		ch <- errorResp(h.role, req.TaskId, "NOT_FOUND",
			fmt.Sprintf("execution %q not found or expired", params.QuestionID))
		return
	}

	// K→V 匹配
	v, exact := exec.question.Resolve(params.Choice)
	if !exact {
		log.Printf("[%s] _decide: choice=%q not found for question=%s, using default",
			h.role, params.Choice, params.QuestionID)
	}
	exec.wp.SetDecision(v)

	log.Printf("[%s] _decide: question=%s choice=%s v=%v, resuming", h.role, params.QuestionID, params.Choice, v)

	// 恢复执行
	ctx := context.Background() // TODO: 从 req 中提取或维持原始 context
	result, err := exec.wp.Resume(ctx)
	if err != nil {
		ch <- errorResp(h.role, req.TaskId, "RESUME_ERROR", err.Error())
		return
	}

	// 嵌套审批：Resume 可能再次暂停
	if result != nil && result.PausedWorkPlan != nil {
		h.sendQuestion(result.PausedWorkPlan, ch)
		return
	}

	ch <- h.buildResultResp(result)
	log.Printf("[%s] _decide: workflow completed", h.role)
}

// ── [workplangate] 响应构造 ───────────────────────────────────────

// buildQuestionResp 构造审批请求响应，发送给 CLI。
func (h *AgentHandler) buildQuestionResp(q workplan.Question) *pb.ToolResponse {
	resp := map[string]interface{}{
		"status":       "awaiting_approval",
		"question_id":  q.ID,
		"content":      q.Content,
		"options":      q.Options,
		"node_elapsed": "0s",
	}
	raw, _ := json.Marshal(resp)
	r, err := pb_api.OKResp(h.role, "", json.RawMessage(raw))
	if err != nil {
		return errorResp(h.role, "", "BUILD_RESP", err.Error())
	}
	return r
}

// buildResultResp 构造正常完成响应。
func (h *AgentHandler) buildResultResp(result *workplan.WorkPlanResult) *pb.ToolResponse {
	resp := map[string]interface{}{
		"output":         result.FinalOutputString(),
		"total_elapsed":  result.TotalElapsed.String(),
		"nodes_executed": len(result.NodeResults),
		"aborted":        result.Aborted,
	}
	respBytes, _ := json.Marshal(resp)
	r, err := pb_api.OKResp(h.role, "", json.RawMessage(respBytes))
	if err != nil {
		return errorResp(h.role, "", "BUILD_RESP", err.Error())
	}
	return r
}

// ── 工具函数 ───────────────────────────────────────────────────────

func errorResp(toolName, taskID, code, msg string) *pb.ToolResponse {
	return pb_api.ErrorResp(toolName, taskID, code, msg, "")
}

// extractUserInput 从 ToolRequest.Params 中提取用户自然语言查询。
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
