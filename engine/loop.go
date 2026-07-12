package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	seelectx "github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

type Loop interface {
	Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
	History() []types.Message
	ClearHistory()
}

type ReActLoop struct {
	agent     *agent.Agent
	llm       types.ChatCompleter
	history   []types.Message
	cfg       SessionConfig
	sessionID string
	cache     cache.Provider
	store     *storage.Store
	modelName string
	tracer    tracer.Tracer
	hooks     *LoopHooks
	respCache *cache.ResponseCache
}

type ReActLoopOption func(*ReActLoop)

func NewReActLoop(a *agent.Agent, llm types.ChatCompleter, opts ...ReActLoopOption) *ReActLoop {
	rl := &ReActLoop{
		agent:     a,
		llm:       llm,
		history:   make([]types.Message, 0),
		cfg:       DefaultSessionConfig(),
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		tracer:    &tracer.NoopTracer{},
	}
	for _, opt := range opts {
		opt(rl)
	}
	rl.cfg = rl.cfg.Effective()
	return rl
}

func WithMaxLoops(n int) ReActLoopOption {
	return func(rl *ReActLoop) { rl.cfg.MaxLoops = n }
}
func WithSessionID(id string) ReActLoopOption {
	return func(rl *ReActLoop) { rl.sessionID = id }
}
func WithModelName(name string) ReActLoopOption {
	return func(rl *ReActLoop) { rl.modelName = name }
}

func (rl *ReActLoop) Run(ctx context.Context, userInput string, onChunk func(string)) (result string, err error) {
	defer rl.saveToCache()

	ctx, rootSpan := rl.tracer.NewTrace(ctx, rl.sessionID)
	rootSpan.SetAttr("user_input", tracer.Truncate(userInput, 500))
	if rl.modelName != "" {
		rootSpan.SetAttr("model", rl.modelName)
	}
	defer func() {
		if err != nil {
			rootSpan.End(tracer.WithError(err))
		} else {
			rootSpan.End()
		}
	}()

	rl.restoreFromCache()
	rl.history = append(rl.history, types.Message{Role: "user", Content: &userInput})

	if seelectx.EstimateHistoryTokens(rl.history) > 6144 {
		compressed, cerr := seelectx.CompressHistory(ctx, rl.llm, rl.history, 8192)
		if cerr == nil {
			rl.history = compressed
		}
	}

	rootCtx := ctx

	for loop := 0; ; loop++ {
		tools := rl.agent.VisibleTools(ctx)

		if rl.hooks != nil && rl.hooks.OnLLMStart != nil {
			rl.hooks.OnLLMStart(ctx, LLMInfo{Turn: loop, ToolCount: len(tools)})
		}

		_, llmSpan := rl.tracer.StartSpan(rootCtx,
			fmt.Sprintf("LLM Call #%d", loop+1), tracer.SpanLLMCall,
			map[string]string{
				"model": rl.modelName, "tools_count": fmt.Sprint(len(tools)),
				"history_len": fmt.Sprint(len(rl.history)),
			})

		assistantMsg, callErr := rl.callLLM(ctx, tools, onChunk)
		if callErr != nil {
			llmSpan.End(tracer.WithError(callErr))
			if rl.hooks != nil && rl.hooks.OnError != nil {
				rl.hooks.OnError(ctx, callErr, loop)
			}
			return "", fmt.Errorf("engine loop %d: %w", loop, callErr)
		}
		rl.history = append(rl.history, assistantMsg)

		if assistantMsg.Usage != nil {
			llmSpan.SetAttr("input_tokens", fmt.Sprint(assistantMsg.Usage.PromptTokens))
			llmSpan.SetAttr("output_tokens", fmt.Sprint(assistantMsg.Usage.CompletionTokens))
			llmSpan.SetAttr("total_tokens", fmt.Sprint(assistantMsg.Usage.TotalTokens))
		}

		if rl.hooks != nil && rl.hooks.OnLLMComplete != nil {
			info := LLMInfo{Turn: loop, ToolCount: len(tools), Usage: assistantMsg.Usage}
			if assistantMsg.Content != nil {
				info.Response = *assistantMsg.Content
			}
			if len(assistantMsg.ToolCalls) > 0 {
				info.ToolCalls = assistantMsg.ToolCalls
			}
			rl.hooks.OnLLMComplete(ctx, info)
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if (assistantMsg.Content == nil || *assistantMsg.Content == "") && assistantMsg.ReasoningContent != "" {
				llmSpan.SetAttr("response_type", "text")
				llmSpan.End()
				return assistantMsg.ReasoningContent, nil
			}
			if assistantMsg.Content == nil || *assistantMsg.Content == "" {
				llmSpan.End(tracer.WithAttr("response_type", "empty"))
				return "", fmt.Errorf("engine loop %d: LLM returned empty content", loop)
			}
			llmSpan.SetAttr("response_type", "text")
			llmSpan.End()
			return *assistantMsg.Content, nil
		}

		llmSpan.SetAttr("response_type", "tool_calls")
		llmSpan.SetAttr("tool_count", fmt.Sprint(len(assistantMsg.ToolCalls)))
		llmSpan.End()

		for _, tc := range assistantMsg.ToolCalls {
			if rl.hooks != nil && rl.hooks.OnToolStart != nil {
				rl.hooks.OnToolStart(ctx, ToolCallInfo{
					Turn: loop, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				})
			}

			_, toolSpan := rl.tracer.StartSpan(rootCtx,
				tc.Function.Name, tracer.SpanToolDispatch,
				map[string]string{"tool": tc.Function.Name, "arguments": truncateArg(tc.Function.Arguments)})

			tStart := time.Now()
			out, dErr := rl.agent.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
			tElapsed := time.Since(tStart)

			if rl.hooks != nil && rl.hooks.OnToolComplete != nil {
				rl.hooks.OnToolComplete(ctx, ToolCallInfo{
					Turn: loop, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
					Result: out, Error: dErr, Duration: tElapsed,
				})
			}

			if dErr != nil {
				out = fmt.Sprintf(`{"error": %q}`, dErr.Error())
				toolSpan.End(tracer.WithError(dErr))
			} else {
				toolSpan.SetAttr("result_length", fmt.Sprint(len(out)))
				toolSpan.End()
			}

			content := truncateResult(out, rl.cfg.MaxToolResultChars)
			rl.history = append(rl.history, types.Message{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: &content,
			})
		}
	}

	// unreachable: for loop only exits via return inside body
}

func (rl *ReActLoop) History() []types.Message {
	cp := make([]types.Message, len(rl.history))
	copy(cp, rl.history)
	return cp
}

func (rl *ReActLoop) ClearHistory() {
	var sys []types.Message
	for _, m := range rl.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	rl.history = sys
}

func (rl *ReActLoop) restoreFromCache() {
	if rl.sessionID == "" {
		return
	}
	if rl.cache != nil {
		val, ok := rl.cache.Get(rl.sessionID)
		if ok && val != "" {
			var cached []types.Message
			if err := json.Unmarshal([]byte(val), &cached); err == nil && len(cached) > 0 {
				rl.history = cached
				return
			}
		}
	}
	if rl.store != nil {
		stored, err := rl.store.Load(rl.sessionID)
		if err == nil && len(stored) > 0 {
			rl.history = stored
		}
	}
}

func (rl *ReActLoop) saveToCache() {
	if rl.sessionID == "" || len(rl.history) == 0 {
		return
	}
	if rl.cache != nil {
		data, err := json.Marshal(rl.history)
		if err == nil {
			rl.cache.SetWithTTL(rl.sessionID, string(data), 5*time.Minute)
		}
	}
	if rl.store != nil {
		_ = rl.store.Save(rl.sessionID, rl.history)
	}
}

// callLLM 执行真实的 LLM 调用（同步或流式）。
func (rl *ReActLoop) callLLM(ctx context.Context, tools []types.Tool, onChunk func(string)) (types.Message, error) {
	if onChunk != nil {
		content, reasoningContent, toolCalls, err := rl.llm.CompleteStream(ctx, rl.history, tools, onChunk)
		if err != nil {
			return types.Message{}, err
		}
		if len(toolCalls) > 0 {
			msg := types.Message{Role: "assistant", Content: nil, ToolCalls: toolCalls}
			if reasoningContent != "" {
				msg.ReasoningContent = reasoningContent
			}
			return msg, nil
		}
		if content == "" {
			msg, err := rl.llm.Complete(ctx, rl.history, tools)
			if err != nil {
				return types.Message{}, err
			}
			return msg, nil
		}
		msg := types.Message{Role: "assistant", Content: &content}
		if reasoningContent != "" {
			msg.ReasoningContent = reasoningContent
		}
		est := len(content) / 4
		if est < 1 {
			est = 1
		}
		msg.Usage = &types.Usage{PromptTokens: 0, CompletionTokens: est, TotalTokens: est}
		return msg, nil
	}
	msg, err := rl.llm.Complete(ctx, rl.history, tools)
	if err != nil {
		return types.Message{}, err
	}
	return msg, nil
}

func truncateResult(content string, maxChars int) string {
	if len(content) <= maxChars {
		return content
	}
	return content[:maxChars] + "\n...[truncated]"
}

func truncateArg(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
