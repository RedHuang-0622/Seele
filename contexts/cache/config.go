// Package cache 提供缓存存储的接口与文件系统实现。
//
// 设计目标：
//   - 零外部依赖（仅标准库）
//   - 内容可寻址存储（SHA256 去重）
//   - TTL 过期、命中统计、边界控制
//
// 使用方（seelectx.Holder）通过 cache.Provider 接口引用本包，
// 不直接依赖 FileCache 具体实现。
package cache

import "time"

// ── Config ─────────────────────────────────────────────────────────

// Config 控制缓存模块的各项阈值与行为。
// 零值字段使用 DefaultConfig() 中的默认值。
type Config struct {
	// BaseDir 缓存文件存储根目录。默认 ".seele/cache/"。
	BaseDir string

	// DefaultTTL 缓存项的默认过期时间。默认 5 分钟。
	DefaultTTL time.Duration

	// MaxEntries 缓存最大条目数。达到上限时拒绝新写入。0 表示无限制。默认 1000。
	MaxEntries int

	// MaxEntrySize 单条缓存内容的最大字节数。超过上限时拒绝写入。0 表示无限制。默认 1MB。
	MaxEntrySize int64
}

// DefaultConfig 返回推荐的缓存配置。
func DefaultConfig() Config {
	return Config{
		BaseDir:      ".seele/cache/",
		DefaultTTL:   5 * time.Minute,
		MaxEntries:   1000,
		MaxEntrySize: 1 * 1024 * 1024, // 1MB
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c Config) Effective() Config {
	d := DefaultConfig()
	if c.BaseDir == "" {
		c.BaseDir = d.BaseDir
	}
	if c.DefaultTTL <= 0 {
		c.DefaultTTL = d.DefaultTTL
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = d.MaxEntries
	}
	if c.MaxEntrySize <= 0 {
		c.MaxEntrySize = d.MaxEntrySize
	}
	return c
}
