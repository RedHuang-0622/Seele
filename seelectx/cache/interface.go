package cache

import "time"

// ── 数据类型 ────────────────────────────────────────────────────────

// Entry 缓存条目的元数据。
type Entry struct {
	Key         string    `json:"key"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	HitCount    int64     `json:"hit_count"`
	SizeBytes   int64     `json:"size_bytes"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// Stats 缓存统计信息。
type Stats struct {
	Entries        int     `json:"entries"`
	TotalSizeBytes int64   `json:"total_size_bytes"`
	HitCount       int64   `json:"hit_count"`
	MissCount      int64   `json:"miss_count"`
	HitRate        float64 `json:"hit_rate"`
}

// Provider 是缓存存储的抽象接口。
//
// 实现（同一策略体系）：
//   - FileCache     — 基于文件系统的通用缓存
//   - ResponseCache — LLM 响应去重缓存（3s TTL + "llm:" 键前缀）
//
// 所有方法在实现不可用时应安全返回零值。
type Provider interface {
	// Get 获取缓存值。ok=true 表示命中。
	Get(key string) (value string, ok bool)

	// GetEntry 获取缓存条目元数据。ok=true 表示命中。
	GetEntry(key string) (entry *Entry, ok bool)

	// Set 使用默认 TTL 存储缓存值。
	Set(key, value string) *Entry

	// SetWithTTL 使用指定 TTL 存储缓存值。ttl <= 0 表示永不过期。
	SetWithTTL(key, value string, ttl time.Duration) *Entry

	// Delete 删除指定缓存键。
	Delete(key string) bool

	// ClearByPrefix 删除所有匹配前缀的缓存键。返回删除的条目数。
	ClearByPrefix(prefix string) int

	// ClearAll 清空所有缓存。返回删除的条目数。
	ClearAll() int

	// Keys 返回所有缓存键的列表。
	Keys() []string

	// List 返回所有缓存条目元数据的副本。
	List() []Entry

	// Stats 返回缓存统计信息。
	Stats() Stats
}
