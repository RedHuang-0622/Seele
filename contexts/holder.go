package seelectx

import (
	"fmt"
	"time"

	types "github.com/RedHuang-0622/Seele/types"
)

// Holder 管理一次 LLM 对话会话。
//
// 依赖两个独立接口（便于测试时各自 mock）：
//   - llm：LLM 推理能力（*llm.ChatClient 天然满足）
//   - tools：工具注册与调度（tool_holder.Holder 实现）
//
// 每个 Holder 拥有独立的对话历史 / 会话 ID / 上下文配置 / 缓存。
//
// 并发安全性：Holder 本身不加锁，同一个 Holder 不应跨 goroutine 并发调用。
// 如需并发，请各自创建独立 Holder。
type Holder struct {
	llm   types.ChatCompleter
	tools ToolDispatcher

	sessionID string
	history   []types.Message
	cfg       SessionConfig

	toolFilter       []string // 工具白名单，空表示不限制
	lastCompressLoop int      // 上次压缩所在的 loop 轮次，-1 表示尚未压缩

	// OnApproval 设置后，工具返回的 awaiting_approval 响应将不会注入 LLM 上下文，
	// 而是通过此回调直接与用户交互。nil 时回退到旧行为（LLM 中转）。
	OnApproval ApprovalCallback

	// cache 是可选的缓存提供者。nil 时所有缓存操作为空操作。
	// 通过 NewWithCache 或 SetCache 设置。
	cache CacheProvider
}

// New 创建一个新的会话 Holder。
// cfg 的零值字段自动使用 DefaultSessionConfig() 默认值。
func New(llm types.ChatCompleter, tools ToolDispatcher, systemPrompt string, cfg SessionConfig) *Holder {
	return NewWithCache(llm, tools, systemPrompt, cfg, nil)
}

// NewWithCache 创建一个新的会话 Holder，额外指定缓存提供者。
//
// cache 参数是可选的，传入 nil 表示不使用缓存（所有缓存操作为空操作）。
// 缓存可用于 chatLoop 缓存 LLM 响应，也可通过 Holder.CacheStats / CacheList 等方法查看。
func NewWithCache(llm types.ChatCompleter, tools ToolDispatcher, systemPrompt string, cfg SessionConfig, cache CacheProvider) *Holder {
	cfg = cfg.Effective()
	h := &Holder{
		llm:              llm,
		tools:            tools,
		sessionID:        fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		cfg:              cfg,
		lastCompressLoop: -1,
		cache:            cache,
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
func (h *Holder) MaxLoops() int { return h.cfg.MaxLoops }

// SetMaxLoops 设置单次 Chat 调用中最多允许的 tool_call 循环次数。
func (h *Holder) SetMaxLoops(n int) {
	if n > 0 {
		h.cfg.MaxLoops = n
	}
}

// ContextConfig 返回当前上下文管理配置。
func (h *Holder) ContextConfig() ContextConfig { return h.cfg.ContextCfg }

// SetContextConfig 设置上下文管理配置。零值字段使用默认值。
func (h *Holder) SetContextConfig(cfg ContextConfig) {
	h.cfg.ContextCfg = cfg.Effective()
}

// SessionConfig 返回当前会话配置的只读副本。
func (h *Holder) SessionConfig() SessionConfig { return h.cfg }

// SetToolFilter 设置工具白名单。nil 表示不限制，空切片表示无可用工具。
func (h *Holder) SetToolFilter(filter []string) {
	h.toolFilter = filter
}

// filteredTools 返回经过白名单过滤后的工具列表。
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

// ── 缓存 ────────────────────────────────────────────────────────────

// SetCache 设置或替换缓存提供者。nil 表示停用缓存。
func (h *Holder) SetCache(cache CacheProvider) {
	h.cache = cache
}

// Cache 返回当前缓存提供者。可能为 nil。
func (h *Holder) Cache() CacheProvider {
	return h.cache
}

// CacheStats 返回缓存统计信息。无缓存时返回零值。
func (h *Holder) CacheStats() CacheStats {
	if h.cache == nil {
		return CacheStats{}
	}
	return h.cache.Stats()
}

// CacheList 返回所有缓存条目元数据。无缓存时返回 nil。
func (h *Holder) CacheList() []CacheEntry {
	if h.cache == nil {
		return nil
	}
	return h.cache.List()
}

// CacheClear 按前缀清理缓存。无缓存时返回 0。
func (h *Holder) CacheClear(prefix string) int {
	if h.cache == nil {
		return 0
	}
	return h.cache.ClearByPrefix(prefix)
}

// CacheClearAll 清空所有缓存。无缓存时返回 0。
func (h *Holder) CacheClearAll() int {
	if h.cache == nil {
		return 0
	}
	return h.cache.ClearAll()
}

// CacheKeys 返回所有缓存键。无缓存时返回 nil。
func (h *Holder) CacheKeys() []string {
	if h.cache == nil {
		return nil
	}
	return h.cache.Keys()
}

// CacheGet 获取指定缓存键的值。无缓存或未命中时返回 ("", false)。
func (h *Holder) CacheGet(key string) (string, bool) {
	if h.cache == nil {
		return "", false
	}
	return h.cache.Get(key)
}
