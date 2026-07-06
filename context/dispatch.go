package seelectx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	types "github.com/RedHuang-0622/Seele/types"
)

// dispatchToolCalls 并发执行 tool_calls，将结果追加到 history。
//
// 瞬时错误的重试由 ToolDispatcher.Dispatch 内部处理，Holder 只关心最终结果。
//
// 审批拦截：若 OnApproval 回调已设置且工具返回 awaiting_approval 响应，
// 该响应不注入 LLM 上下文，而是通过回调直接与用户交互。
func (h *Holder) dispatchToolCalls(ctx context.Context, toolCalls []types.ToolCall) {
	type dispatchResult struct {
		tc      types.ToolCall
		content string
	}

	results := make([]dispatchResult, len(toolCalls))
	sem := make(chan struct{}, h.cfg.MaxConcurrentDispatch)
	var wg sync.WaitGroup

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(i int, tc types.ToolCall) {
			sem <- struct{}{}
			defer func() { <-sem }()
			defer wg.Done()
			start := time.Now()
			result, dispErr := h.tools.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			elapsed := time.Since(start).Milliseconds()

			if dispErr != nil {
				log.Printf("[seelectx.dispatch] %s tool_call %s FAILED (%dms): %v",
					h.sessionID, tc.Function.Name, elapsed, dispErr)
				results[i] = dispatchResult{tc: tc, content: jsonError(dispErr.Error())}
			} else {
				log.Printf("[seelectx.dispatch] %s tool_call %s OK (%dms)",
					h.sessionID, tc.Function.Name, elapsed)
				results[i] = dispatchResult{tc: tc, content: result}
			}
		}(i, tc)
	}
	wg.Wait()

	for _, r := range results {
		if h.OnApproval != nil {
			if qID, ok := parseApprovalQuestionID(r.content); ok {
				final, err := h.resolveApproval(ctx, r.content, qID)
				if err != nil {
					content := TruncateToolResult(
						fmt.Sprintf(`{"error":%q}`, "approval failed: "+err.Error()),
						h.cfg.ContextCfg.MaxToolResultChars)
					h.history = append(h.history, types.Message{
						Role:       "tool",
						ToolCallID: r.tc.ID,
						Name:       r.tc.Function.Name,
						Content:    &content,
					})
				} else {
					content := TruncateToolResult(final, h.cfg.ContextCfg.MaxToolResultChars)
					h.history = append(h.history, types.Message{
						Role:       "tool",
						ToolCallID: r.tc.ID,
						Name:       r.tc.Function.Name,
						Content:    &content,
					})
				}
				continue
			}
		}

		content := TruncateToolResult(r.content, h.cfg.ContextCfg.MaxToolResultChars)
		h.history = append(h.history, types.Message{
			Role:       "tool",
			ToolCallID: r.tc.ID,
			Name:       r.tc.Function.Name,
			Content:    &content,
		})
	}
}

// resolveApproval 处理单个审批请求（含嵌套审批循环）。
func (h *Holder) resolveApproval(ctx context.Context, approvalJSON, questionID string) (string, error) {
	current := approvalJSON
	currentQID := questionID

	for loop := 0; loop < h.cfg.MaxApprovalLoops; loop++ {
		choice, err := h.OnApproval(ctx, current)
		if err != nil {
			return "", fmt.Errorf("collect choice: %w", err)
		}

		decideArgsBytes, _ := json.Marshal(map[string]string{"question_id": currentQID, "choice": choice})
		decideArgs := string(decideArgsBytes)
		result, dispErr := h.tools.Dispatch(ctx, "_decide", decideArgs)
		if dispErr != nil {
			return "", fmt.Errorf("dispatch _decide: %w", dispErr)
		}

		if nextQID, ok := parseApprovalQuestionID(result); ok {
			current = result
			currentQID = nextQID
			continue
		}

		return result, nil
	}

	return "", fmt.Errorf("nested approval exceeded max loops (%d)", h.cfg.MaxApprovalLoops)
}

// jsonError 将错误信息安全地编码为 JSON {"error":"..."} 格式。
// 使用 json.Marshal 而非 %q，确保控制字符和特殊符号正确转义为合法 JSON。
func jsonError(errMsg string) string {
	b, _ := json.Marshal(struct{ Error string }{Error: errMsg})
	return string(b)
}

// parseApprovalQuestionID 检测工具返回是否包含 awaiting_approval 状态。
func parseApprovalQuestionID(result string) (string, bool) {
	if len(result) < 20 || len(result) > 10000 {
		return "", false
	}

	var approval struct {
		Status     string `json:"status"`
		QuestionID string `json:"question_id"`
	}
	if err := json.Unmarshal([]byte(result), &approval); err != nil {
		return "", false
	}
	if approval.Status != "awaiting_approval" || approval.QuestionID == "" {
		return "", false
	}
	return approval.QuestionID, true
}
