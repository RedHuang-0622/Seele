package toolgw

import (
	"context"
	"testing"

	holder "github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// ── Mocks ───────────────────────────────────────────────────────────────

type mockHandler struct {
	fn func(ctx context.Context, argsJSON string) (string, error)
}

func (m *mockHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	return m.fn(ctx, argsJSON)
}

type mockProvider struct {
	name  string
	tools []interfaces.ToolEntry
}

func (m *mockProvider) ProviderName() string            { return m.name }
func (m *mockProvider) Tools() []interfaces.ToolEntry   { return m.tools }

func makeEntry(name string, fn func(ctx context.Context, args string) (string, error)) interfaces.ToolEntry {
	if fn == nil {
		fn = func(_ context.Context, _ string) (string, error) {
			return "result:" + name, nil
		}
	}
	return interfaces.ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name,
				Description: "tool " + name,
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
		Handler: &mockHandler{fn: fn},
	}
}

// ── Tests ───────────────────────────────────────────────────────────────

func TestNewDefaultGateway(t *testing.T) {
	h := holder.New()
	g := NewDefaultGateway(h)
	if g == nil {
		t.Fatal("NewDefaultGateway returned nil")
	}
}

func TestDefaultGatewayTools(t *testing.T) {
	h := holder.New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("tool-a", nil),
			makeEntry("tool-b", nil),
		},
	})
	g := NewDefaultGateway(h)

	tools := g.Tools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
}

func TestDefaultGatewayVisibleToolsWithoutPlugin(t *testing.T) {
	h := holder.New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("tool-a", nil),
			makeEntry("tool-b", nil),
		},
	})
	g := NewDefaultGateway(h)

	visible := g.VisibleTools(context.Background())
	if len(visible) != 2 {
		t.Errorf("expected 2 visible tools without plugin, got %d", len(visible))
	}
}

func TestDefaultGatewayVisibleToolsWithPlugin(t *testing.T) {
	h := holder.New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("allowed-tool", nil),
			makeEntry("blocked-tool", nil),
		},
	})

	pm := holder.NewPluginManager()
	pm.Define(holder.NewPlugin("strict", "", []string{"allowed-*"}, nil))
	pm.Activate("strict")
	h.WithPluginManager(pm)

	g := NewDefaultGateway(h)
	visible := g.VisibleTools(context.Background())
	if len(visible) != 1 {
		t.Fatalf("expected 1 visible tool with plugin, got %d", len(visible))
	}
	if visible[0].Function.Name != "allowed-tool" {
		t.Errorf("expected 'allowed-tool', got %q", visible[0].Function.Name)
	}
}

func TestDefaultGatewayDispatch(t *testing.T) {
	h := holder.New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("greet", nil),
		},
	})
	g := NewDefaultGateway(h)

	result, err := g.Dispatch(context.Background(), "greet", "{}")
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if result != "result:greet" {
		t.Errorf("expected 'result:greet', got %q", result)
	}
}

func TestDefaultGatewayDispatchUnknownTool(t *testing.T) {
	h := holder.New()
	g := NewDefaultGateway(h)

	_, err := g.Dispatch(context.Background(), "missing", "{}")
	if err == nil {
		t.Fatal("expected error dispatching unknown tool")
	}
}

func TestDefaultGatewayActivatePluginActivePlugin(t *testing.T) {
	h := holder.New()
	pm := holder.NewPluginManager()
	pm.Define(holder.NewPlugin("plug-x", "", []string{"*"}, nil))
	h.WithPluginManager(pm)

	g := NewDefaultGateway(h)

	if g.ActivePlugin() != "" {
		t.Errorf("expected no active plugin initially, got %q", g.ActivePlugin())
	}

	if err := g.ActivatePlugin("plug-x"); err != nil {
		t.Fatalf("ActivatePlugin failed: %v", err)
	}
	if g.ActivePlugin() != "plug-x" {
		t.Errorf("expected 'plug-x', got %q", g.ActivePlugin())
	}

	g.DeactivatePlugin()
	if g.ActivePlugin() != "" {
		t.Errorf("expected no active plugin after deactivate, got %q", g.ActivePlugin())
	}
}

func TestDefaultGatewayVisibleToolsRespectsDeactivation(t *testing.T) {
	h := holder.New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("alpha-tool", nil),
			makeEntry("beta-tool", nil),
		},
	})

	pm := holder.NewPluginManager()
	pm.Define(holder.NewPlugin("only-alpha", "", []string{"alpha-*"}, nil))
	pm.Activate("only-alpha")
	h.WithPluginManager(pm)

	g := NewDefaultGateway(h)

	// With plugin active: only alpha-tool visible
	visible := g.VisibleTools(context.Background())
	if len(visible) != 1 || visible[0].Function.Name != "alpha-tool" {
		t.Errorf("expected only 'alpha-tool' when plugin active, got %v", visible)
	}

	// Deactivate plugin: all tools visible
	g.DeactivatePlugin()
	visible = g.VisibleTools(context.Background())
	if len(visible) != 2 {
		t.Errorf("expected all 2 tools after deactivation, got %d", len(visible))
	}
}

// Test that *DefaultGateway satisfies the Gateway interface at compile time.
func TestGatewayInterfaceCompileCheck(t *testing.T) {
	var _ Gateway = (*DefaultGateway)(nil)
}
