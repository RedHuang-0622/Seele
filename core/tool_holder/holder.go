// Package tool_holder 管理所有工具 Provider 的注册、聚合和调度。
//
// Holder 不持有 LLM 客户端，不创建会话。它是纯粹的"工具层"。
//
// 并发模型（v0.4 优化）：
//   - 读路径（Tools / Dispatch）：通过 atomic.Pointer 获取 holderState 快照，零锁开销。
//   - 写路径（Register / Unregister）：mu 序列化 providers 修改 + 重建 holderState + 原子替换。
//
// 策略模式：所有工具统一为 ToolEntry{Definition + Handler}，
// Dispatch 通过 O(1) map lookup 定位 handler，不感知底层协议。
package tool_holder

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RedHuang-0622/Seele/provider"
	types "github.com/RedHuang-0622/Seele/types"
)

// ── holderState：不可变快照 ──────────────────────────────────────────

// holderState 是 Holder 的工具注册状态的不可变快照。
// 写路径分配新 state → 原子替换指针；读路径 Load 后无锁访问。
type holderState struct {
	toolMap  map[string]provider.ToolEntry // name → entry（含 Handler，供 Dispatch）
	toolList []types.Tool                  // 预过滤的工具定义列表（无 _ 前缀，无 Handler）
}

// ── Holder ─────────────────────────────────────────────────────────

// Holder 是工具注册与调度的中枢。
//
// 职责：
//   - 注册/注销 ToolProvider（HubProvider、MCPProvider、InlineProvider 等）
//   - 聚合所有 provider 的工具列表（统一 _ 前缀过滤）
//   - O(1) map 路由 dispatch 到正确的 Handler（策略模式）
type Holder struct {
	mu        sync.Mutex // 仅序列化 providers 写入 + state 重建
	providers []provider.ToolProvider
	state     atomic.Pointer[holderState] // 读路径零锁

	DispatchRetries    int
	DispatchRetryDelay time.Duration
}

// New 创建一个空的 Holder。使用默认配置。
func New() *Holder {
	return NewWithConfig(DefaultHolderConfig())
}

// NewWithConfig 使用指定配置创建 Holder。
func NewWithConfig(cfg HolderConfig) *Holder {
	cfg = cfg.Effective()
	h := &Holder{
		providers:          make([]provider.ToolProvider, 0),
		DispatchRetries:    cfg.DispatchRetries,
		DispatchRetryDelay: cfg.DispatchRetryDelay,
	}
	h.state.Store(&holderState{
		toolMap:  make(map[string]provider.ToolEntry),
		toolList: make([]types.Tool, 0),
	})
	return h
}

// ── 内部辅助 ──────────────────────────────────────────────────────

// rebuildLocked 遍历所有 provider 构造新的 holderState 并原子替换。
// 调用方必须持有 h.mu。
func (h *Holder) rebuildLocked() {
	toolMap := make(map[string]provider.ToolEntry)
	for _, p := range h.providers {
		for _, entry := range p.Tools() {
			name := entry.Definition.Function.Name
			if _, exists := toolMap[name]; !exists {
				toolMap[name] = entry
			}
		}
	}

	// 预过滤 _ 前缀内部工具
	toolList := make([]types.Tool, 0, len(toolMap))
	for name, entry := range toolMap {
		if strings.HasPrefix(name, "_") {
			continue
		}
		toolList = append(toolList, entry.Definition)
	}

	h.state.Store(&holderState{toolMap: toolMap, toolList: toolList})
}
