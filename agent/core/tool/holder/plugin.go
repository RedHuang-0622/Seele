package holder

import (
	"fmt"
	"strings"
	"sync"

	types "github.com/RedHuang-0622/Seele/types"
)

// Plugin 是工具集的命名装配件（Assembly）。
type Plugin struct {
	Name        string
	Description string
	Include     []string
	Exclude     []string
}

func NewPlugin(name, description string, include, exclude []string) Plugin {
	return Plugin{
		Name:        name,
		Description: description,
		Include:     include,
		Exclude:     exclude,
	}
}

func (p Plugin) Match(toolName string) bool {
	if toolName == "" {
		return false
	}
	if p.IsAllTools() {
		return true
	}
	for _, pattern := range p.Exclude {
		if pattern != "" && matchGlob(pattern, toolName) {
			return false
		}
	}
	if len(p.Include) == 0 {
		return true
	}
	for _, pattern := range p.Include {
		if pattern != "" && matchGlob(pattern, toolName) {
			return true
		}
	}
	return false
}

func (p Plugin) IsAllTools() bool {
	return len(p.Include) == 0 && len(p.Exclude) == 0
}

// PluginManager 是插件装配件的注册中心与激活控制器。
type PluginManager struct {
	mu      sync.RWMutex
	plugins map[string]Plugin
	active  string
}

func NewPluginManager() *PluginManager {
	return &PluginManager{
		plugins: make(map[string]Plugin),
	}
}

func (pm *PluginManager) Define(p Plugin) {
	if p.Name == "" {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.plugins[p.Name] = p
}

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

func (pm *PluginManager) Plugin(name string) (Plugin, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.plugins[name]
	return p, ok
}

func (pm *PluginManager) AllPlugins() []string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	names := make([]string, 0, len(pm.plugins))
	for name := range pm.plugins {
		names = append(names, name)
	}
	return names
}

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

func (pm *PluginManager) Deactivate() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.active = ""
}

func (pm *PluginManager) ActivePlugin() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.active
}

func (pm *PluginManager) IsPluginActive() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.active != ""
}

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

func matchGlob(pattern, name string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) == 0 {
		return true
	}
	if strings.HasPrefix(pattern, nonEmpty[0]) && !strings.HasPrefix(pattern, "*") {
		if !strings.HasPrefix(name, nonEmpty[0]) {
			return false
		}
		nonEmpty = nonEmpty[1:]
	}
	if len(nonEmpty) > 0 && strings.HasSuffix(pattern, nonEmpty[len(nonEmpty)-1]) && !strings.HasSuffix(pattern, "*") {
		last := nonEmpty[len(nonEmpty)-1]
		if !strings.HasSuffix(name, last) {
			return false
		}
		nonEmpty = nonEmpty[:len(nonEmpty)-1]
	}
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
