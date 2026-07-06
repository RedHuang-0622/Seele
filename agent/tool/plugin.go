// Package tool 的 plugin 子系统实现"装配件模式"的工具集管理。
//
// 对标微服务 Gateway 模式，但不是通过 proto/gRPC 路由请求，
// 而是在进程内根据激活的 Plugin 切换 LLM 可见的工具列表。
//
// ── 架构 ──
//
//	Agent (全量工具注册 + Plugin 定义)
//	  └── PluginManager (装配件注册中心)
//	       └── Plugin "dev"       → {hub_*, *_tool}
//	       └── Plugin "ops"       → {monitor_*, deploy_*}
//	       └── Plugin "minimal"   → {get_current_time, calculator}
//
//	Holder (引用 PluginManager 副本，独立控制激活)
//	  └── ActivatePlugin("dev")  → LLM 仅看到 dev 组工具
//	  └── DeactivatePlugin()     → LLM 看到全量工具 (all-tools)
//
// 装配件模式：
//   Plugin 将零散注册的 Tool 按业务场景装配为有意义的集合。
//   切换 Plugin 等价于切换 Agent 的"能力视图"。
package tool

import (
	"fmt"
	"strings"
	"sync"

	types "github.com/RedHuang-0622/Seele/types"
)

// ── Plugin 定义 ──────────────────────────────────────────────────────

// Plugin 是工具集的命名装配件（Assembly）。
//
// 语义上等同于"环境/场景/角色"——它定义了当前 LLM 应该看到哪些工具。
// 通过 Include/Exclude 列表 + * 通配符描述工具选择规则。
type Plugin struct {
	// Name 插件名称，全局唯一标识。空字符串表示"all-tools"。
	Name string

	// Description 描述当前插件适用的场景，便于调试和展示。
	Description string

	// Include 包含的工具名列表（支持 * 通配符）。
	// 空值表示"包含全部"（即 IsAllTools）。
	Include []string

	// Exclude 要从 Include 结果中排除的工具名（支持 * 通配符）。
	// 排除优先于包含：即使工具名匹配 Include，只要匹配 Exclude 就排除。
	Exclude []string
}

// NewPlugin 创建插件装配件。
//
// 使用示例：
//
//	// 仅暴露 hub 相关工具
//	NewPlugin("hub-tools", "仅 Hub 工具", []string{"hub_*"}, nil)
//
//	// 暴露除 mcp 外的全部工具
//	NewPlugin("no-mcp", "排除 MCP 工具", nil, []string{"mcp_*"})
//
//	// 精确指定工具集合
//	NewPlugin("minimal", "最少工具集",
//	    []string{"get_current_time", "calculator"}, nil)
func NewPlugin(name, description string, include, exclude []string) Plugin {
	return Plugin{
		Name:        name,
		Description: description,
		Include:     include,
		Exclude:     exclude,
	}
}

// Match 判断工具名是否命中本插件的选取规则。
//
// 判定规则：
//  1. IsAllTools（Include 为空且 Exclude 为空）→ 含所有工具
//  2. Exclude 优先匹配 → 匹配即排除
//  3. Include 匹配 → 包含
//  4. Include 为空且有 Exclude → 含 Include 为空 = 无工具
func (p Plugin) Match(toolName string) bool {
	if toolName == "" {
		return false
	}

	// All-tools 模式
	if p.IsAllTools() {
		return true
	}

	// Exclude 优先
	for _, pattern := range p.Exclude {
		if pattern != "" && matchGlob(pattern, toolName) {
			return false
		}
	}

	// Include 为空 → 已通过 Exclude 则含全部，未命中则无工具
	if len(p.Include) == 0 {
		// Exclude 未命中 → 全部通过
		return true
	}

	// Include 匹配
	for _, pattern := range p.Include {
		if pattern != "" && matchGlob(pattern, toolName) {
			return true
		}
	}

	return false
}

// IsAllTools 报告此插件是否包含所有已注册工具。
func (p Plugin) IsAllTools() bool {
	return len(p.Include) == 0 && len(p.Exclude) == 0
}

// ── PluginManager ────────────────────────────────────────────────────

// PluginManager 是插件装配件的注册中心与激活控制器。
//
// 职责：
//   - 管理 Plugin 的定义（增删查）
//   - 控制当前激活的插件
//   - 根据激活插件过滤工具列表
//
// 并发安全：内部 sync.RWMutex 保护读写。
type PluginManager struct {
	mu      sync.RWMutex
	plugins map[string]Plugin // key = Plugin.Name
	active  string            // 当前激活的 Plugin.Name；"" = all-tools
}

// NewPluginManager 创建空的插件管理器。
// 初始状态为 all-tools 模式（不过滤工具）。
func NewPluginManager() *PluginManager {
	return &PluginManager{
		plugins: make(map[string]Plugin),
	}
}

// ── 定义管理 ─────────────────────────────────────────────────────────

// Define 定义或覆盖一个插件装配件。
func (pm *PluginManager) Define(p Plugin) {
	if p.Name == "" {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.plugins[p.Name] = p
}

// Undefine 移除一个插件定义。若该插件正激活则自动回到 all-tools。
func (pm *PluginManager) Undefine(name string) {
	if name == "" {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.plugins, name)
	if pm.active == name {
		pm.active = ""
	}
}

// Plugin 返回指定插件的只读副本。
func (pm *PluginManager) Plugin(name string) (Plugin, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.plugins[name]
	return p, ok
}

// AllPlugins 返回所有已定义插件的名称列表。
func (pm *PluginManager) AllPlugins() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	names := make([]string, 0, len(pm.plugins))
	for name := range pm.plugins {
		names = append(names, name)
	}
	return names
}

// ── 激活控制 ─────────────────────────────────────────────────────────

// Activate 激活指定插件。激活后 Plugin.Filter 将按该插件规则过滤工具。
// name="" 回到 all-tools 模式（不过滤）。
func (pm *PluginManager) Activate(name string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if name == "" {
		pm.active = ""
		return nil
	}
	if _, ok := pm.plugins[name]; !ok {
		return fmt.Errorf("plugin %q not defined", name)
	}
	pm.active = name
	return nil
}

// Deactivate 停用当前插件，回到 all-tools 模式。
func (pm *PluginManager) Deactivate() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.active = ""
}

// ActivePlugin 返回当前激活的插件名。"" 表示 all-tools 模式。
func (pm *PluginManager) ActivePlugin() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.active
}

// IsPluginActive 报告当前是否处于插件模式（即非 all-tools）。
func (pm *PluginManager) IsPluginActive() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.active != ""
}

// ── 工具过滤 ─────────────────────────────────────────────────────────

// Filter 根据当前激活的插件过滤工具列表。
//
// 这是 PluginManager 的核心方法——它实现"Gateway"的请求路由语义：
// 将全量工具集 → 根据当前插件 → 输出子集。
//
// 无激活插件时直接返回原列表（all-tools 模式），零额外开销。
func (pm *PluginManager) Filter(tools []types.Tool) []types.Tool {
	pm.mu.RLock()
	active := pm.active
	plugin, ok := pm.plugins[active]
	pm.mu.RUnlock()

	if !ok || active == "" {
		return tools
	}

	result := make([]types.Tool, 0, len(tools))
	for _, t := range tools {
		if plugin.Match(t.Function.Name) {
			result = append(result, t)
		}
	}
	return result
}

// Count 返回当前激活插件匹配的工具数量。
// 用于快速检查当前插件是否使得某些工具不可见。
func (pm *PluginManager) Count(allTools []types.Tool) int {
	return len(pm.Filter(allTools))
}

// ── 克隆 ─────────────────────────────────────────────────────────────

// Clone 创建 PluginManager 的深层副本。
// 用于将 Agent 层的插件定义传播到各个 Session。
func (pm *PluginManager) Clone() *PluginManager {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	clone := &PluginManager{
		plugins: make(map[string]Plugin, len(pm.plugins)),
	}
	for name, p := range pm.plugins {
		clone.plugins[name] = p
	}
	return clone
}

// ── Glob 匹配 ────────────────────────────────────────────────────────

// matchGlob 简化 glob 匹配器，仅支持 * 通配符。
//
//	"name"     → 精确匹配
//	"prefix*"  → 前缀匹配
//	"*suffix"  → 后缀匹配
//	"*"        → 匹配全部
//	"*sub*"    → 子串包含
func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}

	// 按 * 分割
	parts := strings.Split(pattern, "*")
	// 过滤空部分（连续的 *）
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}

	if len(nonEmpty) == 0 {
		return true // 模式全是 *
	}

	// 前缀匹配
	if strings.HasPrefix(pattern, nonEmpty[0]) && !strings.HasPrefix(pattern, "*") {
		if !strings.HasPrefix(name, nonEmpty[0]) {
			return false
		}
		nonEmpty = nonEmpty[1:]
	}

	// 后缀匹配
	if len(nonEmpty) > 0 && strings.HasSuffix(pattern, nonEmpty[len(nonEmpty)-1]) && !strings.HasSuffix(pattern, "*") {
		last := nonEmpty[len(nonEmpty)-1]
		if !strings.HasSuffix(name, last) {
			return false
		}
		nonEmpty = nonEmpty[:len(nonEmpty)-1]
	}

	// 中间子串包含检查
	rest := name
	for _, part := range nonEmpty {
		idx := strings.Index(rest, part)
		if idx == -1 {
			return false
		}
		rest = rest[idx+len(part):]
	}

	return true
}
