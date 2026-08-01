// Package bridge provides explicit adapters from common Seele components to
// Agent assembly and WorkPlan execution contracts.
//
// It intentionally depends on both sides of an integration. Neither agent,
// tools, nor workplan imports this package, so using an adapter never creates
// a reverse dependency from a product-neutral component back to Agent.
package bridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/RedHuang-0622/Seele/agent"
	roottools "github.com/RedHuang-0622/Seele/tools"
	"github.com/RedHuang-0622/Seele/types"
)

// ErrToolNotVisible means that a call is not in the request-scoped tool set
// published to the model. It is deliberately distinct from tools.ErrToolNotFound:
// a registered tool may exist while not being available to this Agent request.
var ErrToolNotVisible = errors.New("tool is not visible for this request")

// Registry is the minimal root-tools surface consumed by RegistryRuntime. The
// concrete *tools.Registry satisfies it, while the narrow contract makes the
// bridge usable with decorated or test registries.
type Registry interface {
	Tools() []types.Tool
	Dispatch(context.Context, roottools.ToolCall) (string, error)
}

// VisibilityPolicy selects the request-scoped tool definitions that are both
// shown to the LLM and eligible for dispatch through this runtime. It receives
// the registry's public snapshot. Implementations must be safe for concurrent
// calls when their adapter is shared by multiple Agent sessions.
type VisibilityPolicy func(context.Context, []types.Tool) []types.Tool

// RegistryRuntimeOption configures a RegistryRuntime.
type RegistryRuntimeOption func(*RegistryRuntime)

// WithVisibilityPolicy installs request-scoped tool selection. A nil policy is
// ignored. Without this option every public tool from Registry.Tools is visible
// and dispatchable; private Registry entries (for example names prefixed with
// "_") remain unavailable through the Agent runtime.
func WithVisibilityPolicy(policy VisibilityPolicy) RegistryRuntimeOption {
	return func(runtime *RegistryRuntime) {
		if policy != nil {
			runtime.visibility = policy
		}
	}
}

// RegistryRuntime adapts a root tools.Registry-like catalog and dispatcher to
// agent.ToolRuntime. It does not register providers, refresh remote catalogs,
// grant permissions, or inspect tool output; those concerns belong to the
// supplied Registry and any middleware around it.
type RegistryRuntime struct {
	registry   Registry
	visibility VisibilityPolicy
}

// NewRegistryRuntime constructs the official Registry -> agent.ToolRuntime
// adapter. Construction has no I/O and does not mutate the registry.
func NewRegistryRuntime(registry Registry, options ...RegistryRuntimeOption) (*RegistryRuntime, error) {
	if isNilRegistry(registry) {
		return nil, fmt.Errorf("agent.bridge: registry is required")
	}
	runtime := &RegistryRuntime{
		registry: registry,
		visibility: func(_ context.Context, definitions []types.Tool) []types.Tool {
			return definitions
		},
	}
	for _, option := range options {
		if option != nil {
			option(runtime)
		}
	}
	return runtime, nil
}

// VisibleTools returns the current public registry snapshot after applying the
// request-scoped visibility policy.
func (r *RegistryRuntime) VisibleTools(ctx context.Context) []types.Tool {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.visibleTools(ctx)
}

// Dispatch forwards a model tool call to Registry.Dispatch after confirming
// that the tool is visible for this request. This makes the catalog and
// dispatcher agree: internal registry entries are not accidentally exposed by
// an Agent merely because a model guessed their names.
func (r *RegistryRuntime) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	if r == nil || r.registry == nil {
		return "", fmt.Errorf("agent.bridge: registry is required")
	}
	if !containsTool(r.visibleTools(ctx), name) {
		return "", fmt.Errorf("agent.bridge: tool %q: %w", name, ErrToolNotVisible)
	}
	return r.registry.Dispatch(ctx, roottools.ToolCall{
		Name:          name,
		ArgumentsJSON: argsJSON,
	})
}

func (r *RegistryRuntime) visibleTools(ctx context.Context) []types.Tool {
	definitions := publicTools(r.registry.Tools())
	if r.visibility == nil {
		return definitions
	}
	return r.visibility(ctx, definitions)
}

func publicTools(definitions []types.Tool) []types.Tool {
	public := make([]types.Tool, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Function.Name != "" && definition.Function.Name[0] == '_' {
			continue
		}
		public = append(public, definition)
	}
	return public
}

func containsTool(definitions []types.Tool, name string) bool {
	for _, definition := range definitions {
		if definition.Function.Name == name {
			return true
		}
	}
	return false
}

func isNilRegistry(registry Registry) bool {
	if registry == nil {
		return true
	}
	// Registry implementations are commonly pointers. Calling a method on a
	// typed nil interface would panic, so reject nil-able values at the assembly
	// boundary instead of deferring the panic to the first request.
	value := reflect.ValueOf(registry)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	}
	return false
}

var _ agent.ToolRuntime = (*RegistryRuntime)(nil)
