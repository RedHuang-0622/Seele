// Package tool_holder 管理所有工具 Provider 的注册、聚合和调度。
//
// Holder 不持有 LLM 客户端，不创建会话。它是纯粹的"工具层"。
// 并发安全：所有对 providers 的读写通过 mu 保护。
package tool_holder

import (
	"sync"
	"time"

	"github.com/sukasukasuka123/Seele/provider"
)

// Holder 是工具注册与调度的中枢。
//
// 职责：
//   - 注册/注销 ToolProvider（HubProvider、MCPProvider 等）
//   - 聚合所有 provider 的工具列表
//   - 按工具名路由 dispatch 到正确的 provider
//
// Holder 不感知任何具体协议（gRPC / MCP / HTTP），协议适配由 provider 层完成。
type Holder struct {
	mu        sync.RWMutex
	providers []provider.ToolProvider // 按注册顺序排列，dispatch 时按序查找

	// DispatchRetries 瞬时错误最大重试次数，默认 3。
	DispatchRetries int
	// DispatchRetryDelay 重试间隔，默认 2s。
	DispatchRetryDelay time.Duration
}

// New 创建一个空的 Holder。重试参数使用默认值（3 次 / 2s）。
func New() *Holder {
	return &Holder{
		DispatchRetries:    3,
		DispatchRetryDelay: 2 * time.Second,
	}
}
