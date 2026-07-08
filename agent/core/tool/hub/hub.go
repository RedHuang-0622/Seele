// Package hubprov 封装 microHub gRPC 工具提供者。
//
// 依赖 microHub 的 gRPC 服务发现与调度能力，将外部 Skill 进程
// 包装为 agent/tool.ToolProvider 接口。
package hubprov

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
	jsonSchema "github.com/RedHuang-0622/microHub/jsonSchema"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// HubProvider 将现有 microHub + registry 封装为 tool.ToolProvider。
type HubProvider struct {
	hub             *hubbase.BaseHub
	mu              sync.RWMutex
	retired         map[string]struct{}
	toolCallTimeout time.Duration
}

// NewHubProvider 创建 HubProvider。
func NewHubProvider(hub *hubbase.BaseHub, timeout time.Duration) (*HubProvider, error) {
	if hub == nil {
		return nil, fmt.Errorf("HubProvider: hub must not be nil")
	}
	return &HubProvider{
		hub:             hub,
		retired:         make(map[string]struct{}),
		toolCallTimeout: timeout,
	}, nil
}

func (p *HubProvider) ProviderName() string { return "microhub" }

// ── Retire / Restore ────────────────────────────────────────────

func (p *HubProvider) Retire(name string) {
	p.mu.Lock()
	p.retired[name] = struct{}{}
	p.mu.Unlock()
	slog.Default().Info("hub skill retired", "skill", name)
}

func (p *HubProvider) Restore(name string) {
	p.mu.Lock()
	delete(p.retired, name)
	p.mu.Unlock()
	slog.Default().Info("hub skill restored", "skill", name)
}

// ── ToolProvider 接口实现 ─────────────────────────────────────────

func (p *HubProvider) Tools() []interfaces.ToolEntry {
	retired := p.retiredSnapshot()
	all := registry.GetOnlineTools()

	result := make([]interfaces.ToolEntry, 0, len(all))
	for _, t := range all {
		if registry.IsOffline(t.Addr) {
			continue
		}
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		result = append(result, interfaces.ToolEntry{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        t.Name,
					Description: t.Method,
					Parameters:  buildParameters(t.InputSchema),
				},
			},
			Handler: &HubToolHandler{
				Hub:     p.hub,
				Method:  t.Method,
				Timeout: p.toolCallTimeout,
			},
		})
	}
	return result
}

// ── Skills 摘要 ──────────────────────────────────────────────────

func (p *HubProvider) Skills() []types.SkillInfo {
	retired := p.retiredSnapshot()
	all := registry.GetOnlineTools()
	result := make([]types.SkillInfo, 0, len(all))
	for _, t := range all {
		if registry.IsOffline(t.Addr) {
			continue
		}
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		result = append(result, types.SkillInfo{
			Name:        t.Name,
			Method:      t.Method,
			Description: t.Method,
			Addr:        t.Addr,
		})
	}
	return result
}

// ── 内部辅助 ─────────────────────────────────────────────────────

func (p *HubProvider) retiredSnapshot() map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := make(map[string]struct{}, len(p.retired))
	for k := range p.retired {
		snap[k] = struct{}{}
	}
	return snap
}

// buildParameters 将 microHub SchemaNode 转为 OpenAI function calling 格式。
func buildParameters(inputSchema string) map[string]interface{} {
	fallback := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
	if inputSchema == "" {
		return fallback
	}
	var node jsonSchema.SchemaNode
	if err := json.Unmarshal([]byte(inputSchema), &node); err != nil {
		slog.Default().Warn("hub buildParameters: parse input_schema failed", "error", err)
		return fallback
	}
	if node.Type != jsonSchema.TypeObject {
		return fallback
	}

	properties := make(map[string]interface{}, len(node.Data))
	for fieldName, fieldNode := range node.Data {
		properties[fieldName] = schemaNodeToOpenAI(fieldNode)
	}

	params := map[string]interface{}{
		"type":       "object",
		"properties": properties,
	}
	if len(node.Required) > 0 {
		params["required"] = node.Required
	}
	return params
}

func schemaNodeToOpenAI(node *jsonSchema.SchemaNode) map[string]interface{} {
	if node == nil {
		return map[string]interface{}{"type": "string"}
	}
	m := map[string]interface{}{"type": string(node.Type)}
	if len(node.Enum) > 0 {
		m["enum"] = node.Enum
	}
	if node.Min != nil {
		m["minimum"] = *node.Min
	}
	if node.Max != nil {
		m["maximum"] = *node.Max
	}
	if node.Default != nil {
		m["default"] = node.Default
	}
	if node.Type == jsonSchema.TypeObject && len(node.Data) > 0 {
		props := make(map[string]interface{}, len(node.Data))
		for k, v := range node.Data {
			props[k] = schemaNodeToOpenAI(v)
		}
		m["properties"] = props
		if len(node.Required) > 0 {
			m["required"] = node.Required
		}
	}
	if node.Type == jsonSchema.TypeArray && node.Items != nil {
		m["items"] = schemaNodeToOpenAI(node.Items)
	}
	return m
}
