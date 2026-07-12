package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// ResponseCache 是 LLM 响应去重场景的 Provider 实现。
//
// 与 FileCache 实现同一 Provider 接口，属同一策略体系：
//   - FileCache    → 文件系统缓存（通用存储）
//   - ResponseCache → LLM 回复缓存（3s TTL 窗口 + "llm:" 键前缀）
//
// 内部委托一个底层 Provider（通常是 FileCache）做实际存储，
// 自己负责键前缀（"llm:"）和默认 TTL（3s）。
// 键值存储的是 JSON 化的 respCacheEntry（LLM 回复内容）。
type ResponseCache struct {
	inner Provider
}

// respCacheEntry 是缓存的 LLM 回复值。
type respCacheEntry struct {
	Content          *string          `json:"content,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`
}

// NewResponseCache 创建 LLM 响应缓存策略。
// inner 为 nil 时所有方法安全返回零值。
func NewResponseCache(inner Provider) *ResponseCache {
	return &ResponseCache{inner: inner}
}

// ── Provider 实现 ──────────────────────────────────────────────────

func (rc *ResponseCache) Get(key string) (string, bool) {
	if rc.inner == nil {
		return "", false
	}
	return rc.inner.Get("llm:" + key)
}

func (rc *ResponseCache) GetEntry(key string) (*Entry, bool) {
	if rc.inner == nil {
		return nil, false
	}
	return rc.inner.GetEntry("llm:" + key)
}

// Set 使用默认 TTL（3s）存储缓存值。
func (rc *ResponseCache) Set(key, value string) *Entry {
	return rc.SetWithTTL(key, value, 3*time.Second)
}

// SetWithTTL 存储缓存值，固定 3s TTL（忽略传入的 ttl 参数）。
// 这是响应去重的核心策略：短 TTL 避免同输入重复调 LLM。
func (rc *ResponseCache) SetWithTTL(key, value string, _ time.Duration) *Entry {
	if rc.inner == nil {
		return nil
	}
	return rc.inner.SetWithTTL("llm:"+key, value, 3*time.Second)
}

func (rc *ResponseCache) Delete(key string) bool {
	if rc.inner == nil {
		return false
	}
	return rc.inner.Delete("llm:" + key)
}

func (rc *ResponseCache) ClearByPrefix(prefix string) int {
	if rc.inner == nil {
		return 0
	}
	return rc.inner.ClearByPrefix("llm:" + prefix)
}

func (rc *ResponseCache) ClearAll() int {
	if rc.inner == nil {
		return 0
	}
	return rc.inner.ClearByPrefix("llm:")
}

func (rc *ResponseCache) Keys() []string {
	if rc.inner == nil {
		return nil
	}
	all := rc.inner.Keys()
	var out []string
	for _, k := range all {
		if len(k) > 4 && k[:4] == "llm:" {
			out = append(out, k[4:])
		}
	}
	return out
}

func (rc *ResponseCache) List() []Entry {
	if rc.inner == nil {
		return nil
	}
	all := rc.inner.List()
	var out []Entry
	for _, e := range all {
		if len(e.Key) > 4 && e.Key[:4] == "llm:" {
			e.Key = e.Key[4:]
			out = append(out, e)
		}
	}
	return out
}

func (rc *ResponseCache) Stats() Stats {
	// 仅返回 llm: 前缀范围内的统计
	entries := rc.List()
	totalSize := int64(0)
	for _, e := range entries {
		totalSize += e.SizeBytes
	}
	return Stats{
		Entries:        len(entries),
		TotalSizeBytes: totalSize,
	}
}

// ── 高层便利方法 ───────────────────────────────────────────────────
// 以下方法不属 Provider 接口，是 ResponseCache 场景的语义增强。

// TryGet 尝试从缓存获取 LLM 回复。
// 与普通 Get 的区别：返回值是反序列化的 types.Message。
func (rc *ResponseCache) TryGet(history []types.Message, tools []types.Tool, model string) (*types.Message, bool) {
	if rc.inner == nil {
		return nil, false
	}
	key := rc.key(history, tools, model)
	cached, ok := rc.inner.Get("llm:" + key) // 用 inner 避免二次前缀
	if !ok || cached == "" {
		return nil, false
	}
	var entry respCacheEntry
	if err := json.Unmarshal([]byte(cached), &entry); err != nil {
		return nil, false
	}
	return &types.Message{
		Role:             "assistant",
		Content:          entry.Content,
		ReasoningContent: entry.ReasoningContent,
		ToolCalls:        entry.ToolCalls,
	}, true
}

// Set 将 LLM 回复写入缓存。
// 与普通 Set 的区别：自动计算键，入参为 domain 类型。
func (rc *ResponseCache) SetResponse(history []types.Message, tools []types.Tool, model string, msg *types.Message) {
	if rc.inner == nil || msg == nil {
		return
	}
	key := rc.key(history, tools, model)
	entry := respCacheEntry{
		Content:          msg.Content,
		ReasoningContent: msg.ReasoningContent,
		ToolCalls:        msg.ToolCalls,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	rc.inner.SetWithTTL("llm:"+key, string(data), 3*time.Second)
}

// Enabled 返回缓存是否可用（inner 非空）。
func (rc *ResponseCache) Enabled() bool {
	return rc.inner != nil
}

// key 计算缓存键。
func (rc *ResponseCache) key(history []types.Message, tools []types.Tool, model string) string {
	h := sha256.New()
	for _, m := range history {
		b, _ := json.Marshal(m)
		h.Write(b)
	}
	for _, t := range tools {
		b, _ := json.Marshal(t)
		h.Write(b)
	}
	h.Write([]byte(model))
	return hex.EncodeToString(h.Sum(nil))
}
