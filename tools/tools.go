// Package tools defines Seele's product-neutral function-calling contract.
//
// The package deliberately does not import agent, seelectx, workplan, MCP, or
// any workspace implementation. Providers expose opaque handlers; callers
// decide which providers to register and how to inspect or filter results.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	seeletypes "github.com/RedHuang-0622/Seele/types"
)

var (
	ErrToolNotFound      = errors.New("tool not found")
	ErrDuplicateTool     = errors.New("duplicate tool")
	ErrDuplicateProvider = errors.New("duplicate tool provider")
	ErrInvalidEntry      = errors.New("invalid tool entry")
	ErrUnavailable       = errors.New("tool temporarily unavailable")
)

// ToolHandler executes a function-calling request. ArgumentsJSON is kept
// opaque so a provider can choose its own validation and decoding policy.
type ToolHandler interface {
	Execute(ctx context.Context, argumentsJSON string) (string, error)
}

// HandlerFunc adapts a function to ToolHandler.
type HandlerFunc func(context.Context, string) (string, error)

func (f HandlerFunc) Execute(ctx context.Context, argumentsJSON string) (string, error) {
	if f == nil {
		return "", ErrInvalidEntry
	}
	return f(ctx, argumentsJSON)
}

// ToolEntry joins an LLM-visible definition with its opaque executor.
type ToolEntry struct {
	Definition   seeletypes.Tool
	Handler      ToolHandler
	OutputSchema map[string]interface{}
	Metadata     map[string]string
}

// ToolProvider is the synchronous provider contract. Providers may refresh
// their own connection or registry before Tools is called. Dynamic providers
// can additionally implement RefreshableProvider.
type ToolProvider interface {
	ProviderName() string
	Tools() []ToolEntry
}

// RefreshableProvider is optional and lets a registry refresh remote catalogs
// without coupling the core contract to a transport.
type RefreshableProvider interface {
	ToolProvider
	Refresh(context.Context) error
}

// ProviderFunc is useful for adapters around ordinary function-calling
// registries. It has no dependency on a concrete transport or Agent.
type ProviderFunc struct {
	Name string
	List func() []ToolEntry
}

func (p ProviderFunc) ProviderName() string { return p.Name }

func (p ProviderFunc) Tools() []ToolEntry {
	if p.List == nil {
		return nil
	}
	return p.List()
}

// ToolCall describes one model function call in a transport-neutral form.
type ToolCall struct {
	ID            string
	Name          string
	ArgumentsJSON string
}

// Dispatcher executes calls by name. Implementations may add visibility,
// permissions, tracing, approval, or output filtering as middleware.
type Dispatcher interface {
	Dispatch(context.Context, ToolCall) (string, error)
}

// Middleware decorates one handler without changing provider contracts.
type Middleware func(name string, next ToolHandler) ToolHandler

// Snapshot is an immutable view used by callers to assemble model requests.
type Snapshot struct {
	Definitions []seeletypes.Tool
	Entries     map[string]ToolEntry
}

// Registry is a provider registry and dispatcher. Provider refreshes and
// registrations publish an atomic snapshot, while dispatch only takes a read
// lock and never holds it during user code execution.
type Registry struct {
	mu                sync.RWMutex
	providers         map[string]ToolProvider
	state             Snapshot
	middlewares       []Middleware
	dispatchRetries   int
	dispatchRetryWait time.Duration
	callTimeout       time.Duration
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

func WithDispatchRetries(count int, wait time.Duration) RegistryOption {
	return func(r *Registry) {
		if count >= 0 {
			r.dispatchRetries = count
		}
		if wait >= 0 {
			r.dispatchRetryWait = wait
		}
	}
}

func WithCallTimeout(timeout time.Duration) RegistryOption {
	return func(r *Registry) {
		if timeout > 0 {
			r.callTimeout = timeout
		}
	}
}

func WithMiddleware(mw ...Middleware) RegistryOption {
	return func(r *Registry) {
		r.middlewares = append(r.middlewares, mw...)
	}
}

func NewRegistry(options ...RegistryOption) *Registry {
	r := &Registry{providers: make(map[string]ToolProvider)}
	for _, option := range options {
		if option != nil {
			option(r)
		}
	}
	r.state = Snapshot{Definitions: []seeletypes.Tool{}, Entries: map[string]ToolEntry{}}
	return r
}

// Register adds a provider and rebuilds a validated snapshot. Registration is
// rejected when a provider or tool name is duplicated.
func (r *Registry) Register(provider ToolProvider) error {
	if provider == nil || provider.ProviderName() == "" {
		return fmt.Errorf("%w: provider name is required", ErrInvalidEntry)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[provider.ProviderName()]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateProvider, provider.ProviderName())
	}
	r.providers[provider.ProviderName()] = provider
	if err := r.rebuildLocked(); err != nil {
		delete(r.providers, provider.ProviderName())
		return err
	}
	return nil
}

func (r *Registry) Unregister(providerName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider, exists := r.providers[providerName]
	if !exists {
		return nil
	}
	delete(r.providers, providerName)
	if err := r.rebuildLocked(); err != nil {
		r.providers[providerName] = provider
		return err
	}
	return nil
}

// Refresh invokes optional remote provider refresh hooks and rebuilds the
// snapshot. A provider that cannot refresh leaves its last successful entries.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.RLock()
	providers := make([]ToolProvider, 0, len(r.providers))
	for _, provider := range r.providers {
		providers = append(providers, provider)
	}
	r.mu.RUnlock()
	for _, provider := range providers {
		if refreshable, ok := provider.(RefreshableProvider); ok {
			if err := refreshable.Refresh(ctx); err != nil {
				return fmt.Errorf("tools.refresh %q: %w", provider.ProviderName(), err)
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rebuildLocked()
}

func (r *Registry) rebuildLocked() error {
	entries := make(map[string]ToolEntry)
	definitions := make([]seeletypes.Tool, 0)
	for _, provider := range r.providers {
		for _, entry := range provider.Tools() {
			name := entry.Definition.Function.Name
			if name == "" || entry.Handler == nil {
				return fmt.Errorf("%w: provider %q returned unnamed or handler-less tool", ErrInvalidEntry, provider.ProviderName())
			}
			if _, exists := entries[name]; exists {
				return fmt.Errorf("%w: tool %q", ErrDuplicateTool, name)
			}
			entry.Handler = chain(name, entry.Handler, r.middlewares)
			entries[name] = entry
			if name[0] != '_' {
				definitions = append(definitions, entry.Definition)
			}
		}
	}
	r.state = Snapshot{Definitions: definitions, Entries: entries}
	return nil
}

func chain(name string, handler ToolHandler, middlewares []Middleware) ToolHandler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		if middlewares[i] != nil {
			handler = middlewares[i](name, handler)
		}
	}
	return handler
}

func (r *Registry) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]seeletypes.Tool, len(r.state.Definitions))
	for index, definition := range r.state.Definitions {
		definitions[index] = cloneDefinition(definition)
	}
	entries := make(map[string]ToolEntry, len(r.state.Entries))
	for name, entry := range r.state.Entries {
		entry.Definition = cloneDefinition(entry.Definition)
		entry.OutputSchema = cloneSchema(entry.OutputSchema)
		entry.Metadata = cloneStrings(entry.Metadata)
		entries[name] = entry
	}
	return Snapshot{Definitions: definitions, Entries: entries}
}

func (r *Registry) Tools() []seeletypes.Tool { return r.Snapshot().Definitions }

func (r *Registry) Dispatch(ctx context.Context, call ToolCall) (string, error) {
	r.mu.RLock()
	entry, ok := r.state.Entries[call.Name]
	retries, wait, timeout := r.dispatchRetries, r.dispatchRetryWait, r.callTimeout
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("tools.dispatch %q: %w", call.Name, ErrToolNotFound)
	}
	callCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		if deadline, has := ctx.Deadline(); !has || time.Until(deadline) > timeout {
			callCtx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
	}
	attempts := retries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		result, err := entry.Handler.Execute(callCtx, call.ArgumentsJSON)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !errors.Is(err, ErrUnavailable) || attempt == attempts-1 {
			return "", err
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-callCtx.Done():
				timer.Stop()
				return "", callCtx.Err()
			case <-timer.C:
			}
		}
	}
	return "", lastErr
}

// FunctionProvider exposes a fixed list of function-calling entries.
type FunctionProvider struct {
	Name    string
	Entries []ToolEntry
}

func NewFunctionProvider(name string, entries ...ToolEntry) (*FunctionProvider, error) {
	if name == "" {
		return nil, fmt.Errorf("%w: provider name is required", ErrInvalidEntry)
	}
	p := &FunctionProvider{Name: name, Entries: append([]ToolEntry(nil), entries...)}
	for _, entry := range p.Entries {
		if entry.Definition.Function.Name == "" || entry.Handler == nil {
			return nil, fmt.Errorf("%w: provider %q contains invalid entry", ErrInvalidEntry, name)
		}
	}
	return p, nil
}

func (p *FunctionProvider) ProviderName() string { return p.Name }

func (p *FunctionProvider) Tools() []ToolEntry {
	return append([]ToolEntry(nil), p.Entries...)
}

// AdaptLegacyProvider wraps providers that expose ProviderName/Tools through
// function values. It keeps adapters independent of concrete source packages.
func AdaptLegacyProvider(name string, list func() []ToolEntry) ToolProvider {
	return ProviderFunc{Name: name, List: list}
}

func cloneDefinition(definition seeletypes.Tool) seeletypes.Tool {
	definition.Function.Parameters = cloneSchema(definition.Function.Parameters)
	return definition
}

func cloneSchema(schema map[string]interface{}) map[string]interface{} {
	if schema == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(schema))
	for key, value := range schema {
		cloned[key] = cloneSchemaValue(value)
	}
	return cloned
}

func cloneSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneSchema(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneSchemaValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
