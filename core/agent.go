package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	history "github.com/sukasukasuka123/Seele/history"
	"github.com/sukasukasuka123/Seele/provider"
	types "github.com/sukasukasuka123/Seele/types"
)

// ApprovalCallback is called when a dispatched tool returns an awaiting_approval
// response. The implementation should present the options to the user, collect
// their choice, and return the choice key (matching one of the option keys in
// the approval JSON). The agent handles dispatching _decide and looping
// for nested approvals automatically.
//
// Returns the user's choice key (e.g., "execute", "skip", "abort").
// Returns an error if the user cancels or input cannot be collected.
type ApprovalCallback func(ctx context.Context, approvalJSON string) (choice string, err error)

// AgentServices 定义 Agent 对其宿主运行时的能力需求。
// Runtime 实现此接口，便于测试时 mock。
type AgentServices interface {
	// LLM 补全
	Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
	CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(delta string)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error)
	// 工具注册与调度
	Tools() []types.Tool
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)
}

// Agent 是绑定到单个会话的智能体实例。
//
// 每个 Agent 拥有：
//   - 独立的对话历史（history）
//   - 唯一的会话 ID（sessionID）
//   - tool_call 循环上限（maxLoops，默认 4）
//   - 上下文管理配置（contextCfg）
//
// 并发安全性：Agent 本身不加锁，同一个 Agent 不应跨 goroutine 并发调用。
// 如需并发，请通过 Runtime.New() 各自创建独立 Agent。
type Agent struct {
	svc        AgentServices
	sessionID  string
	history    []types.Message
	maxLoops   int
	contextCfg history.ContextConfig

	toolFilter       []string // 工具白名单，空表示不限制
	lastCompressLoop int      // 上次压缩所在的 loop 轮次，-1 表示尚未压缩

	// OnApproval 设置后，工具返回的 awaiting_approval 响应将不会注入 LLM 上下文，
	// 而是通过此回调直接与用户交互。回调返回 choice key 后，框架自动调用 _decide
	// 恢复工作流，最终结果才注入 LLM 上下文。nil 时回退到旧行为（LLM 中转）。
	OnApproval ApprovalCallback
}

// SessionID 返回本 Agent 的唯一会话标识符。
func (a *Agent) SessionID() string {
	return a.sessionID
}

// History 返回当前对话历史的只读副本。
func (a *Agent) History() []types.Message {
	cp := make([]types.Message, len(a.history))
	copy(cp, a.history)
	return cp
}

// ClearHistory 清空对话历史，但保留 system 消息（如有）。
func (a *Agent) ClearHistory() {
	var sys []types.Message
	for _, m := range a.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	a.history = sys
}

// UpdateSystemPrompt 替换	对话历史中的首条 system 消息内容。
// 若历史中没有 system 消息，则在最前面插入一条。
// 配合热加载机制使用：修改 prompt 文件后调用此方法即可实时生效。
func (a *Agent) UpdateSystemPrompt(newPrompt string) {
	if len(a.history) > 0 && a.history[0].Role == "system" {
		a.history[0].Content = &newPrompt
		return
	}
	// 没有 system 消息 → 在开头插入
	a.history = append([]types.Message{{Role: "system", Content: &newPrompt}}, a.history...)
}

// MaxLoops 返回当前的最大 tool_call 循环次数。
func (a *Agent) MaxLoops() int { return a.maxLoops }

// SetMaxLoops 设置单次 Chat 调用中最多允许的 tool_call 循环次数。
// 默认值为 8；设置过大可能导致长时间阻塞。
func (a *Agent) SetMaxLoops(n int) {
	if n > 0 {
		a.maxLoops = n
	}
}

// ContextConfig 返回当前上下文管理配置。
func (a *Agent) ContextConfig() history.ContextConfig { return a.contextCfg }

// SetContextConfig 设置上下文管理配置。零值字段使用默认值。
// 可在 Chat 调用前随时调整，对后续所有 Chat/ChatStream 调用生效。
func (a *Agent) SetContextConfig(cfg history.ContextConfig) {
	a.contextCfg = cfg.Effective()
}

// SetToolFilter 设置工具白名单。nil 表示不限制，空切片表示无可用工具。
func (a *Agent) SetToolFilter(filter []string) {
	a.toolFilter = filter
}

// filteredTools 返回经过 toolFilter 白名单过滤后的工具列表。
// nil → 不限，返回全部；非 nil（含空切片）→ 仅返回白名单内的工具。
func (a *Agent) filteredTools(all []types.Tool) []types.Tool {
	if a.toolFilter == nil {
		return all
	}
	if len(a.toolFilter) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(a.toolFilter))
	for _, name := range a.toolFilter {
		set[name] = struct{}{}
	}
	result := make([]types.Tool, 0, len(a.toolFilter))
	for _, t := range all {
		if _, ok := set[t.Function.Name]; ok {
			result = append(result, t)
		}
	}
	return result
}

// ForceAppendHistory 直接向对话历史追加一条消息（仅用于测试）。
func (a *Agent) ForceAppendHistory(msg types.Message) {
	a.history = append(a.history, msg)
}

// Chat 追加 userInput 消息，驱动 LLM 推理并自动执行 tool_calls，
// 直至 LLM 返回纯文本回复或达到 maxLoops 上限。
//
// 循环流程：
//  1. 调用 LLM（携带完整历史 + 当前可用工具列表）
//  2. 若回复含 tool_calls → 依次 dispatch → 结果追加为 tool 消息
//  3. 重新调用 LLM（携带工具结果）
//  4. 重复直到没有 tool_calls 或达到 maxLoops
//
// 每轮开始前都会实时读取 registry 刷新工具列表，支持热更新。
func (a *Agent) Chat(ctx context.Context, userInput string) (string, error) {
	if userInput != "" {
		a.history = append(a.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := a.filteredTools(a.svc.Tools())

	for loop := 0; loop < a.maxLoops; loop++ {
		cfg := a.contextCfg
		// 跳过相邻轮次的冗余压缩：压缩后 history 已大幅缩减，
		// 单轮 tool 结果通常不足以再次超过阈值
		if a.lastCompressLoop < 0 || loop-a.lastCompressLoop > 1 {
			if history.NeedCompression(a.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, a.svc, a.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[agent.Chat] session:[%s] compression failed: %v, using hard trim", a.sessionID, err)
					a.history = history.TrimHistory(a.history, cfg.MaxTokens)
				} else {
					a.history = compressed
				}
				a.lastCompressLoop = loop
			}
		}

		msg, err := a.svc.Complete(ctx, a.history, tools)
		if err != nil {
			return "", fmt.Errorf("agent[%s] chat loop %d: %w", a.sessionID, loop, err)
		}

		a.history = append(a.history, types.Message{
			Role:             "assistant",
			Content:          msg.Content,
			ReasoningContent: msg.ReasoningContent,
			ToolCalls:        msg.ToolCalls,
		})

		if len(msg.ToolCalls) == 0 {
			return *msg.Content, nil
		}

		a.dispatchToolCalls(ctx, msg.ToolCalls)
		tools = a.filteredTools(a.svc.Tools())
	}

	return "", fmt.Errorf("[agent.Chat] session:[%s] reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}

// ChatStream 与 Chat 行为完全一致，但最终的文本回复改为流式推送。
//
// 流程：
//   - tool_call 轮次：completeStream 的 delta 缓冲到 chunks，不推送 onChunk；
//     若 LLM 返回 tool_call JSON 碎片，不会泄露给用户
//   - 最终文本轮次：确认无 tool_calls 后，将缓冲的 chunks 全部推送给 onChunk
//
// onChunk 在确认是文本回复后同步调用；
// 所有 chunk 拼接即完整回复，也作为返回值返回（同时追加进 history）。
func (a *Agent) ChatStream(ctx context.Context, userInput string, onChunk func(delta string)) (string, error) {
	if userInput != "" {
		a.history = append(a.history, types.Message{Role: "user", Content: &userInput})
	}

	tools := a.filteredTools(a.svc.Tools())

	for loop := 0; loop < a.maxLoops; loop++ {
		cfg := a.contextCfg
		if a.lastCompressLoop < 0 || loop-a.lastCompressLoop > 1 {
			if history.NeedCompression(a.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, a.svc, a.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[agent.ChatStream] session:[%s] compression failed: %v, using hard trim", a.sessionID, err)
					a.history = history.TrimHistory(a.history, cfg.MaxTokens)
				} else {
					a.history = compressed
				}
				a.lastCompressLoop = loop
			}
		}

		// 先缓冲所有 delta，确认非 tool_call 轮次后才推送，防止 tool_call JSON 碎片泄露
		var chunks []string
		fullContent, reasoningContent, toolCalls, err := a.svc.CompleteStream(
			ctx, a.history, tools,
			func(delta string) {
				chunks = append(chunks, delta)
			},
		)
		if err != nil {
			return "", fmt.Errorf("[agent.ChatStream] session:[%s] stream loop %d: %w", a.sessionID, loop, err)
		}

		if len(toolCalls) == 0 {
			for _, c := range chunks {
				onChunk(c)
			}
			a.history = append(a.history, types.Message{
				Role:             "assistant",
				Content:          &fullContent,
				ReasoningContent: reasoningContent,
			})
			return fullContent, nil
		}

		// tool_call 轮次：丢弃缓冲的 chunks，仅保留结构化 tool_calls
		a.history = append(a.history, types.Message{
			Role:             "assistant",
			Content:          &fullContent,
			ReasoningContent: reasoningContent,
			ToolCalls:        toolCalls,
		})

		a.dispatchToolCalls(ctx, toolCalls)
		tools = a.filteredTools(a.svc.Tools())
	}

	return "", fmt.Errorf("[agent.ChatStream] session:[%s] reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}

// dispatchToolCalls 并发执行 tool_calls，处理瞬时错误重试，将结果追加到 history。
//
// 瞬时错误（ErrToolUnavailable）不注入 history，最多重试 3 次；
// 业务错误包装为 {"error":"..."} 注入 history 供 LLM 感知。
//
// 审批拦截：若 OnApproval 回调已设置且工具返回 awaiting_approval 响应，
// 该响应不会注入 LLM 上下文。而是通过回调收集用户选择，框架自动调用 _decide
// 恢复工作流，仅最终业务结果进入 history。LLM 对审批过程完全无感知。
func (a *Agent) dispatchToolCalls(ctx context.Context, toolCalls []types.ToolCall) {
	type dispatchResult struct {
		tc        types.ToolCall
		content   string
		transient bool
	}

	const maxDispatchRetries = 3
	const maxConcurrentDispatch = 5
	var results []dispatchResult
	for retries := 0; retries < maxDispatchRetries; retries++ {
		results = make([]dispatchResult, len(toolCalls))
		sem := make(chan struct{}, maxConcurrentDispatch)
		var wg sync.WaitGroup
		for i, tc := range toolCalls {
			wg.Add(1)
			go func(i int, tc types.ToolCall) {
				sem <- struct{}{}
				defer func() { <-sem }()
				defer wg.Done()
				start := time.Now()
				result, dispErr := a.svc.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
				elapsed := time.Since(start).Milliseconds()

				if dispErr != nil {
					if errors.Is(dispErr, provider.ErrToolUnavailable) {
						log.Printf("[agent.dispatch] session:[%s] tool_call %s UNAVAILABLE (%dms), retry %d/%d: %v",
							a.sessionID, tc.Function.Name, elapsed, retries+1, maxDispatchRetries, dispErr)
						results[i] = dispatchResult{tc: tc, transient: true}
					} else {
						log.Printf("[agent.dispatch] session:[%s] tool_call %s FAILED (%dms): %v",
							a.sessionID, tc.Function.Name, elapsed, dispErr)
						results[i] = dispatchResult{tc: tc, content: fmt.Sprintf(`{"error":%q}`, dispErr.Error())}
					}
				} else {
					log.Printf("[agent.dispatch] session:[%s] tool_call %s OK (%dms)",
						a.sessionID, tc.Function.Name, elapsed)
					results[i] = dispatchResult{tc: tc, content: result}
				}
			}(i, tc)
		}
		wg.Wait()

		hasTransient := false
		for _, r := range results {
			if r.transient {
				hasTransient = true
				break
			}
		}
		if !hasTransient {
			break
		}
		log.Printf("[agent.dispatch] session:[%s] transient dispatch, retrying in 2s (attempt %d/%d)",
			a.sessionID, retries+1, maxDispatchRetries)
		time.Sleep(2 * time.Second)
	}

	for _, r := range results {
		if r.transient {
			continue
		}

		// 审批拦截：工具返回 awaiting_approval 时不注入 LLM 上下文，
		// 而是通过 OnApproval 回调直接与用户交互
		if a.OnApproval != nil {
			if qID, ok := parseApprovalQuestionID(r.content); ok {
				final, err := a.resolveApproval(ctx, r.content, qID)
				if err != nil {
					content := history.TruncateToolResult(
						fmt.Sprintf(`{"error":%q}`, "approval failed: "+err.Error()),
						a.contextCfg.MaxToolResultChars)
					a.history = append(a.history, types.Message{
						Role:       "tool",
						ToolCallID: r.tc.ID,
						Name:       r.tc.Function.Name,
						Content:    &content,
					})
				} else {
					content := history.TruncateToolResult(final, a.contextCfg.MaxToolResultChars)
					a.history = append(a.history, types.Message{
						Role:       "tool",
						ToolCallID: r.tc.ID,
						Name:       r.tc.Function.Name,
						Content:    &content,
					})
				}
				continue
			}
		}

		// 普通 tool result：直接注入 history
		content := history.TruncateToolResult(r.content, a.contextCfg.MaxToolResultChars)
		a.history = append(a.history, types.Message{
			Role:       "tool",
			ToolCallID: r.tc.ID,
			Name:       r.tc.Function.Name,
			Content:    &content,
		})
	}
}

// resolveApproval 处理单个审批请求（含嵌套审批循环）。
// 1. 通过 OnApproval 回调收集用户选择
// 2. 调用 _decide 恢复工作流
// 3. 若结果仍为 awaiting_approval（嵌套审批），重复步骤 1-2
// 4. 返回最终业务结果
func (a *Agent) resolveApproval(ctx context.Context, approvalJSON, questionID string) (string, error) {
	// 防止无限循环（极端情况：嵌套审批）
	const maxApprovalLoops = 10

	current := approvalJSON
	currentQID := questionID

	for loop := 0; loop < maxApprovalLoops; loop++ {
		choice, err := a.OnApproval(ctx, current)
		if err != nil {
			return "", fmt.Errorf("collect choice: %w", err)
		}

		decideArgs := fmt.Sprintf(`{"question_id":%q,"choice":%q}`, currentQID, choice)
		result, dispErr := a.svc.Dispatch(ctx, "_decide", decideArgs)
		if dispErr != nil {
			return "", fmt.Errorf("dispatch _decide: %w", dispErr)
		}

		// 检查是否还有嵌套审批
		if nextQID, ok := parseApprovalQuestionID(result); ok {
			current = result
			currentQID = nextQID
			continue
		}

		return result, nil
	}

	return "", fmt.Errorf("nested approval exceeded max loops (%d)", maxApprovalLoops)
}

// parseApprovalQuestionID 检测工具返回是否包含 awaiting_approval 状态。
// 若是，返回 question_id；否则返回空字符串和 false。
func parseApprovalQuestionID(result string) (string, bool) {
	// 快速检测：避免对非 JSON 或过长字符串做完整解析
	if len(result) < 20 || len(result) > 10000 {
		return "", false
	}
	if !strings.Contains(result, `"status"`) || !strings.Contains(result, `"awaiting_approval"`) {
		return "", false
	}

	// 轻量解析：只提取 question_id，避免引入 encoding/json 的完整解析
	idx := strings.Index(result, `"question_id"`)
	if idx < 0 {
		return "", false
	}
	// 跳过 "question_id": "
	start := idx + len(`"question_id"`) + 1
	// 跳过冒号和可能的空格
	for start < len(result) && (result[start] == ':' || result[start] == ' ' || result[start] == '"') {
		start++
	}
	end := start
	for end < len(result) && result[end] != '"' {
		end++
	}
	if end <= start {
		return "", false
	}
	return result[start:end], true
}
