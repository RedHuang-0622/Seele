package tool

import (
	"context"
	"sync"

	types "github.com/RedHuang-0622/Seele/types"
)

// InlineProvider 管理一组 Go 函数注册的工具。
// 实现 ToolProvider 接口。
type InlineProvider struct {
	mu    sync.RWMutex
	tools map[string]ToolEntry
}

// NewInlineProvider 创建空的 InlineProvider。
func NewInlineProvider() *InlineProvider {
	return &InlineProvider{
		tools: make(map[string]ToolEntry),
	}
}

func (p *InlineProvider) ProviderName() string { return "inline" }

// Tools 返回所有已注册的内联工具。
func (p *InlineProvider) Tools() []ToolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]ToolEntry, 0, len(p.tools))
	for _, entry := range p.tools {
		result = append(result, entry)
	}
	return result
}

// Register 注册一个内联工具。
func (p *InlineProvider) Register(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools[name] = ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name,
				Description: desc,
				Parameters:  inputSchema,
			},
		},
		Handler: &InlineToolHandler{Fn: handler},
	}
}

// Unregister 移除一个内联工具。
func (p *InlineProvider) Unregister(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.tools, name)
}
