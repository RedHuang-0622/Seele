// Package seelectx (contexts) 是 Seele 的对话会话管理核心包。
//
// 架构：
//
//	contexts/                  [package seelectx] — 集成层，对外 API
//	├── cache/                 [package cache]    — 缓存子模块（接口 + FileCache）
//	├── history/               [package history]  — 上下文预算子模块（压缩/截断/估算）
//	├── react/                 [package react]    — ReAct 循环策略（同步/流式）
//	│
//	├── holder.go              — Holder 会话管理器
//	├── chat.go                — chatLoop 模板方法（ReAct 循环）
//	├── dispatch.go            — 工具调度 + 审批流
//	├── interface.go           — ToolDispatcher + ApprovalCallback
//	├── session_config.go      — SessionConfig
//	├── storage.go             — LocalStorage + FileStorage
//	└── cache_tool.go          — RegisterCacheTools（装配件模式桥接）
//
// 命名说明：
//
//	包名 seelectx 沿用 v0.4 之前的命名，避免与标准库 "context" 混淆。
//	目录名 contexts/ 作为导入路径，明确表达"多个上下文"的语义。
package seelectx

import (
	"github.com/RedHuang-0622/Seele/contexts/cache"
	"github.com/RedHuang-0622/Seele/contexts/history"
	"github.com/RedHuang-0622/Seele/contexts/react"
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

// ContextConfig 委托到 history.Config
type ContextConfig = history.Config

// DefaultContextConfig 委托到 history.DefaultConfig
var DefaultContextConfig = history.DefaultConfig

// ── ReAct 类型别名 ───────────────────────────────────────────────

// CompletionStrategy 委托到 react.CompletionStrategy
type CompletionStrategy = react.CompletionStrategy

// CompletionResult 委托到 react.CompletionResult
type CompletionResult = react.CompletionResult

// SyncStrategy 委托到 react.SyncStrategy
type SyncStrategy = react.SyncStrategy

// StreamStrategy 委托到 react.StreamStrategy
type StreamStrategy = react.StreamStrategy

// StreamEventStrategy 委托到 react.StreamEventStrategy
type StreamEventStrategy = react.StreamEventStrategy

// ── 历史函数委托（保持向后兼容）─────────────────────────────────

var (
	EstimateTokens         = history.EstimateTokens
	EstimateMessageTokens  = history.EstimateMessageTokens
	EstimateHistoryTokens  = history.EstimateHistoryTokens
	TruncateToolResult     = history.TruncateToolResult
	TrimHistory            = history.TrimHistory
	NeedCompression        = history.NeedCompression
	CompressHistory        = history.CompressHistory
)
