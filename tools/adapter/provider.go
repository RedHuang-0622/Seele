// Package adapter adapts remote tool catalogs to the root tools contract.
package adapter

import (
	"context"
	"fmt"
	"sync"

	roottools "github.com/RedHuang-0622/Seele/tools"
	seeletypes "github.com/RedHuang-0622/Seele/types"
)

const (
	KindMCP      = "mcp"
	KindMicroHub = "microhub"
	KindSkills   = "skills"
)

// Descriptor is the common catalog record shared by MCP, microHub, Skills,
// and future transports. RemoteName is passed to the invoker; Name is exposed
// to the LLM and can therefore be namespaced independently.
type Descriptor struct {
	Name         string
	RemoteName   string
	Description  string
	InputSchema  map[string]interface{}
	OutputSchema map[string]interface{}
	Metadata     map[string]string
}

type Catalog interface {
	ListTools(context.Context) ([]Descriptor, error)
}

type Invoker interface {
	Invoke(context.Context, string, string) (string, error)
}

type CatalogFunc func(context.Context) ([]Descriptor, error)

func (f CatalogFunc) ListTools(ctx context.Context) ([]Descriptor, error) { return f(ctx) }

type InvokeFunc func(context.Context, string, string) (string, error)

func (f InvokeFunc) Invoke(ctx context.Context, name, argumentsJSON string) (string, error) {
	return f(ctx, name, argumentsJSON)
}

type Option func(*Provider)

// WithNamespace prefixes LLM-visible names while preserving the remote name.
func WithNamespace(namespace string) Option {
	return func(provider *Provider) { provider.namespace = namespace }
}

// Provider is a cached adapter over a remote catalog and invoker. Refresh is
// explicit so Agent construction never causes hidden network or process I/O.
type Provider struct {
	mu        sync.RWMutex
	kind      string
	name      string
	namespace string
	catalog   Catalog
	invoker   Invoker
	entries   []roottools.ToolEntry
}

func NewProvider(kind, name string, catalog Catalog, invoker Invoker, options ...Option) (*Provider, error) {
	if kind == "" || name == "" || catalog == nil || invoker == nil {
		return nil, fmt.Errorf("%w: kind, name, catalog and invoker are required", roottools.ErrInvalidEntry)
	}
	provider := &Provider{kind: kind, name: name, catalog: catalog, invoker: invoker}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}
	return provider, nil
}

func NewMCPProvider(name string, catalog Catalog, invoker Invoker, options ...Option) (*Provider, error) {
	return NewProvider(KindMCP, name, catalog, invoker, options...)
}

func NewMicroHubProvider(name string, catalog Catalog, invoker Invoker, options ...Option) (*Provider, error) {
	return NewProvider(KindMicroHub, name, catalog, invoker, options...)
}

func NewSkillsProvider(name string, catalog Catalog, invoker Invoker, options ...Option) (*Provider, error) {
	return NewProvider(KindSkills, name, catalog, invoker, options...)
}

func (p *Provider) ProviderName() string { return p.name }

func (p *Provider) Tools() []roottools.ToolEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]roottools.ToolEntry(nil), p.entries...)
}

func (p *Provider) Refresh(ctx context.Context) error {
	descriptors, err := p.catalog.ListTools(ctx)
	if err != nil {
		return fmt.Errorf("tools.adapter %s catalog %q: %w", p.kind, p.name, err)
	}
	entries := make([]roottools.ToolEntry, 0, len(descriptors))
	seen := make(map[string]struct{}, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.Name == "" {
			return fmt.Errorf("%w: %s provider %q returned unnamed tool", roottools.ErrInvalidEntry, p.kind, p.name)
		}
		visibleName := descriptor.Name
		if p.namespace != "" {
			visibleName = p.namespace + "__" + descriptor.Name
		}
		if _, exists := seen[visibleName]; exists {
			return fmt.Errorf("%w: %s provider %q tool %q", roottools.ErrDuplicateTool, p.kind, p.name, visibleName)
		}
		seen[visibleName] = struct{}{}
		remoteName := descriptor.RemoteName
		if remoteName == "" {
			remoteName = descriptor.Name
		}
		invokeName := remoteName
		entries = append(entries, roottools.ToolEntry{
			Definition: seeletypes.Tool{Type: "function", Function: seeletypes.ToolFunction{
				Name: visibleName, Description: descriptor.Description, Parameters: objectSchema(descriptor.InputSchema),
			}},
			Handler: roottools.HandlerFunc(func(callCtx context.Context, argumentsJSON string) (string, error) {
				return p.invoker.Invoke(callCtx, invokeName, argumentsJSON)
			}),
			OutputSchema: descriptor.OutputSchema,
			Metadata:     cloneMetadata(descriptor.Metadata, p.kind),
		})
	}
	p.mu.Lock()
	p.entries = entries
	p.mu.Unlock()
	return nil
}

func objectSchema(schema map[string]interface{}) map[string]interface{} {
	if schema != nil {
		return schema
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func cloneMetadata(metadata map[string]string, kind string) map[string]string {
	result := make(map[string]string, len(metadata)+1)
	for key, value := range metadata {
		result[key] = value
	}
	result["provider_kind"] = kind
	return result
}
