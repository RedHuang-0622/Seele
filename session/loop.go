package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	seelectx "github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/storage"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

type Loop interface {
	Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
	History() []types.Message
	ClearHistory()
}

// ToolRuntime is the minimal tool capability required by ReActLoop. The loop
// does not depend on Agent, Holder, MCP, or any product tool implementation.
type ToolRuntime interface {
	VisibleTools(ctx context.Context) []types.Tool
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)
}

type ReActLoop struct {
	agent               ToolRuntime
	llm                 types.ChatCompleter
	history             []types.Message
	historyOwner        seelectx.DurableHistory
	assembler           seelectx.RequestAssembler
	promptBlocks        []seelectx.PromptBlock
	toolResultProcessor seelectx.ToolResultProcessor
	compressor          seelectx.Compressor
	contextController   seelectx.ContextController
	cfg                 SessionConfig
	sessionID           string
	cache               cache.Provider
	store               storage.Storage
	modelName           string
	tracer              tracer.Tracer
	telemetryHook       telemetry.Hook
	hooks               *LoopHooks
	respCache           *cache.ResponseCache
}

type ReActLoopOption func(*ReActLoop)

func NewReActLoop(a ToolRuntime, llm types.ChatCompleter, opts ...ReActLoopOption) *ReActLoop {
	rl := &ReActLoop{
		agent:     a,
		llm:       llm,
		history:   make([]types.Message, 0),
		assembler: seelectx.DefaultRequestAssembler{},
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

// WithHistoryOwner explicitly separates the loop's working messages from a
// caller-owned durable history. No owner is created implicitly.
func WithHistoryOwner(owner seelectx.DurableHistory) ReActLoopOption {
	return func(rl *ReActLoop) { rl.historyOwner = owner }
}

// WithRequestAssembler selects how prompt blocks and working history are
// assembled for each LLM request.
func WithRequestAssembler(assembler seelectx.RequestAssembler) ReActLoopOption {
	return func(rl *ReActLoop) {
		if assembler != nil {
			rl.assembler = assembler
		}
	}
}

// WithPromptBlocks supplies static or per-session prompt contributions. A
// custom RequestAssembler may interpret or ignore these blocks.
func WithPromptBlocks(blocks ...seelectx.PromptBlock) ReActLoopOption {
	return func(rl *ReActLoop) {
		rl.promptBlocks = append([]seelectx.PromptBlock(nil), blocks...)
	}
}

// WithToolResultProcessor lets the caller filter, reference, or preserve raw
// tool output before it enters the working history.
func WithToolResultProcessor(processor seelectx.ToolResultProcessor) ReActLoopOption {
	return func(rl *ReActLoop) { rl.toolResultProcessor = processor }
}

// WithCompressor injects an explicit context compressor. ReActLoop never calls
// it based on token thresholds.
func WithCompressor(compressor seelectx.Compressor) ReActLoopOption {
	return func(rl *ReActLoop) { rl.compressor = compressor }
}

// WithContextController explicitly enables event-driven context policy. A
// nil controller leaves context untouched.
func WithContextController(controller seelectx.ContextController) ReActLoopOption {
	return func(rl *ReActLoop) { rl.contextController = controller }
}

// WithReActTelemetryHook installs product-neutral lifecycle instrumentation. The
// hook receives Agent, LLM, and Tool intent/effect pairs using OTel-aligned
// attributes. A nil hook leaves the execution path unchanged.
func WithReActTelemetryHook(hook telemetry.Hook) ReActLoopOption {
	return func(rl *ReActLoop) { rl.telemetryHook = hook }
}

// CompressNow explicitly compresses the current history using the generic
// seelectx helper. It persists the candidate history before replacing the
// in-memory snapshot, so a failed store write leaves the session unchanged.
func (rl *ReActLoop) CompressNow(ctx context.Context) error {
	var updated []types.Message
	var err error
	if rl.compressor != nil {
		compressed, compressErr := rl.compressor.Compress(ctx, seelectx.CompressionRequest{
			SessionID: rl.sessionID, History: rl.history, MaxTokens: 8192,
		})
		updated, err = compressed.Messages, compressErr
	} else {
		updated, err = seelectx.CompressHistory(ctx, rl.llm, rl.history, 8192)
	}
	if err != nil {
		return fmt.Errorf("session: compress history: %w", err)
	}
	if err := rl.persistHistory(updated); err != nil {
		return fmt.Errorf("session: persist compressed history: %w", err)
	}
	rl.history = updated
	return nil
}

func (rl *ReActLoop) Run(ctx context.Context, userInput string, onChunk func(string)) (result string, err error) {
	if rl.llm == nil {
		return "", fmt.Errorf("session: llm client is required")
	}
	var agentInvocation telemetry.Invocation
	if rl.telemetryHook != nil {
		instrumentedCtx, invocation, hookErr := rl.telemetryHook.Before(ctx, telemetry.Action{
			Type: telemetry.EventAgentStart, Name: "react", SpanName: "agent.react",
			SpanKind: telemetry.SpanAgent,
			Attributes: telemetry.Attributes{
				telemetry.AttributeGenAIAgentName: "seele.react",
				telemetry.AttributeGenAIAgentID:   rl.sessionID,
			},
		})
		if hookErr != nil {
			return "", fmt.Errorf("session: telemetry agent before: %w", hookErr)
		}
		ctx, agentInvocation = instrumentedCtx, invocation
		defer func() {
			if err != nil {
				if errorHook, ok := rl.telemetryHook.(telemetry.ErrorHook); ok {
					_ = errorHook.OnError(ctx, "agent.react", err, telemetry.Attributes{
						telemetry.AttributeGenAIAgentID: rl.sessionID,
					})
				}
			}
			hookErr := rl.telemetryHook.After(ctx, agentInvocation, telemetry.Effect{
				Error: err,
				Attributes: telemetry.Attributes{
					telemetry.AttributeGenAIAgentID: rl.sessionID,
				},
			})
			if hookErr != nil && err == nil {
				err = fmt.Errorf("session: telemetry agent after: %w", hookErr)
			}
		}()
	}
	defer func() {
		if saveErr := rl.saveToCache(ctx); saveErr != nil && err == nil {
			err = fmt.Errorf("session: save history: %w", saveErr)
		}
	}()

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

	if restoreErr := rl.restoreHistory(ctx); restoreErr != nil {
		return "", restoreErr
	}
	rl.history = append(rl.history, types.Message{Role: "user", Content: &userInput})

	rootCtx := ctx

	for loop := 0; ; loop++ {
		if err := rl.handleContextEvent(ctx, seelectx.ContextEvent{
			Kind: seelectx.ContextBeforeModel, Turn: loop, Query: userInput,
			History: rl.History(),
		}); err != nil {
			return "", err
		}
		tools := rl.visibleTools(ctx)

		if rl.hooks != nil && rl.hooks.OnLLMStart != nil {
			rl.hooks.OnLLMStart(ctx, LLMInfo{Turn: loop, ToolCount: len(tools)})
		}

		_, llmSpan := rl.tracer.StartSpan(rootCtx,
			fmt.Sprintf("LLM Call #%d", loop+1), tracer.SpanLLMCall,
			map[string]string{
				"model": rl.modelName, "tools_count": fmt.Sprint(len(tools)),
				"history_len": fmt.Sprint(len(rl.history)),
			})

		llmCtx := ctx
		var llmInvocation telemetry.Invocation
		if rl.telemetryHook != nil {
			var hookErr error
			llmCtx, llmInvocation, hookErr = rl.telemetryHook.Before(ctx, telemetry.Action{
				Type: telemetry.EventLLMBefore, Name: "completion", SpanName: "llm.completion",
				SpanKind: telemetry.SpanLLM,
				Attributes: telemetry.Attributes{
					telemetry.AttributeGenAIOperationName: "chat",
					telemetry.AttributeGenAIRequestModel:  rl.modelName,
				},
			})
			if hookErr != nil {
				return "", fmt.Errorf("session: telemetry llm before: %w", hookErr)
			}
		}
		assistantMsg, callErr := rl.callLLM(llmCtx, tools, onChunk)
		if rl.telemetryHook != nil {
			attributes := telemetry.Attributes{}
			if assistantMsg.Usage != nil {
				attributes[telemetry.AttributeGenAIUsageInput] = assistantMsg.Usage.PromptTokens
				attributes[telemetry.AttributeGenAIUsageOutput] = assistantMsg.Usage.CompletionTokens
			}
			hookErr := rl.telemetryHook.After(llmCtx, llmInvocation, telemetry.Effect{Error: callErr, Attributes: attributes})
			if hookErr != nil && callErr == nil {
				callErr = fmt.Errorf("session: telemetry llm after: %w", hookErr)
			}
		}
		if callErr != nil {
			llmSpan.End(tracer.WithError(callErr))
			if rl.hooks != nil && rl.hooks.OnError != nil {
				rl.hooks.OnError(ctx, callErr, loop)
			}
			return "", fmt.Errorf("session loop %d: %w", loop, callErr)
		}
		rl.history = append(rl.history, assistantMsg)
		if err := rl.handleContextEvent(ctx, seelectx.ContextEvent{
			Kind: seelectx.ContextAfterAssistant, Turn: loop, Query: userInput,
			History: rl.History(),
		}); err != nil {
			return "", err
		}

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
				return "", fmt.Errorf("session loop %d: LLM returned empty content", loop)
			}
			llmSpan.SetAttr("response_type", "text")
			llmSpan.End()
			return *assistantMsg.Content, nil
		}

		llmSpan.SetAttr("response_type", "tool_calls")
		llmSpan.SetAttr("tool_count", fmt.Sprint(len(assistantMsg.ToolCalls)))
		llmSpan.End()

		for _, tc := range assistantMsg.ToolCalls {
			if rl.agent == nil {
				return "", fmt.Errorf("session: model requested tool %q but no tool runtime is configured", tc.Function.Name)
			}
			if rl.hooks != nil && rl.hooks.OnToolStart != nil {
				rl.hooks.OnToolStart(ctx, ToolCallInfo{
					Turn: loop, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				})
			}

			_, toolSpan := rl.tracer.StartSpan(rootCtx,
				tc.Function.Name, tracer.SpanToolDispatch,
				map[string]string{"tool": tc.Function.Name, "arguments": truncateArg(tc.Function.Arguments)})

			tStart := time.Now()
			toolCtx := ctx
			var toolInvocation telemetry.Invocation
			if rl.telemetryHook != nil {
				var hookErr error
				toolCtx, toolInvocation, hookErr = rl.telemetryHook.Before(ctx, telemetry.Action{
					Type: telemetry.EventToolBefore, Name: tc.Function.Name,
					SpanName: "tool." + tc.Function.Name, SpanKind: telemetry.SpanTool,
					Attributes: telemetry.Attributes{
						telemetry.AttributeGenAIToolName:   tc.Function.Name,
						telemetry.AttributeGenAIToolCallID: tc.ID,
					},
				})
				if hookErr != nil {
					return "", fmt.Errorf("session: telemetry tool before %q: %w", tc.Function.Name, hookErr)
				}
			}
			out, dErr := rl.agent.Dispatch(toolCtx, tc.Function.Name, tc.Function.Arguments)
			tElapsed := time.Since(tStart)
			if rl.telemetryHook != nil {
				hookErr := rl.telemetryHook.After(toolCtx, toolInvocation, telemetry.Effect{
					Error: dErr,
					Attributes: telemetry.Attributes{
						telemetry.AttributeGenAIToolName:   tc.Function.Name,
						telemetry.AttributeGenAIToolCallID: tc.ID,
						"seele.tool.duration_ms":           float64(tElapsed.Microseconds()) / 1000,
					},
				})
				if hookErr != nil && dErr == nil {
					dErr = fmt.Errorf("session: telemetry tool after %q: %w", tc.Function.Name, hookErr)
				}
			}

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

			content := out
			if rl.toolResultProcessor != nil {
				view, processErr := rl.toolResultProcessor.Process(ctx, seelectx.ToolResult{
					CallID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
					Raw: out, Err: dErr,
				})
				if processErr != nil {
					return "", fmt.Errorf("session: process tool result %q: %w", tc.Function.Name, processErr)
				}
				content = view.Content
			} else {
				content = truncateResult(out, rl.cfg.MaxToolResultChars)
			}
			rl.history = append(rl.history, types.Message{
				Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: &content,
			})
			toolResult := seelectx.ToolResult{
				CallID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
				Raw: out, Err: dErr,
			}
			if err := rl.handleContextEvent(ctx, seelectx.ContextEvent{
				Kind: seelectx.ContextAfterTool, Turn: loop, Query: userInput,
				History: rl.History(), Tool: &toolResult,
			}); err != nil {
				return "", err
			}
		}

		// OnIterationComplete 每轮 ReAct 结束后、下次 LLM 调用前回调。
		// 返回 false 可中断后续迭代（输入队列等场景）。
		if rl.hooks != nil && rl.hooks.OnIterationComplete != nil && !rl.hooks.OnIterationComplete(ctx, loop) {
			return "", nil
		}
	}

	// unreachable: for loop only exits via return inside body
}

func (rl *ReActLoop) visibleTools(ctx context.Context) []types.Tool {
	if rl.agent == nil {
		return nil
	}
	return rl.agent.VisibleTools(ctx)
}

func (rl *ReActLoop) handleContextEvent(ctx context.Context, event seelectx.ContextEvent) error {
	if rl.contextController == nil {
		return nil
	}
	decision, err := rl.contextController.Handle(ctx, event)
	if err != nil {
		return fmt.Errorf("session: context controller: %w", err)
	}
	if decision.ReplaceHistory {
		rl.history = append(rl.history[:0], decision.History...)
	}
	return nil
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

// AppendHistory appends a message to the loop's history. It is exposed for
// tests and for callers that want to seed a conversation before the first
// Run, or replay a session for diagnostics.
func (rl *ReActLoop) AppendHistory(msg types.Message) {
	rl.history = append(rl.history, msg)
}

func (rl *ReActLoop) restoreHistory(ctx context.Context) error {
	if rl.historyOwner != nil {
		stored, err := rl.historyOwner.Load(ctx)
		if err != nil {
			return fmt.Errorf("session: load durable history: %w", err)
		}
		rl.history = append(rl.history[:0], stored...)
		return nil
	}
	return rl.restoreFromCache()
}

func (rl *ReActLoop) restoreFromCache() error {
	if rl.sessionID == "" {
		return nil
	}
	if rl.cache != nil {
		val, ok := rl.cache.Get(rl.sessionID)
		if ok && val != "" {
			var cached []types.Message
			if err := json.Unmarshal([]byte(val), &cached); err == nil && len(cached) > 0 {
				rl.history = cached
				return nil
			}
		}
	}
	if rl.store != nil {
		stored, err := rl.store.Load(rl.sessionID)
		if err == nil && len(stored) > 0 {
			rl.history = stored
		}
	}
	return nil
}

func (rl *ReActLoop) saveToCache(ctx context.Context) error {
	return rl.persistHistoryContext(ctx, rl.history)
}

// persistHistory preserves the legacy test/helper signature. New callers that
// have a request context should use persistHistoryContext through Run.
func (rl *ReActLoop) persistHistory(history []types.Message) error {
	return rl.persistHistoryContext(context.Background(), history)
}

func (rl *ReActLoop) persistHistoryContext(ctx context.Context, history []types.Message) error {
	if rl.historyOwner != nil {
		return rl.historyOwner.Save(ctx, history)
	}
	if rl.sessionID == "" || len(history) == 0 {
		return nil
	}
	var data []byte
	var err error
	if rl.cache != nil {
		data, err = json.Marshal(history)
		if err != nil {
			return fmt.Errorf("marshal history: %w", err)
		}
	}
	if rl.store != nil {
		if err := rl.store.Save(rl.sessionID, history); err != nil {
			return fmt.Errorf("save history: %w", err)
		}
	}
	if rl.cache != nil {
		if entry := rl.cache.SetWithTTL(rl.sessionID, string(data), 5*time.Minute); entry == nil {
			return fmt.Errorf("cache history: provider rejected write")
		}
	}
	return nil
}

// callLLM 执行真实的 LLM 调用（同步或流式）。

func (rl *ReActLoop) callLLM(ctx context.Context, tools []types.Tool, onChunk func(string)) (types.Message, error) {
	assembled, err := rl.assembler.Assemble(ctx, seelectx.AssemblyRequest{
		WorkingHistory: rl.history,
		Blocks:         rl.promptBlocks,
		Tools:          tools,
	})
	if err != nil {
		return types.Message{}, fmt.Errorf("assemble request: %w", err)
	}
	messages, assembledTools := assembled.Messages, assembled.Tools
	if onChunk != nil {
		content, reasoningContent, toolCalls, err := rl.llm.CompleteStream(ctx, messages, assembledTools, onChunk)
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
			msg, err := rl.llm.Complete(ctx, messages, assembledTools)
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
	msg, err := rl.llm.Complete(ctx, messages, assembledTools)
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
