package builtin

import (
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
)

// RegisterAll registers all built-in tool providers on the given holder.
// This is the recommended one-call setup for CLI applications.
func RegisterAll(h *holder.Holder) {
	providers := AllProviders()
	for _, p := range providers {
		h.Register(p)
	}
}

// AllProviders returns all built-in tool providers.
func AllProviders() []interfaces.ToolProvider {
	return []interfaces.ToolProvider{
		NewGrepTool(),
		NewEditorTool(),
		NewShellTool(),
		NewGitTool(),
	}
}

// WithShellTimeout creates a ShellTool with a custom default command timeout.
func WithShellTimeout(timeout time.Duration) *ShellTool {
	return &ShellTool{DefaultTimeout: timeout}
}
