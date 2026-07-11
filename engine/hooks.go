package engine

import (
	"context"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// ─── 回调事件数据 ───────────────────────────────────────────────────────

// LLMInfo 描述一次 LLM 调用的上下文信息。
// LLMStart 时仅 Turn/ToolCount 有效，LLMComplete 时 Response/ToolCalls/Usage 补充。
type LLMInfo struct {
	Turn      int               // 当前是第几次 LLM 调用（0-based）
	ToolCount int               // 本次调用可用的工具数
	Response  string            // LLM 返回的纯文本（tool_calls 时为空）
	ToolCalls []types.ToolCall  // LLM 返回的工具调用（文本回复时为 nil）
	Usage     *types.Usage      // token 用量（可能为 nil）
}

// ToolCallInfo 描述一次工具调用的完整信息。
// ToolStart 时仅 Turn/Name/Arguments 有效，ToolComplete 时 Result/Error/Duration 补充。
type ToolCallInfo struct {
	Turn      int           // 所在 ReAct 轮次
	Name      string        // 工具名
	Arguments string        // JSON 参数（原始，未截断）
	Result    string        // 工具返回结果（原始，未截断）
	Error     error         // 工具执行错误（成功时为 nil）
	Duration  time.Duration // 工具执行耗时
}

// ─── 回调接口 ──────────────────────────────────────────────────────────

// LoopHooks 是 ReAct 循环的可选回调集合。
// 所有字段均为可选，nil 回调静默跳过。
// 回调在循环内同步调用——不要在回调中执行阻塞操作。
type LoopHooks struct {
	// OnLLMStart 在每次 LLM 调用之前触发。
	OnLLMStart func(ctx context.Context, info LLMInfo)

	// OnLLMComplete 在 LLM 返回之后触发（含文本和 tool_calls 两种情况）。
	OnLLMComplete func(ctx context.Context, info LLMInfo)

	// OnToolStart 在每次工具调度之前触发。
	OnToolStart func(ctx context.Context, info ToolCallInfo)

	// OnToolComplete 在工具执行完成后触发（含成功和失败两种情况）。
	OnToolComplete func(ctx context.Context, info ToolCallInfo)

	// OnError 在循环因错误退出时触发。
	OnError func(ctx context.Context, err error, turn int)
}
