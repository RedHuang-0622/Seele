// Package holder 管理工具注册、调度与插件装配件。
package holder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

type holderState struct {
	toolMap  map[string]interfaces.ToolEntry
	toolList []types.Tool
}

// Holder 是工具注册与调度的中枢。
type Holder struct {
	mu        sync.Mutex
	providers []interfaces.ToolProvider
	state     atomic.Pointer[holderState]

	DispatchRetries    int
	DispatchRetryDelay time.Duration
	ToolCallTimeout    time.Duration // 单次工具调用超时

	pluginMgr *PluginManager
}

// New 创建使用默认配置的 Holder。
func New() *Holder {
	return NewWithConfig(DefaultHolderConfig())
}

// NewWithConfig 使用指定配置创建 Holder。
func NewWithConfig(cfg HolderConfig) *Holder {
	cfg = cfg.Effective()
	h := &Holder{
		providers:          make([]interfaces.ToolProvider, 0),
		DispatchRetries:    cfg.DispatchRetries,
		DispatchRetryDelay: cfg.DispatchRetryDelay,
		ToolCallTimeout:    cfg.ToolCallTimeout,
	}
	h.state.Store(&holderState{
		toolMap:  make(map[string]interfaces.ToolEntry),
		toolList: make([]types.Tool, 0),
	})
	return h
}

func (h *Holder) rebuildLocked() {
	toolMap := make(map[string]interfaces.ToolEntry)
	for _, p := range h.providers {
		for _, entry := range p.Tools() {
			name := entry.Definition.Function.Name
			if _, exists := toolMap[name]; !exists {
				toolMap[name] = entry
			}
		}
	}
	toolList := make([]types.Tool, 0, len(toolMap))
	for name, entry := range toolMap {
		if strings.HasPrefix(name, "_") {
			continue
		}
		toolList = append(toolList, entry.Definition)
	}
	h.state.Store(&holderState{toolMap: toolMap, toolList: toolList})
}

// Register 注册一个 ToolProvider。
func (h *Holder) Register(p interfaces.ToolProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers = append(h.providers, p)
	h.rebuildLocked()
}

// Unregister 按名称移除 provider。
func (h *Holder) Unregister(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	filtered := h.providers[:0]
	for _, p := range h.providers {
		if p.ProviderName() != name {
			filtered = append(filtered, p)
		}
	}
	h.providers = filtered
	h.rebuildLocked()
}

// Tools 返回所有已注册工具的 LLM 可见定义列表。
func (h *Holder) Tools() []types.Tool {
	return h.state.Load().toolList
}

// Dispatch 通过 map 查找 handler 并执行。瞬时错误自动重试。
//
// 超时策略：
//   - 若 ctx 已有 Deadline 且早于 ToolCallTimeout，优先使用 ctx 的 Deadline
//   - 否则用 ToolCallTimeout 派生超时 context
//   - 各 handler 的 Execute 必须尊重传入 context 的 Deadline（review P0 修复）
func (h *Holder) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	st := h.state.Load()
	entry, ok := st.toolMap[name]
	if !ok {
		return "", fmt.Errorf("tool.dispatch: tool %q not found", name)
	}

	// 派生超时 context：优先用配置的 ToolCallTimeout
	deadline, hasDeadline := ctx.Deadline()
	callCtx := ctx
	if h.DispatchRetries > 0 {
		timeout := h.DispatchRetryDelay * time.Duration(h.DispatchRetries)
		if h.ToolCallTimeout > timeout {
			timeout = h.ToolCallTimeout
		}
		if !hasDeadline || time.Until(deadline) > timeout {
			var cancel context.CancelFunc
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}

	var lastErr error
	for attempt := 0; attempt < h.DispatchRetries; attempt++ {
		result, err := entry.Handler.Execute(callCtx, argsJSON)
		if err == nil {
			return result, nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("tool.dispatch: tool %q timeout after %v: %w",
				name, h.ToolCallTimeout, err)
		}
		if !errors.Is(err, interfaces.ErrToolUnavailable) {
			return "", err
		}
		lastErr = err
		if attempt < h.DispatchRetries-1 {
			time.Sleep(h.DispatchRetryDelay)
		}
	}
	return "", fmt.Errorf("tool.dispatch: tool %q unavailable after %d retries: %w",
		name, h.DispatchRetries, lastErr)
}

// RegisterInline 直接注册一个 Go 函数工具。
// optOutput 可选，指定输出 struct 的 SchemaOf，nil 表示无结构化输出约束。
func (h *Holder) RegisterInline(name, desc string, inputSchema map[string]interface{}, fn func(ctx context.Context, argsJSON string) (string, error), optOutput ...map[string]interface{}) {
	ip := h.getOrCreateInline()
	h.mu.Lock()
	entry := interfaces.ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name,
				Description: desc,
				Parameters:  inputSchema,
			},
		},
		Handler: &inlineHandler{Fn: fn},
	}
	if len(optOutput) > 0 && optOutput[0] != nil {
		entry.OutputSchema = optOutput[0]
	}
	ip.tools[name] = entry
	h.rebuildLocked()
	h.mu.Unlock()
}

// ── Plugin 管理 ─────────────────────────────────────────────────

func (h *Holder) WithPluginManager(pm *PluginManager) *Holder {
	h.pluginMgr = pm
	return h
}

func (h *Holder) Plugin() *PluginManager      { return h.pluginMgr }
func (h *Holder) IsPluginActive() bool         { return h.pluginMgr != nil && h.pluginMgr.IsPluginActive() }
func (h *Holder) ActivePlugin() string {
	if h.pluginMgr == nil { return "" }
	return h.pluginMgr.ActivePlugin()
}
func (h *Holder) ActivatePlugin(name string) error {
	if h.pluginMgr == nil { return fmt.Errorf("holder: no PluginManager") }
	return h.pluginMgr.Activate(name)
}
func (h *Holder) DeactivatePlugin() {
	if h.pluginMgr != nil { h.pluginMgr.Deactivate() }
}

// ── 内部内联工具 ────────────────────────────────────────────────

type inlineProvider struct {
	tools map[string]interfaces.ToolEntry
}

func (p *inlineProvider) ProviderName() string { return "_inline" }
func (p *inlineProvider) Tools() []interfaces.ToolEntry {
	result := make([]interfaces.ToolEntry, 0, len(p.tools))
	for _, entry := range p.tools {
		result = append(result, entry)
	}
	return result
}

type inlineHandler struct {
	Fn func(ctx context.Context, argsJSON string) (string, error)
}

func (h *inlineHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	return h.Fn(ctx, argsJSON)
}

func (h *Holder) getOrCreateInline() *inlineProvider {
	for _, p := range h.providers {
		if ip, ok := p.(*inlineProvider); ok {
			return ip
		}
	}
	ip := &inlineProvider{tools: make(map[string]interfaces.ToolEntry)}
	h.providers = append([]interfaces.ToolProvider{ip}, h.providers...)
	h.rebuildLocked()
	return ip
}
