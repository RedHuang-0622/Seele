package Seele

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Runtime 是工具调度的中枢，负责：
//   - 注册和管理多个 ToolProvider（HubProvider、MCPProvider 等）
//   - 聚合所有 provider 的工具列表（供 Agent 调用 LLM 时使用）
//   - 按工具名路由 dispatch 到正确的 provider
//   - 创建 Agent 实例
//
// Runtime 本身不感知任何具体协议（gRPC / MCP / HTTP）。
// 并发安全：所有对 providers 的读写都通过 mu 保护。
type Runtime struct {
	llm       *chatClient
	mu        sync.RWMutex
	providers []ToolProvider // 按注册顺序排列，dispatch 时按序查找
}

// NewRuntime 创建 Runtime。
// 至少需要一个有效的 LLMConfig，provider 可在创建后通过 Register 注册。
func NewRuntime(llmCfg LLMConfig) (*Runtime, error) {
	if llmCfg.BaseURL == "" || llmCfg.Model == "" {
		return nil, fmt.Errorf("Runtime: LLMConfig requires BaseURL and Model")
	}
	return &Runtime{
		llm: newChatClient(llmCfg),
	}, nil
}

// ── Provider 管理 ────────────────────────────────────────────────

// Register 注册一个 ToolProvider。
// 同名 provider 会追加（不去重），调用方负责保证唯一性。
// 注册顺序即 dispatch 的路由优先级：先注册的先匹配。
func (r *Runtime) Register(p ToolProvider) {
	r.mu.Lock()
	r.providers = append(r.providers, p)
	r.mu.Unlock()
	log.Printf("[Runtime] registered provider=%q", p.ProviderName())
}

// Unregister 按名称移除 provider（全部同名的都移除）。
func (r *Runtime) Unregister(name string) {
	r.mu.Lock()
	filtered := r.providers[:0]
	for _, p := range r.providers {
		if p.ProviderName() != name {
			filtered = append(filtered, p)
		}
	}
	r.providers = filtered
	r.mu.Unlock()
	log.Printf("[Runtime] unregistered provider=%q", name)
}

// ── Agent 工厂 ───────────────────────────────────────────────────

// NewAgent 创建绑定到本 Runtime 的 Agent。
// systemPrompt 为空时不注入 system 消息。
func (r *Runtime) NewAgent(systemPrompt string, loopTimes int) *Agent {
	if loopTimes == 0 {
		loopTimes = 4 // 默认值
	}
	a := &Agent{
		runtime:   r,
		sessionID: fmt.Sprintf("sess_%d", time.Now().UnixNano()),
		maxLoops:  loopTimes,
	}
	if systemPrompt != "" {
		a.history = []Message{{Role: "system", Content: &systemPrompt}}
	}
	return a
}

// ── Agent 内部调用（不对外暴露）──────────────────────────────────

// tools 聚合所有已注册 provider 的工具列表。
// 每次调用都实时读取，支持热更新（如 MCP Server 动态增减工具）。
func (r *Runtime) tools() []Tool {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()

	var result []Tool
	for _, p := range providers {
		result = append(result, p.Tools()...)
	}
	return result
}

// dispatch 根据工具名路由到对应 provider 并执行。
// 路由规则：按注册顺序，找到第一个 HasTool 返回 true 的 provider。
func (r *Runtime) dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	r.mu.RLock()
	providers := r.providers
	r.mu.RUnlock()

	for _, p := range providers {
		if p.HasTool(name) {
			return p.Dispatch(ctx, name, argsJSON)
		}
	}
	return "", fmt.Errorf("Runtime.dispatch: tool %q not found in any provider", name)
}
