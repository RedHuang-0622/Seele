package tool_holder

import (
	"log"

	"github.com/RedHuang-0622/Seele/provider"
)

// Register 注册一个 ToolProvider，立即将其工具并入 map。
// 注册顺序即工具名冲突时的优先级：先注册的优先。
func (h *Holder) Register(p provider.ToolProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.providers = append(h.providers, p)
	h.mergeLocked(p)
	log.Printf("[tool_holder] registered provider=%q", p.ProviderName())
}

// Unregister 按名称移除 provider 及其所有工具，重建 map。
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
	log.Printf("[tool_holder] unregistered provider=%q", name)
}
