package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

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

// ── Provider 接口 ───────────────────────────────────────────────────

// Provider 是缓存存储的抽象接口。
//
// 当前实现：
//   - FileCache — 基于文件系统的本地缓存
//
// 所有方法在实现不可用（nil Provider）时都应安全返回零值。
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

// ── FileCache 实现 ──────────────────────────────────────────────────

// FileCache 是基于文件系统的 Provider 实现。
//
// 存储策略：
//   - 内容文件：baseDir / sha256(content) / content   — 内容去重
//   - 元数据文件：baseDir / sha256(content) / meta.json — 过期时间 + 命中次数
//   - 键索引：内存 sync.Map（key → {hash, 元数据, 文件路径}），O(1) 查询
//
// 并发安全：
//   - 读路径：atomic 统计 + sync.Map 索引
//   - 写路径：indexMu 锁保护元数据并发读写
type FileCache struct {
	cfg     Config
	baseDir string

	index     sync.Map    // key → *cacheIndexEntry
	indexMu   sync.Mutex  // 保护索引批量写入 + 元数据并发读写
	hits      atomic.Int64
	misses    atomic.Int64
	totalSize atomic.Int64
}

// cacheIndexEntry 是 FileCache 内存索引中的条目。
type cacheIndexEntry struct {
	meta     Entry
	hash     string // SHA256 十六进制，同时也是内容子目录名
	filePath string // 内容文件的完整路径
}

// NewFileCache 创建基于文件系统的缓存。
// cfg 的零值字段使用 DefaultConfig() 填充。
func NewFileCache(cfg Config) (*FileCache, error) {
	cfg = cfg.Effective()
	baseDir := cfg.BaseDir
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("cache: create base dir %q: %w", baseDir, err)
	}

	c := &FileCache{
		cfg:     cfg,
		baseDir: baseDir,
	}

	// 重建内存索引：扫描已有缓存文件
	if err := c.rebuildIndex(); err != nil {
		return nil, fmt.Errorf("cache: rebuild index: %w", err)
	}

	return c, nil
}

// ── 实现 Provider ───────────────────────────────────────────────────

// Get 获取缓存值。自动检查 TTL 过期，过期视为未命中并删除。
func (c *FileCache) Get(key string) (string, bool) {
	val, ok := c.index.Load(key)
	if !ok {
		c.misses.Add(1)
		return "", false
	}

	entry := val.(*cacheIndexEntry)

	// TTL 过期检查
	if !entry.meta.ExpiresAt.IsZero() && time.Now().After(entry.meta.ExpiresAt) {
		c.index.Delete(key)
		c.misses.Add(1)
		_ = os.Remove(entry.filePath)
		_ = os.Remove(c.metaPath(entry.hash))
		return "", false
	}

	// 读取内容文件
	data, err := os.ReadFile(entry.filePath)
	if err != nil {
		c.index.Delete(key)
		c.misses.Add(1)
		return "", false
	}

	// 命中计数更新（indexMu 保护 GetEntry/List 的并发读取）
	c.indexMu.Lock()
	entry.meta.HitCount++
	meta := entry.meta // 在锁内快照
	c.indexMu.Unlock()
	c.saveMeta(entry.hash, meta)
	c.hits.Add(1)

	return string(data), true
}

// GetEntry 获取缓存条目元数据。
func (c *FileCache) GetEntry(key string) (*Entry, bool) {
	val, ok := c.index.Load(key)
	if !ok {
		c.misses.Add(1)
		return nil, false
	}

	entry := val.(*cacheIndexEntry)

	// TTL 过期检查
	if !entry.meta.ExpiresAt.IsZero() && time.Now().After(entry.meta.ExpiresAt) {
		c.index.Delete(key)
		_ = os.Remove(entry.filePath)
		_ = os.Remove(c.metaPath(entry.hash))
		c.misses.Add(1)
		return nil, false
	}

	c.indexMu.Lock()
	cp := entry.meta
	c.indexMu.Unlock()
	return &cp, true
}

// Set 使用默认 TTL 存储缓存值。
func (c *FileCache) Set(key, value string) *Entry {
	return c.SetWithTTL(key, value, c.cfg.DefaultTTL)
}

// SetWithTTL 使用指定 TTL 存储缓存值。
func (c *FileCache) SetWithTTL(key, value string, ttl time.Duration) *Entry {
	// 大小检查
	if c.cfg.MaxEntrySize > 0 && int64(len(value)) > c.cfg.MaxEntrySize {
		return nil
	}

	// 条目数检查
	if c.cfg.MaxEntries > 0 {
		count := 0
		c.index.Range(func(_, _ any) bool {
			count++
			return count < c.cfg.MaxEntries
		})
		if count >= c.cfg.MaxEntries {
			return nil
		}
	}

	// 计算内容哈希（SHA256 用于去重 + 子目录名）
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
	contentDir := filepath.Join(c.baseDir, hash)

	if err := os.MkdirAll(contentDir, 0755); err != nil {
		return nil
	}

	// 写入内容文件
	contentPath := filepath.Join(contentDir, "content")
	if err := os.WriteFile(contentPath, []byte(value), 0644); err != nil {
		return nil
	}

	// 构造元数据
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	meta := Entry{
		Key:         key,
		CreatedAt:   time.Now(),
		ExpiresAt:   expiresAt,
		HitCount:    0,
		SizeBytes:   int64(len(value)),
		ContentHash: hash,
	}

	// 写入元数据文件
	c.saveMeta(hash, meta)

	// 更新内存索引
	entry := &cacheIndexEntry{
		meta:     meta,
		hash:     hash,
		filePath: contentPath,
	}
	c.index.Store(key, entry)
	c.totalSize.Add(int64(len(value)))

	return &meta
}

// Delete 删除指定缓存键。
func (c *FileCache) Delete(key string) bool {
	val, loaded := c.index.LoadAndDelete(key)
	if !loaded {
		return false
	}

	entry := val.(*cacheIndexEntry)
	c.totalSize.Add(-entry.meta.SizeBytes)

	// 检查是否还有其他 key 引用同一哈希
	hasOtherRef := false
	c.index.Range(func(k, v any) bool {
		if k != key && v.(*cacheIndexEntry).hash == entry.hash {
			hasOtherRef = true
			return false
		}
		return true
	})

	// 无其他引用时删除内容文件
	if !hasOtherRef {
		contentDir := filepath.Dir(entry.filePath)
		_ = os.Remove(entry.filePath)
		_ = os.Remove(c.metaPath(entry.hash))
		_ = os.Remove(contentDir)
	}

	return true
}

// ClearByPrefix 删除所有匹配前缀的缓存键。
func (c *FileCache) ClearByPrefix(prefix string) int {
	if prefix == "" {
		return 0
	}

	var toDelete []string
	c.index.Range(func(key, _ any) bool {
		k := key.(string)
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			toDelete = append(toDelete, k)
		}
		return true
	})

	for _, k := range toDelete {
		c.Delete(k)
	}

	return len(toDelete)
}

// ClearAll 清空所有缓存。
func (c *FileCache) ClearAll() int {
	var count int
	c.index.Range(func(key, _ any) bool {
		count++
		return true
	})

	_ = os.RemoveAll(c.baseDir)
	_ = os.MkdirAll(c.baseDir, 0755)

	c.indexMu.Lock()
	c.index = sync.Map{}
	c.totalSize.Store(0)
	c.indexMu.Unlock()

	return count
}

// Keys 返回所有缓存键的副本。
func (c *FileCache) Keys() []string {
	var keys []string
	c.index.Range(func(key, _ any) bool {
		keys = append(keys, key.(string))
		return true
	})
	return keys
}

// List 返回所有缓存条目元数据的副本。
func (c *FileCache) List() []Entry {
	var entries []Entry
	c.indexMu.Lock()
	c.index.Range(func(_, val any) bool {
		entry := val.(*cacheIndexEntry)
		entries = append(entries, entry.meta)
		return true
	})
	c.indexMu.Unlock()
	return entries
}

// Stats 返回缓存统计信息。
func (c *FileCache) Stats() Stats {
	var entries int
	c.index.Range(func(_, _ any) bool {
		entries++
		return true
	})

	hits := c.hits.Load()
	misses := c.misses.Load()
	total := hits + misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	return Stats{
		Entries:        entries,
		TotalSizeBytes: c.totalSize.Load(),
		HitCount:       hits,
		MissCount:      misses,
		HitRate:        hitRate,
	}
}

// ── 内部方法 ────────────────────────────────────────────────────────

// rebuildIndex 扫描缓存目录重建内存索引。
func (c *FileCache) rebuildIndex() error {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()

	entries, err := os.ReadDir(c.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, dir := range entries {
		if !dir.IsDir() {
			continue
		}
		hash := dir.Name()
		contentPath := filepath.Join(c.baseDir, hash, "content")
		metaPath := c.metaPath(hash)

		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var meta Entry
		if err := json.Unmarshal(metaData, &meta); err != nil {
			continue
		}

		// TTL 过期检查
		if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
			_ = os.Remove(contentPath)
			_ = os.Remove(metaPath)
			_ = os.Remove(filepath.Join(c.baseDir, hash))
			continue
		}

		entry := &cacheIndexEntry{
			meta:     meta,
			hash:     hash,
			filePath: contentPath,
		}
		c.index.Store(meta.Key, entry)
		c.totalSize.Add(meta.SizeBytes)
	}

	return nil
}

// metaPath 返回哈希对应的元数据文件路径。
func (c *FileCache) metaPath(hash string) string {
	return filepath.Join(c.baseDir, hash, "meta.json")
}

// saveMeta 将元数据写入文件。
func (c *FileCache) saveMeta(hash string, meta Entry) {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	metaPath := c.metaPath(hash)
	_ = os.MkdirAll(filepath.Dir(metaPath), 0755)
	_ = os.WriteFile(metaPath, data, 0644)
}
