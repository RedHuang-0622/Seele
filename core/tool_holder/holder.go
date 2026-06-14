// Package tool_holder 管理所有工具 Provider 的注册、聚合和调度。
//
// Holder 不持有 LLM 客户端，不创建会话。它是纯粹的"工具层"。
// 并发安全：所有对 providers / toolMap 的读写通过 mu 保护。
//
// 策略模式：所有工具统一为 ToolEntry{Definition + Handler}，
// Dispatch 通过 O(1) map lookup 定位 handler，不感知底层协议。
package tool_holder

import (
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/provider"
)

// Holder 是工具注册与调度的中枢。
//
// 职责：
//   - 注册/注销 ToolProvider（HubProvider、MCPProvider、InlineProvider 等）
//   - 聚合所有 provider 的工具列表（统一 _ 前缀过滤）
//   - O(1) map 路由 dispatch 到正确的 Handler（策略模式）
type Holder struct {
	mu        sync.RWMutex
	providers []provider.ToolProvider
	toolMap   map[string]provider.ToolEntry // name → entry（含 _ 前缀内部工具）

	DispatchRetries    int
	DispatchRetryDelay time.Duration
}

// New 创建一个空的 Holder。重试参数使用默认值（3 次 / 2s）。
func New() *Holder {
	return &Holder{
		providers:          make([]provider.ToolProvider, 0),
		toolMap:            make(map[string]provider.ToolEntry),
		DispatchRetries:    3,
		DispatchRetryDelay: 2 * time.Second,
	}
}

// ── 内部辅助 ──────────────────────────────────────────────────────

// rebuildLocked 遍历所有 provider 重建 toolMap。调用方必须持有写锁。
func (h *Holder) rebuildLocked() {
	h.toolMap = make(map[string]provider.ToolEntry, len(h.toolMap))
	for _, p := range h.providers {
		h.mergeLocked(p)
	}
}

// mergeLocked 将一个 provider 的所有工具并入 toolMap。调用方必须持有写锁。
// 同名工具：先注册的优先，后注册的忽略（与旧注册顺序路由行为一致）。
func (h *Holder) mergeLocked(p provider.ToolProvider) {
	for _, entry := range p.Tools() {
		name := entry.Definition.Function.Name
		if _, exists := h.toolMap[name]; !exists {
			h.toolMap[name] = entry
		}
	}
}
