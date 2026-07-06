package tool

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	types "github.com/RedHuang-0622/Seele/types"
)

// holderState 是 Holder 的工具注册状态的不可变快照。
type holderState struct {
	toolMap  map[string]ToolEntry // name → entry（含 Handler，供 Dispatch）
	toolList []types.Tool         // 预过滤的工具定义列表（无 _ 前缀，无 Handler）
}

// Holder 是工具注册与调度的中枢。
//
// 职责：
//   - 注册/注销 ToolProvider
//   - 聚合所有 provider 的工具列表（统一 _ 前缀过滤）
//   - O(1) map 路由 dispatch 到正确的 Handler（策略模式）
//   - Plugin 装配件的注册、激活与工具过滤
type Holder struct {
	mu        sync.Mutex // 仅序列化 providers 写入 + state 重建
	providers []ToolProvider
	state     atomic.Pointer[holderState] // 读路径零锁

	DispatchRetries    int
	DispatchRetryDelay time.Duration

	pluginMgr *PluginManager // 插件装配件管理器（可选）
}

// New 创建一个空的 Holder。使用默认配置。
func New() *Holder {
	return NewWithConfig(DefaultHolderConfig())
}

// NewWithConfig 使用指定配置创建 Holder。
func NewWithConfig(cfg HolderConfig) *Holder {
	cfg = cfg.Effective()
	h := &Holder{
		providers:          make([]ToolProvider, 0),
		DispatchRetries:    cfg.DispatchRetries,
		DispatchRetryDelay: cfg.DispatchRetryDelay,
	}
	h.state.Store(&holderState{
		toolMap:  make(map[string]ToolEntry),
		toolList: make([]types.Tool, 0),
	})
	return h
}

// rebuildLocked 遍历所有 provider 构造新的 holderState 并原子替换。
// 调用方必须持有 h.mu。
func (h *Holder) rebuildLocked() {
	toolMap := make(map[string]ToolEntry)
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

// WithPluginManager 附加插件管理器到本 Holder。返回自身便于链式调用。
func (h *Holder) WithPluginManager(pm *PluginManager) *Holder {
	h.pluginMgr = pm
	return h
}

// Plugin 返回附加的 PluginManager 引用，用于精细控制插件定义。
func (h *Holder) Plugin() *PluginManager {
	return h.pluginMgr
}

// IsPluginActive 报告当前是否处于插件模式。
func (h *Holder) IsPluginActive() bool {
	return h.pluginMgr != nil && h.pluginMgr.IsPluginActive()
}

// ActivePlugin 返回当前激活的插件名。"" 表示 all-tools 模式。
func (h *Holder) ActivePlugin() string {
	if h.pluginMgr == nil {
		return ""
	}
	return h.pluginMgr.ActivePlugin()
}

// ActivatePlugin 激活指定插件，切换工具可见性规则。
func (h *Holder) ActivatePlugin(name string) error {
	if h.pluginMgr == nil {
		return fmt.Errorf("tool: no PluginManager attached, call WithPluginManager first")
	}
	return h.pluginMgr.Activate(name)
}

// DeactivatePlugin 停用当前插件，回到 all-tools 模式。
func (h *Holder) DeactivatePlugin() {
	if h.pluginMgr != nil {
		h.pluginMgr.Deactivate()
	}
}
