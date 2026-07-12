// Package seelectx (contexts) 是 Seele 的对话会话管理核心包。
//
// 架构：
//
//	contexts/                  [package seelectx] — 集成层，对外 API
//	├── cache/                 [package cache]    — 缓存子模块（接口 + FileCache）
//	├── ctx_manager/           [package ctx_manager]  — 上下文预算子模块（压缩/截断/估算）
//	│
//	├── storage.go             — LocalStorage + FileStorage
//	└── cache_tool.go          — RegisterCacheTools（装配件模式桥接）
package seelectx

import (
	"github.com/RedHuang-0622/Seele/seelectx/cache"
	"github.com/RedHuang-0622/Seele/seelectx/ctx_manager"
)

// ── 缓存类型别名 ─────────────────────────────────────────────────

// CacheProvider 委托到 cache.Provider
type CacheProvider = cache.Provider

// CacheConfig 委托到 cache.Config
type CacheConfig = cache.Config

// CacheEntry 委托到 cache.Entry
type CacheEntry = cache.Entry

// CacheStats 委托到 cache.Stats
type CacheStats = cache.Stats

// DefaultCacheConfig 委托到 cache.DefaultConfig
var DefaultCacheConfig = cache.DefaultConfig

// NewFileCache 委托到 cache.NewFileCache
var NewFileCache = cache.NewFileCache

// ── 上下文预算类型别名 ───────────────────────────────────────────

// ContextConfig 委托到 ctx_manager.Config
type ContextConfig = ctx_manager.Config

// DefaultContextConfig 委托到 ctx_manager.DefaultConfig
var DefaultContextConfig = ctx_manager.DefaultConfig

// ── 历史函数委托（保持向后兼容）─────────────────────────────────

var (
	EstimateTokens         = ctx_manager.EstimateTokens
	EstimateMessageTokens  = ctx_manager.EstimateMessageTokens
	EstimateHistoryTokens  = ctx_manager.EstimateHistoryTokens
	TruncateToolResult     = ctx_manager.TruncateToolResult
	TrimHistory            = ctx_manager.TrimHistory
	NeedCompression        = ctx_manager.NeedCompression
	CompressHistory        = ctx_manager.CompressHistory
)
