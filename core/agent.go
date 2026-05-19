package core

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	history "github.com/sukasukasuka123/Seele/history"
	"github.com/sukasukasuka123/Seele/provider"
	types "github.com/sukasukasuka123/Seele/types"
)

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
	runtime    *Runtime
	sessionID  string
	history    []types.Message
	maxLoops   int
	contextCfg history.ContextConfig

	toolFilter       []string // 工具白名单，空表示不限制
	lastCompressLoop int      // 上次压缩所在的 loop 轮次，-1 表示尚未压缩
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

	tools := a.filteredTools(a.runtime.tools())

	for loop := 0; loop < a.maxLoops; loop++ {
		cfg := a.contextCfg
		// 跳过相邻轮次的冗余压缩：压缩后 history 已大幅缩减，
		// 单轮 tool 结果通常不足以再次超过阈值
		if a.lastCompressLoop < 0 || loop-a.lastCompressLoop > 1 {
			if history.NeedCompression(a.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, a.runtime.llm, a.history, cfg.MaxTokens)
				if err != nil {
					log.Printf("[agent.Chat] session:[%s] compression failed: %v, using hard trim", a.sessionID, err)
					a.history = history.TrimHistory(a.history, cfg.MaxTokens)
				} else {
					a.history = compressed
				}
				a.lastCompressLoop = loop
			}
		}

		msg, err := a.runtime.llm.Complete(ctx, a.history, tools)
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
		tools = a.filteredTools(a.runtime.tools())
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

	tools := a.filteredTools(a.runtime.tools())

	for loop := 0; loop < a.maxLoops; loop++ {
		cfg := a.contextCfg
		if a.lastCompressLoop < 0 || loop-a.lastCompressLoop > 1 {
			if history.NeedCompression(a.history, cfg.CompressThreshold) {
				compressed, err := history.CompressHistory(ctx, a.runtime.llm, a.history, cfg.MaxTokens)
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
		fullContent, reasoningContent, toolCalls, err := a.runtime.llm.CompleteStream(
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
		tools = a.filteredTools(a.runtime.tools())
	}

	return "", fmt.Errorf("[agent.ChatStream] session:[%s] reached maxLoops (%d) without a final text reply",
		a.sessionID, a.maxLoops)
}

// dispatchToolCalls 并发执行 tool_calls，处理瞬时错误重试，将结果追加到 history。
//
// 瞬时错误（ErrToolUnavailable）不注入 history，最多重试 3 次；
// 业务错误包装为 {"error":"..."} 注入 history 供 LLM 感知。
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
				result, dispErr := a.runtime.dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
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
		content := history.TruncateToolResult(r.content, a.contextCfg.MaxToolResultChars)
		a.history = append(a.history, types.Message{
			Role:       "tool",
			ToolCallID: r.tc.ID,
			Name:       r.tc.Function.Name,
			Content:    &content,
		})
	}
}
