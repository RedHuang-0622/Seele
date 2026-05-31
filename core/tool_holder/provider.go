package tool_holder

import (
	"log"

	"github.com/sukasukasuka123/Seele/provider"
)

// Register 注册一个 ToolProvider。
// 同名 provider 会追加（不去重），调用方负责保证唯一性。
// 注册顺序即 dispatch 的路由优先级：先注册的先匹配。
func (h *Holder) Register(p provider.ToolProvider) {
	h.mu.Lock()
	h.providers = append(h.providers, p)
	h.mu.Unlock()
	log.Printf("[tool_holder] registered provider=%q", p.ProviderName())
}

// Unregister 按名称移除 provider（全部同名的都移除）。
func (h *Holder) Unregister(name string) {
	h.mu.Lock()
	filtered := h.providers[:0]
	for _, p := range h.providers {
		if p.ProviderName() != name {
			filtered = append(filtered, p)
		}
	}
	h.providers = filtered
	h.mu.Unlock()
	log.Printf("[tool_holder] unregistered provider=%q", name)
}
