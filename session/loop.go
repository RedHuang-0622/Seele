package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
	// historyPublisher 是历史快照发布器（宿主观测面用；见 WithHistoryPublisher）。
	historyPublisher func([]types.Message)
	cfg              SessionConfig
	sessionID        string
	cache            cache.Provider
	store            storage.Storage
	modelName        string
	tracer           tracer.Tracer
	telemetryHook    telemetry.Hook
	hooks            *LoopHooks
	respCache        *cache.ResponseCache

	// stateMu 是工作状态的短临界区：只护 history / promptBlocks / cfg 这些字段的
	// 读写。临界区里**绝不**调用模型、工具、LoopHooks、ContextController、发布器或
	// 持久化——「回合不持锁跑整轮」这条不变量就落在这里（见 turn.go）。
	stateMu sync.Mutex
	// running 表示本循环此刻有一轮在飞（guard 在 stateMu 内）。在飞期间宿主的写
	// 命令挂进 pending，由循环在安全检查点落地。
	running bool
	// pending 是回合在飞期间排队的写命令（替换 / 追加 / 系统提示）。
	pending []historyCommand
	// blockSystemPrompt 表示 system prompt 由 promptBlocks 承载：SetSystemPrompt
	// 改块，而不是改历史里的 system 消息。
	blockSystemPrompt bool
}

type ReActLoopOption func(*ReActLoop)

// WithHistoryPublisher 注册历史快照发布器：循环在每个历史检查点
// （模型调用前、assistant 落历史后、工具结果落历史后）把一份**已拷贝**的
// 历史交给发布器。用途是宿主的观测面（UI/详情/落账投影）能在 ChatStream
// 持锁运行期间读到最后一次发布的快照，而不去抢会话锁。
//
// 发布器在持锁的循环 goroutine 内同步调用，必须无阻塞、不可重入本 Session。
func WithHistoryPublisher(publish func([]types.Message)) ReActLoopOption {
	return func(rl *ReActLoop) { rl.historyPublisher = publish }
}

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
	current := rl.History()
	var updated []types.Message
	var err error
	if rl.compressor != nil {
		compressed, compressErr := rl.compressor.Compress(ctx, seelectx.CompressionRequest{
			SessionID: rl.sessionID, History: current, MaxTokens: 8192,
		})
		updated, err = compressed.Messages, compressErr
	} else {
		updated, err = seelectx.CompressHistory(ctx, rl.llm, current, 8192)
	}
	if err != nil {
		return fmt.Errorf("session: compress history: %w", err)
	}
	if err := rl.persistHistory(updated); err != nil {
		return fmt.Errorf("session: persist compressed history: %w", err)
	}
	// 落地走与宿主同一条替换路径：空闲当场生效；回合在飞则排队到下一个检查点，
	// 并同样校验在飞 tool_call 单元。
	if err := rl.ReplaceHistory(updated); err != nil {
		return fmt.Errorf("session: install compressed history: %w", err)
	}
	return nil
}

func (rl *ReActLoop) Run(ctx context.Context, userInput string, onChunk func(string)) (result string, err error) {
	if rl.llm == nil {
		return "", fmt.Errorf("session: llm client is required")
	}
	// 回合开始：置「在飞」标记。此后宿主的写命令一律排队、由本循环在安全检查点落地；
	// 回合收口（含提前 return）时把剩余命令一次落地——最后那个检查点就是「回合已
	// 结束」这一刻。
	rl.stateMu.Lock()
	rl.running = true
	rl.stateMu.Unlock()
	defer rl.endTurn()
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
	rl.appendWorking(types.Message{Role: "user", Content: &userInput})

	rootCtx := ctx

	for loop := 0; ; loop++ {
		// 模型请求前的安全检查点：上一轮的工具结果都已入历史，此刻没有在飞的
		// tool_call 单元，排队命令可以整体落地。
		rl.drainPending()
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
				"history_len": fmt.Sprint(rl.historyLen()),
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
		rl.appendWorking(assistantMsg)
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

			// 工具派发返回后的安全检查点：这一截 tool_call 单元的结果还没入历史，
			// 「在飞」状态与宿主提交替换时一致，排队命令在此落地（含在飞单元校验）。
			rl.drainPending()

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
			rl.appendWorking(types.Message{
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
	// 历史检查点：先把快照发布给宿主观测面（event.History 是调用方已拷贝的
	// 副本），再走上下文控制器——控制器为 nil 时也要发布。
	rl.publishHistory(event.History)
	if rl.contextController == nil {
		return nil
	}
	decision, err := rl.contextController.Handle(ctx, event)
	if err != nil {
		return fmt.Errorf("session: context controller: %w", err)
	}
	if decision.ReplaceHistory {
		rl.replaceWorking(decision.History)
	}
	return nil
}

// History 返回当前工作历史的拷贝。它只取工作状态的短临界区，**永不阻塞**：回合在飞
// 时也一样，任何 goroutine、任何时刻调用都立刻返回（旧模型下这个调用要排在整轮会话
// 锁后面，环内调用更是同 goroutine 自锁）。
func (rl *ReActLoop) History() []types.Message {
	rl.stateMu.Lock()
	defer rl.stateMu.Unlock()
	return rl.snapshotLocked()
}

// ReplaceHistory 就地替换工作历史，语义与「清空后逐条追加」一致。
//
// 两种落点：会话空闲 → 当场落地；回合在飞 → 挂进检查点队列并**立即返回**（不等回合），
// 由循环在下一个安全检查点落地——工具派发返回后、追加 tool 结果之前，或每次模型请求
// 之前。于是回合内的工具 handler 与回合外的折叠走同一套方法，同回合的下一次请求读到的
// 就是替换后的历史。
//
// 提交时按当前工作历史校验：会丢掉在飞 tool_call 单元（结果尚未 append）的替换被拒
// （ErrInFlightToolCallDropped）且历史不动。安全检查点上这份状态与提交时刻一致，因此
// 这一次校验就是全部校验。
func (rl *ReActLoop) ReplaceHistory(history []types.Message) error {
	if rl == nil {
		return ErrLoopUnsupported
	}
	replacement := append([]types.Message(nil), history...)
	rl.stateMu.Lock()
	if dropped := droppedInFlightUnit(rl.history, replacement); dropped != "" {
		rl.stateMu.Unlock()
		return fmt.Errorf("%w: tool call %q", ErrInFlightToolCallDropped, dropped)
	}
	if rl.running {
		rl.enqueueLocked(historyCommand{replace: replacement})
		rl.stateMu.Unlock()
		return nil
	}
	rl.history = append(rl.history[:0], replacement...)
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
	return nil
}

// SetSystemPrompt 替换 system prompt：会话把 prompt 静态挂在 promptBlocks 上时改块，
// 否则改历史里的 system 消息（与旧 setSystemPromptLocked 同一实现，两条路径不分叉）。
// 落点规则与 ReplaceHistory 一致。
func (rl *ReActLoop) SetSystemPrompt(prompt string) {
	if rl == nil {
		return
	}
	rl.stateMu.Lock()
	if rl.running {
		rl.enqueueLocked(historyCommand{systemRaw: &prompt})
		rl.stateMu.Unlock()
		return
	}
	rl.setSystemPromptLocked(prompt)
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
}

// SetMaxLoops 动态设置最大 tool_call 循环次数（0 表示默认值 25）。
func (rl *ReActLoop) SetMaxLoops(n int) {
	if rl == nil {
		return
	}
	if n <= 0 {
		n = DefaultSessionConfig().MaxLoops
	}
	rl.stateMu.Lock()
	rl.cfg.MaxLoops = n
	rl.stateMu.Unlock()
}

// appendWorking / replaceWorking / historyLen 是循环自己的读点与写点：只取工作状态短
// 临界区，不排队（循环自己在回合内写历史本来就是顺序的，排队是给回合外宿主的命令用的）。
func (rl *ReActLoop) appendWorking(msg types.Message) {
	rl.stateMu.Lock()
	rl.history = append(rl.history, msg)
	rl.stateMu.Unlock()
}

func (rl *ReActLoop) replaceWorking(history []types.Message) {
	rl.stateMu.Lock()
	rl.history = append(rl.history[:0], history...)
	rl.stateMu.Unlock()
}

func (rl *ReActLoop) historyLen() int {
	rl.stateMu.Lock()
	defer rl.stateMu.Unlock()
	return len(rl.history)
}

func (rl *ReActLoop) snapshotLocked() []types.Message {
	cp := make([]types.Message, len(rl.history))
	copy(cp, rl.history)
	return cp
}

// setSystemPromptLocked 是 SetSystemPrompt 的实现体（调用方持有 stateMu）。
func (rl *ReActLoop) setSystemPromptLocked(prompt string) {
	msg := types.Message{Role: "system", Content: &prompt}
	if rl.blockSystemPrompt {
		for i := range rl.promptBlocks {
			if rl.promptBlocks[i].Name == "system" {
				rl.promptBlocks[i].Messages = []types.Message{msg}
				return
			}
		}
	}
	for i, m := range rl.history {
		if m.Role == "system" {
			rl.history[i] = msg
			return
		}
	}
	rl.history = append([]types.Message{msg}, rl.history...)
}

// maxPendingHistoryCommands 是检查点队列的上界。一轮里的写命令正常只有几条（折叠、
// 系统提示、回合注入）；真堆到这个量说明宿主在一轮里写了上千条，此时退化成当场落地
// ——宁可落进中间态，也不静默丢命令。
const maxPendingHistoryCommands = 1024

// enqueueLocked 把一条写命令挂进检查点队列（调用方持有 stateMu）。
func (rl *ReActLoop) enqueueLocked(cmd historyCommand) {
	switch {
	case cmd.replace != nil:
		// 替换会覆盖整份工作历史，因此它同时清掉更早排队的命令：先落地再覆盖与直接
		// 覆盖不可区分（写 promptBlocks 的那一支本来就不受替换影响）。
		rl.pending = append(rl.pending[:0], cmd)
	case cmd.systemRaw != nil && rl.blockSystemPrompt:
		// 块写入与工作历史无关：不会落进中间态，也不会被替换吞掉，直接落地。
		rl.applyCommandLocked(cmd)
	case len(rl.pending) >= maxPendingHistoryCommands:
		rl.applyCommandLocked(cmd)
	default:
		rl.pending = append(rl.pending, cmd)
	}
}

// applyCommandLocked 落地一条命令（调用方持有 stateMu）。
func (rl *ReActLoop) applyCommandLocked(cmd historyCommand) {
	switch {
	case cmd.replace != nil:
		rl.history = append(rl.history[:0], cmd.replace...)
	case cmd.append != nil:
		rl.history = append(rl.history, *cmd.append)
	case cmd.systemRaw != nil:
		rl.setSystemPromptLocked(*cmd.systemRaw)
	}
}

// drainPending 在安全检查点落地排队的写命令，并把结果发布给观测面。
//
// 调用点只有两处，都落在「没有在飞 tool_call 单元」或「在飞状态与提交时刻一致」的
// 位置：工具派发返回后、追加 tool 结果之前；以及每次模型请求之前。
func (rl *ReActLoop) drainPending() {
	rl.stateMu.Lock()
	if len(rl.pending) == 0 {
		rl.stateMu.Unlock()
		return
	}
	commands := rl.pending
	rl.pending = nil
	for _, cmd := range commands {
		rl.applyCommandLocked(cmd)
	}
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
}

// endTurn 收口一轮：清「在飞」标记，并把这一轮里剩下没落地的写命令落地（最后那个
// 检查点就是「回合已结束」这一刻）。
func (rl *ReActLoop) endTurn() {
	rl.stateMu.Lock()
	rl.running = false
	commands := rl.pending
	rl.pending = nil
	for _, cmd := range commands {
		rl.applyCommandLocked(cmd)
	}
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
}

// publishHistory 把一份历史快照交给发布器（nil 发布器为 no-op）。
func (rl *ReActLoop) publishHistory(history []types.Message) {
	if rl == nil || rl.historyPublisher == nil {
		return
	}
	rl.historyPublisher(history)
}

// ClearHistory 清空工作历史（保留 system 消息）：空闲当场落地，回合在飞排队到下一个
// 检查点。
func (rl *ReActLoop) ClearHistory() {
	if rl == nil {
		return
	}
	rl.stateMu.Lock()
	var sys []types.Message
	for _, m := range rl.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	rl.history = sys
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
}

// AppendHistory 追加一条消息到工作历史：空闲当场落地，回合在飞排队到下一个检查点
// （回合里插消息若落进「assistant 已带 tool_calls、结果未落」的中间态会造孤儿，排队
// 就是为了避开它）。种子会话（首次 Run 之前）与诊断回放也走这里。
func (rl *ReActLoop) AppendHistory(msg types.Message) {
	if rl == nil {
		return
	}
	rl.stateMu.Lock()
	if rl.running {
		rl.enqueueLocked(historyCommand{append: &msg})
		rl.stateMu.Unlock()
		return
	}
	rl.history = append(rl.history, msg)
	snapshot := rl.snapshotLocked()
	rl.stateMu.Unlock()
	rl.publishHistory(snapshot)
}

func (rl *ReActLoop) restoreHistory(ctx context.Context) error {
	if rl.historyOwner != nil {
		stored, err := rl.historyOwner.Load(ctx)
		if err != nil {
			return fmt.Errorf("session: load durable history: %w", err)
		}
		rl.replaceWorking(stored)
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
				rl.replaceWorking(cached)
				return nil
			}
		}
	}
	if rl.store != nil {
		stored, err := rl.store.Load(rl.sessionID)
		if err == nil && len(stored) > 0 {
			rl.replaceWorking(stored)
		}
	}
	return nil
}

func (rl *ReActLoop) saveToCache(ctx context.Context) error {
	return rl.persistHistoryContext(ctx, rl.History())
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
	// 请求装配只读工作状态的一份快照：拷贝在短临界区内做，模型调用在锁外。
	rl.stateMu.Lock()
	working := append([]types.Message(nil), rl.history...)
	blocks := append([]seelectx.PromptBlock(nil), rl.promptBlocks...)
	rl.stateMu.Unlock()
	assembled, err := rl.assembler.Assemble(ctx, seelectx.AssemblyRequest{
		WorkingHistory: working,
		Blocks:         blocks,
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
