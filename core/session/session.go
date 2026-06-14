package session

import (
	"fmt"
	"time"

	history "github.com/RedHuang-0622/Seele/history"
	types "github.com/RedHuang-0622/Seele/types"
)

// Holder 管理一次 LLM 对话会话。
//
// 依赖两个独立接口（便于测试时各自 mock）：
//   - llm：LLM 推理能力（*llm.ChatClient 天然满足）
//   - tools：工具注册与调度（tool_holder.Holder 实现）
//
// 每个 Holder 拥有独立的对话历史 / 会话 ID / 上下文配置。
//
// 并发安全性：Holder 本身不加锁，同一个 Holder 不应跨 goroutine 并发调用。
// 如需并发，请各自创建独立 Holder。
type Holder struct {
	llm        types.ChatCompleter
	tools      ToolDispatcher
	sessionID  string
	history    []types.Message
	maxLoops   int
	contextCfg history.ContextConfig

	toolFilter       []string // 工具白名单，空表示不限制
	lastCompressLoop int      // 上次压缩所在的 loop 轮次，-1 表示尚未压缩

	// OnApproval 设置后，工具返回的 awaiting_approval 响应将不会注入 LLM 上下文，
	// 而是通过此回调直接与用户交互。nil 时回退到旧行为（LLM 中转）。
	OnApproval ApprovalCallback
}

// New 创建一个新的会话 Holder。
func New(llm types.ChatCompleter, tools ToolDispatcher, systemPrompt string, loopTimes int) *Holder {
	if loopTimes <= 0 {
		loopTimes = 4
	}
	h := &Holder{
		llm:              llm,
		tools:            tools,
		sessionID:        fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		maxLoops:         loopTimes,
		contextCfg:       history.DefaultContextConfig(),
		lastCompressLoop: -1,
	}
	if systemPrompt != "" {
		h.history = []types.Message{{Role: "system", Content: &systemPrompt}}
	}
	return h
}

// ── 会话与历史 ─────────────────────────────────────────────────────

// SessionID 返回本会话的唯一标识符。
func (h *Holder) SessionID() string { return h.sessionID }

// History 返回当前对话历史的只读副本。
func (h *Holder) History() []types.Message {
	cp := make([]types.Message, len(h.history))
	copy(cp, h.history)
	return cp
}

// ClearHistory 清空对话历史，但保留 system 消息（如有）。
func (h *Holder) ClearHistory() {
	var sys []types.Message
	for _, m := range h.history {
		if m.Role == "system" {
			sys = append(sys, m)
		}
	}
	h.history = sys
}

// UpdateSystemPrompt 替换对话历史中的首条 system 消息内容。
// 若历史中没有 system 消息，则在最前面插入一条。
func (h *Holder) UpdateSystemPrompt(newPrompt string) {
	if len(h.history) > 0 && h.history[0].Role == "system" {
		h.history[0].Content = &newPrompt
		return
	}
	h.history = append([]types.Message{{Role: "system", Content: &newPrompt}}, h.history...)
}

// ForceAppendHistory 直接向对话历史追加一条消息（仅用于测试）。
func (h *Holder) ForceAppendHistory(msg types.Message) {
	h.history = append(h.history, msg)
}

// ── 配置 ────────────────────────────────────────────────────────────

// MaxLoops 返回当前的最大 tool_call 循环次数。
func (h *Holder) MaxLoops() int { return h.maxLoops }

// SetMaxLoops 设置单次 Chat 调用中最多允许的 tool_call 循环次数。
func (h *Holder) SetMaxLoops(n int) {
	if n > 0 {
		h.maxLoops = n
	}
}

// ContextConfig 返回当前上下文管理配置。
func (h *Holder) ContextConfig() history.ContextConfig { return h.contextCfg }

// SetContextConfig 设置上下文管理配置。零值字段使用默认值。
func (h *Holder) SetContextConfig(cfg history.ContextConfig) {
	h.contextCfg = cfg.Effective()
}

// SetToolFilter 设置工具白名单。nil 表示不限制，空切片表示无可用工具。
func (h *Holder) SetToolFilter(filter []string) {
	h.toolFilter = filter
}

// filteredTools 返回经过 toolFilter 白名单过滤后的工具列表。
func (h *Holder) filteredTools(all []types.Tool) []types.Tool {
	if h.toolFilter == nil {
		return all
	}
	if len(h.toolFilter) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(h.toolFilter))
	for _, name := range h.toolFilter {
		set[name] = struct{}{}
	}
	result := make([]types.Tool, 0, len(h.toolFilter))
	for _, t := range all {
		if _, ok := set[t.Function.Name]; ok {
			result = append(result, t)
		}
	}
	return result
}
