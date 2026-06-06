package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	types "github.com/sukasukasuka123/Seele/types"
	jsonSchema "github.com/RedHuang-0622/microHub/jsonSchema"
	"github.com/RedHuang-0622/microHub/pb_api"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// ErrToolUnavailable 表示工具暂时不可达（连接池满、超时、网络抖动等），
// 与工具返回的业务错误不同。调用方可据此决定重试而非将错误注入对话历史。
var ErrToolUnavailable = errors.New("tool temporarily unavailable")

// HubProvider 将现有 microHub + registry 封装为 ToolProvider。
//
// 这是对原 Runtime 中内联 Hub 逻辑的直接提取：
//   - tools()     → HubProvider.Tools()
//   - dispatch()  → HubProvider.Dispatch()
//
// retired 集合（Retire/Restore）从 Runtime 下沉到此处管理，
// 因为它只对 Hub 工具生效。
type HubProvider struct {
	hub             *hubbase.BaseHub
	mu              sync.RWMutex
	retired         map[string]struct{}
	toolCallTimeout time.Duration

	// toolIndex 缓存工具名到是否存在的 bool，每次 Tools() 刷新
	// 避免 HasTool 每次遍历完整列表
	mu2       sync.RWMutex
	toolIndex map[string]struct{}
}

// NewHubProvider 创建 HubProvider。
// hub 不能为 nil，registry 须在此之前已完成 Init。
func NewHubProvider(hub *hubbase.BaseHub, timeout time.Duration) (*HubProvider, error) {
	if hub == nil {
		return nil, fmt.Errorf("HubProvider: hub must not be nil")
	}
	return &HubProvider{
		hub:             hub,
		retired:         make(map[string]struct{}),
		toolCallTimeout: timeout,
		toolIndex:       make(map[string]struct{}),
	}, nil
}

func (p *HubProvider) ProviderName() string { return "microhub" }

// ── Retire / Restore（仅对 Hub 工具生效）────────────────────────

func (p *HubProvider) Retire(name string) {
	p.mu.Lock()
	p.retired[name] = struct{}{}
	p.mu.Unlock()
	log.Printf("[HubProvider] retired skill=%q", name)
}

func (p *HubProvider) Restore(name string) {
	p.mu.Lock()
	delete(p.retired, name)
	p.mu.Unlock()
	log.Printf("[HubProvider] restored skill=%q", name)
}

// ── ToolProvider 接口实现 ─────────────────────────────────────────

func (p *HubProvider) Tools() []types.Tool {
	retired := p.retiredSnapshot()
	all := registry.GetOnlineTools()

	newIndex := make(map[string]struct{}, len(all))
	result := make([]types.Tool, 0, len(all))

	for _, t := range all {
		if registry.IsOffline(t.Addr) {
			continue
		}
		if _, blocked := retired[t.Name]; blocked {
			continue
		}
		// toolIndex 包含所有工具（含 _ 前缀），确保 HasTool 对框架内部工具也返回 true
		// 修复 B2：将 HasTool 索引置于 _ 前缀过滤之前
		newIndex[t.Name] = struct{}{}
		// 隐藏框架内部工具（_ 前缀），LLM 不可见但 Dispatch 可达
		if strings.HasPrefix(t.Name, "_") {
			continue
		}
		result = append(result, types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        t.Name,
				Description: t.Method,
				Parameters:  buildParameters(t.InputSchema),
			},
		})
	}

	p.mu2.Lock()
	p.toolIndex = newIndex
	p.mu2.Unlock()

	return result
}

func (p *HubProvider) HasTool(name string) bool {
	p.mu2.RLock()
	_, ok := p.toolIndex[name]
	p.mu2.RUnlock()
	return ok
}

func (p *HubProvider) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	t, ok := registry.SelectToolByName(name)
	if !ok {
		return "", fmt.Errorf("HubProvider: skill %q not in registry", name)
	}

	p.mu.RLock()
	_, blocked := p.retired[name]
	p.mu.RUnlock()
	if blocked {
		return "", fmt.Errorf("HubProvider: skill %q is retired", name)
	}

	if !json.Valid([]byte(argsJSON)) {
		return "", fmt.Errorf("HubProvider: skill %q invalid JSON args: %.200s", name, argsJSON)
	}

	req, err := pb_api.Request().
		Method(t.Method).
		Params([]byte(argsJSON)).
		Build()
	if err != nil {
		return "", fmt.Errorf("HubProvider: build request for %q: %w", name, err)
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, p.toolCallTimeout)
	defer cancel()

	start := time.Now()
	results := p.hub.Dispatch(dispatchCtx, req)
	log.Printf("[HubProvider] dispatch skill=%s method=%s latency=%dms",
		name, t.Method, time.Since(start).Milliseconds())

	if len(results) == 0 {
		return "", fmt.Errorf("HubProvider: skill %q: no response (is the tool process running?)", name)
	}

	var parts, errs []string
	for _, r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err.Error())
			continue
		}
		for _, resp := range r.Responses {
			switch resp.Status {
			case "error":
				for _, e := range resp.Errors {
					errs = append(errs, fmt.Sprintf("[%s] %s: %s", resp.ToolName, e.Code, e.Message))
				}
			case "ok", "partial":
				if raw := string(resp.Result); raw != "" && raw != "{}" {
					parts = append(parts, raw)
				}
			}
		}
	}

	if len(errs) > 0 && len(parts) == 0 {
		if allTransportErrors(results) {
			return "", fmt.Errorf("%w: HubProvider: skill %q: %s", ErrToolUnavailable, name, strings.Join(errs, "; "))
		}
		return "", fmt.Errorf("HubProvider: skill %q failed: %s", name, strings.Join(errs, "; "))
	}
	return strings.Join(parts, "\n"), nil
}

// ── Skills 摘要（供 Engine.Skills() 使用）────────────────────────

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
			Description: t.Method, // ToolEntry 无 Description 字段，暂用 Method；待 microHub 补充该字段后替换
			Addr:        t.Addr,
		})
	}
	return result
}

// ── 内部工具方法 ───────────────────────────────────────────────────

func (p *HubProvider) retiredSnapshot() map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snap := make(map[string]struct{}, len(p.retired))
	for k := range p.retired {
		snap[k] = struct{}{}
	}
	return snap
}

// allTransportErrors 判断所有 DispatchResult 是否均为传输层错误（工具不可达）。
// 若任一结果包含业务层响应（工具正常处理了请求并返回了结果/错误），则返回 false。
func allTransportErrors(results []hubbase.DispatchResult) bool {
	if len(results) == 0 {
		return true
	}
	for _, r := range results {
		if r.Err == nil && len(r.Responses) > 0 {
			return false // 工具收到请求并给出了业务响应
		}
	}
	return true
}

// buildParameters 将 microHub input_schema 转为 OpenAI JSON Schema。
// （从原 runtime.go 迁移至此，因为它只服务于 Hub 工具）
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
		log.Printf("[HubProvider] buildParameters: parse input_schema failed: %v", err)
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
