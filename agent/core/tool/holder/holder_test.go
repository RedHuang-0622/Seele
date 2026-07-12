package holder

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// ── Mock types ──────────────────────────────────────────────────────────

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

func (m *mockProvider) ProviderName() string           { return m.name }
func (m *mockProvider) Tools() []interfaces.ToolEntry { return m.tools }

func makeEntry(name string, fn func(ctx context.Context, args string) (string, error)) interfaces.ToolEntry {
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

// ── Holder ──────────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	h := New()
	if h == nil {
		t.Fatal("New() returned nil")
	}
	if h.DispatchRetries != 3 {
		t.Errorf("expected default DispatchRetries=3, got %d", h.DispatchRetries)
	}
	if h.DispatchRetryDelay != 2*time.Second {
		t.Errorf("expected default DispatchRetryDelay=2s, got %v", h.DispatchRetryDelay)
	}
	if h.ToolCallTimeout != 30*time.Second {
		t.Errorf("expected default ToolCallTimeout=30s, got %v", h.ToolCallTimeout)
	}
	if len(h.Tools()) != 0 {
		t.Errorf("expected empty tools, got %d", len(h.Tools()))
	}
}

func TestNewWithConfig(t *testing.T) {
	cfg := HolderConfig{
		DispatchRetries:    5,
		DispatchRetryDelay: 3 * time.Second,
		ToolCallTimeout:    10 * time.Second,
	}
	h := NewWithConfig(cfg)
	if h.DispatchRetries != 5 {
		t.Errorf("expected 5, got %d", h.DispatchRetries)
	}
	if h.DispatchRetryDelay != 3*time.Second {
		t.Errorf("expected 3s, got %v", h.DispatchRetryDelay)
	}
	if h.ToolCallTimeout != 10*time.Second {
		t.Errorf("expected 10s, got %v", h.ToolCallTimeout)
	}
}

func TestDefaultHolderConfig(t *testing.T) {
	cfg := DefaultHolderConfig()
	if cfg.DispatchRetries != 3 {
		t.Errorf("expected 3, got %d", cfg.DispatchRetries)
	}
	if cfg.DispatchRetryDelay != 2*time.Second {
		t.Errorf("expected 2s, got %v", cfg.DispatchRetryDelay)
	}
	if cfg.ToolCallTimeout != 30*time.Second {
		t.Errorf("expected 30s, got %v", cfg.ToolCallTimeout)
	}
}

func TestHolderConfigEffectiveZeroValue(t *testing.T) {
	cfg := HolderConfig{}.Effective()
	defaults := DefaultHolderConfig()
	if cfg.DispatchRetries != defaults.DispatchRetries {
		t.Errorf("zero -> expected %d, got %d", defaults.DispatchRetries, cfg.DispatchRetries)
	}
	if cfg.DispatchRetryDelay != defaults.DispatchRetryDelay {
		t.Errorf("zero -> expected %v, got %v", defaults.DispatchRetryDelay, cfg.DispatchRetryDelay)
	}
	if cfg.ToolCallTimeout != defaults.ToolCallTimeout {
		t.Errorf("zero -> expected %v, got %v", defaults.ToolCallTimeout, cfg.ToolCallTimeout)
	}

	// Non-zero values preserved
	cfg2 := HolderConfig{
		DispatchRetries:    7,
		DispatchRetryDelay: 5 * time.Second,
		ToolCallTimeout:    60 * time.Second,
	}.Effective()
	if cfg2.DispatchRetries != 7 {
		t.Errorf("expected 7, got %d", cfg2.DispatchRetries)
	}
	if cfg2.DispatchRetryDelay != 5*time.Second {
		t.Errorf("expected 5s, got %v", cfg2.DispatchRetryDelay)
	}
	if cfg2.ToolCallTimeout != 60*time.Second {
		t.Errorf("expected 60s, got %v", cfg2.ToolCallTimeout)
	}
}

func TestRegister(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("tool1", func(_ context.Context, _ string) (string, error) {
				return "ok", nil
			}),
		},
	})
	tools := h.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Function.Name != "tool1" {
		t.Errorf("expected 'tool1', got %q", tools[0].Function.Name)
	}
}

func TestRegisterMultipleProviders(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name: "a",
		tools: []interfaces.ToolEntry{
			makeEntry("t1", nil),
			makeEntry("t2", nil),
		},
	})
	h.Register(&mockProvider{
		name: "b",
		tools: []interfaces.ToolEntry{
			makeEntry("t3", nil),
		},
	})
	tools := h.Tools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	names := make(map[string]bool)
	for _, t := range tools {
		names[t.Function.Name] = true
	}
	for _, n := range []string{"t1", "t2", "t3"} {
		if !names[n] {
			t.Errorf("missing tool %q", n)
		}
	}
}

func TestUnregister(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name:  "prov",
		tools: []interfaces.ToolEntry{makeEntry("t1", nil)},
	})
	if len(h.Tools()) != 1 {
		t.Fatal("expected 1 tool after register")
	}
	h.Unregister("prov")
	if len(h.Tools()) != 0 {
		t.Errorf("expected 0 tools after unregister, got %d", len(h.Tools()))
	}
}

func TestUnregisterUnknownName(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name:  "prov",
		tools: []interfaces.ToolEntry{makeEntry("t1", nil)},
	})
	h.Unregister("nonexistent")
	if len(h.Tools()) != 1 {
		t.Errorf("expected 1 tool after unregister unknown, got %d", len(h.Tools()))
	}
}

func TestDispatch(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("greet", func(_ context.Context, _ string) (string, error) {
				return "hello", nil
			}),
		},
	})
	result, err := h.Dispatch(context.Background(), "greet", `{"who":"world"}`)
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	h := New()
	_, err := h.Dispatch(context.Background(), "unknown", "{}")
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestDispatchWithRetryOnErrToolUnavailable(t *testing.T) {
	h := NewWithConfig(HolderConfig{
		DispatchRetries:    3,
		DispatchRetryDelay: time.Millisecond,
		ToolCallTimeout:    time.Second,
	})
	var callCount int32
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("flakey", func(_ context.Context, _ string) (string, error) {
				atomic.AddInt32(&callCount, 1)
				if atomic.LoadInt32(&callCount) == 1 {
					return "", interfaces.ErrToolUnavailable
				}
				return "ok-after-retry", nil
			}),
		},
	})
	result, err := h.Dispatch(context.Background(), "flakey", "{}")
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if result != "ok-after-retry" {
		t.Errorf("expected 'ok-after-retry', got %q", result)
	}
	if c := atomic.LoadInt32(&callCount); c != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", c)
	}
}

func TestDispatchWithDeadlineExceeded(t *testing.T) {
	h := NewWithConfig(HolderConfig{
		DispatchRetries:    3,
		DispatchRetryDelay: time.Millisecond,
		ToolCallTimeout:    time.Second,
	})
	var callCount int32
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("slow", func(_ context.Context, _ string) (string, error) {
				atomic.AddInt32(&callCount, 1)
				return "", context.DeadlineExceeded
			}),
		},
	})
	_, err := h.Dispatch(context.Background(), "slow", "{}")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error message, got: %v", err)
	}
	if c := atomic.LoadInt32(&callCount); c != 1 {
		t.Errorf("expected 1 call (no retry on DeadlineExceeded), got %d", c)
	}
}

func TestRegisterInline(t *testing.T) {
	h := New()
	h.RegisterInline("inline-tool", "inline desc",
		map[string]interface{}{"type": "object"},
		func(_ context.Context, _ string) (string, error) {
			return "inline-result", nil
		},
	)
	tools := h.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Function.Name != "inline-tool" {
		t.Errorf("expected 'inline-tool', got %q", tools[0].Function.Name)
	}
	if tools[0].Function.Description != "inline desc" {
		t.Errorf("expected 'inline desc', got %q", tools[0].Function.Description)
	}
	result, err := h.Dispatch(context.Background(), "inline-tool", "{}")
	if err != nil {
		t.Fatalf("Dispatch inline failed: %v", err)
	}
	if result != "inline-result" {
		t.Errorf("expected 'inline-result', got %q", result)
	}
}

func TestRegisterInlineWithOutputSchema(t *testing.T) {
	h := New()
	outputSchema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
	}
	h.RegisterInline("inline-out", "desc",
		map[string]interface{}{"type": "object"},
		func(_ context.Context, _ string) (string, error) { return "{}", nil },
		outputSchema,
	)
	st := h.state.Load()
	entry, ok := st.toolMap["inline-out"]
	if !ok {
		t.Fatal("inline-out not found in toolMap")
	}
	if entry.OutputSchema == nil {
		t.Fatal("OutputSchema should not be nil")
	}
	if typ, _ := entry.OutputSchema["type"].(string); typ != "object" {
		t.Errorf("expected type 'object', got %q", typ)
	}
}

func TestWithPluginManager(t *testing.T) {
	pm := NewPluginManager()
	h := New().WithPluginManager(pm)
	if h.Plugin() != pm {
		t.Error("Plugin() mismatch")
	}
}

func TestPluginDelegation(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("alpha", "", []string{"*"}, nil))
	if err := pm.Activate("alpha"); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	h := New().WithPluginManager(pm)

	if !h.IsPluginActive() {
		t.Error("IsPluginActive should be true")
	}
	if h.ActivePlugin() != "alpha" {
		t.Errorf("ActivePlugin should be 'alpha', got %q", h.ActivePlugin())
	}

	h.DeactivatePlugin()
	if h.IsPluginActive() {
		t.Error("IsPluginActive should be false after deactivate")
	}

	if err := h.ActivatePlugin("alpha"); err != nil {
		t.Fatalf("ActivatePlugin failed: %v", err)
	}
	if !h.IsPluginActive() {
		t.Error("IsPluginActive should be true after reactivate")
	}
}

func TestActivatePluginWithoutPluginManager(t *testing.T) {
	h := New()
	if err := h.ActivatePlugin("test"); err == nil {
		t.Fatal("expected error")
	} else if !strings.Contains(err.Error(), "no PluginManager") {
		t.Errorf("expected 'no PluginManager' error, got: %v", err)
	}
	if h.IsPluginActive() {
		t.Error("IsPluginActive should be false")
	}
	if h.ActivePlugin() != "" {
		t.Errorf("ActivePlugin should be empty, got %q", h.ActivePlugin())
	}
	h.DeactivatePlugin() // should not panic
}

func TestHiddenToolsNotExposed(t *testing.T) {
	h := New()
	h.Register(&mockProvider{
		name: "prov",
		tools: []interfaces.ToolEntry{
			makeEntry("_hidden", nil),
			makeEntry("visible", nil),
		},
	})
	tools := h.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 visible tool, got %d", len(tools))
	}
	if tools[0].Function.Name != "visible" {
		t.Errorf("expected 'visible', got %q", tools[0].Function.Name)
	}
	// Hidden tool should still be in toolMap for dispatch
	st := h.state.Load()
	if _, ok := st.toolMap["_hidden"]; !ok {
		t.Error("hidden tool should exist in toolMap")
	}
}

// ── PluginManager ───────────────────────────────────────────────────────

func TestNewPluginManager(t *testing.T) {
	pm := NewPluginManager()
	if pm == nil {
		t.Fatal("NewPluginManager returned nil")
	}
	if pm.ActivePlugin() != "" {
		t.Errorf("expected empty active plugin, got %q", pm.ActivePlugin())
	}
	if pm.IsPluginActive() {
		t.Error("new PluginManager should not be active")
	}
}

func TestPluginManagerDefineUndefinePluginAllPlugins(t *testing.T) {
	pm := NewPluginManager()

	pm.Define(NewPlugin("p1", "first", []string{"a"}, nil))
	pm.Define(NewPlugin("p2", "second", nil, []string{"b"}))

	p, ok := pm.Plugin("p1")
	if !ok {
		t.Fatal("expected p1 to exist")
	}
	if p.Name != "p1" || p.Description != "first" {
		t.Errorf("unexpected plugin fields: %+v", p)
	}

	_, ok = pm.Plugin("nonexistent")
	if ok {
		t.Error("nonexistent plugin should not be found")
	}

	names := pm.AllPlugins()
	if len(names) != 2 {
		t.Fatalf("expected 2 plugin names, got %d", len(names))
	}
	m := make(map[string]bool)
	for _, n := range names {
		m[n] = true
	}
	if !m["p1"] || !m["p2"] {
		t.Errorf("AllPlugins missing expected: got %v", names)
	}

	pm.Undefine("p1")
	_, ok = pm.Plugin("p1")
	if ok {
		t.Error("p1 should be removed after Undefine")
	}
	if len(pm.AllPlugins()) != 1 {
		t.Errorf("expected 1 remaining plugin, got %d", len(pm.AllPlugins()))
	}
}

func TestPluginManagerActivateDeactivate(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("act", "", []string{"x"}, nil))

	if err := pm.Activate("act"); err != nil {
		t.Fatalf("Activate failed: %v", err)
	}
	if !pm.IsPluginActive() {
		t.Error("should be active")
	}
	if pm.ActivePlugin() != "act" {
		t.Errorf("expected 'act', got %q", pm.ActivePlugin())
	}

	pm.Deactivate()
	if pm.IsPluginActive() {
		t.Error("should not be active after Deactivate")
	}
	if pm.ActivePlugin() != "" {
		t.Errorf("expected '', got %q", pm.ActivePlugin())
	}
}

func TestPluginManagerActivateEmptyName(t *testing.T) {
	pm := NewPluginManager()
	if err := pm.Activate(""); err != nil {
		t.Errorf("Activate empty should not error, got: %v", err)
	}
}

func TestPluginManagerActivateUnknownPlugin(t *testing.T) {
	pm := NewPluginManager()
	err := pm.Activate("unknown")
	if err == nil {
		t.Fatal("expected error activating unknown plugin")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("expected 'not defined' error, got: %v", err)
	}
}

func TestPluginManagerUndefineActivePluginDeactivates(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("active-p", "", []string{"*"}, nil))
	pm.Activate("active-p")
	pm.Undefine("active-p")

	if pm.IsPluginActive() {
		t.Error("should be inactive after undefining active plugin")
	}
	if _, ok := pm.Plugin("active-p"); ok {
		t.Error("plugin should be removed")
	}
}

func TestPluginManagerFilterNoActivePlugin(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("p", "", []string{"only-me"}, nil))
	tools := []types.Tool{
		{Function: types.ToolFunction{Name: "tool-a"}},
		{Function: types.ToolFunction{Name: "tool-b"}},
	}
	result := pm.Filter(tools)
	if len(result) != 2 {
		t.Errorf("expected all 2 tools when no active plugin, got %d", len(result))
	}
}

func TestPluginManagerFilterWithActivePlugin(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("filter-p", "", []string{"allowed-*"}, nil))
	pm.Activate("filter-p")

	tools := []types.Tool{
		{Function: types.ToolFunction{Name: "allowed-foo"}},
		{Function: types.ToolFunction{Name: "allowed-bar"}},
		{Function: types.ToolFunction{Name: "blocked-baz"}},
	}
	result := pm.Filter(tools)
	if len(result) != 2 {
		t.Fatalf("expected 2 filtered tools, got %d", len(result))
	}
	for _, tt := range result {
		if tt.Function.Name != "allowed-foo" && tt.Function.Name != "allowed-bar" {
			t.Errorf("unexpected tool in result: %q", tt.Function.Name)
		}
	}
}

func TestPluginManagerClone(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("orig", "", []string{"*"}, nil))
	pm.Activate("orig")

	clone := pm.Clone()
	clone.Undefine("orig")

	if _, ok := pm.Plugin("orig"); !ok {
		t.Error("original should still have plugin")
	}
	if _, ok := clone.Plugin("orig"); ok {
		t.Error("clone should not have undefined plugin")
	}
}

func TestPluginManagerDefineEmptyName(t *testing.T) {
	pm := NewPluginManager()
	pm.Define(NewPlugin("", "", nil, nil))
	if len(pm.AllPlugins()) != 0 {
		t.Error("should not define plugin with empty name")
	}
}

func TestPluginManagerUndefineEmptyName(t *testing.T) {
	pm := NewPluginManager()
	pm.Undefine("") // should not panic
}

// ── Plugin ─────────────────────────────────────────────────────────────

func TestNewPlugin(t *testing.T) {
	p := NewPlugin("test-plug", "useful description", []string{"in-a", "in-b"}, []string{"ex-c"})
	if p.Name != "test-plug" {
		t.Errorf("expected 'test-plug', got %q", p.Name)
	}
	if p.Description != "useful description" {
		t.Errorf("expected 'useful description', got %q", p.Description)
	}
	if len(p.Include) != 2 || p.Include[0] != "in-a" || p.Include[1] != "in-b" {
		t.Errorf("unexpected Include: %v", p.Include)
	}
	if len(p.Exclude) != 1 || p.Exclude[0] != "ex-c" {
		t.Errorf("unexpected Exclude: %v", p.Exclude)
	}
}

func TestPluginMatchWildcard(t *testing.T) {
	p := NewPlugin("test", "", []string{"*"}, nil)
	if !p.Match("anything") {
		t.Error("'*' should match any tool")
	}
	if p.Match("") {
		t.Error("empty tool name should not match")
	}
}

func TestPluginMatchNoPatterns(t *testing.T) {
	p := NewPlugin("all", "", nil, nil)
	if !p.IsAllTools() {
		t.Error("should be all-tools")
	}
	if !p.Match("any") {
		t.Error("all-tools should match anything")
	}
}

func TestPluginMatchExcludePattern(t *testing.T) {
	p := NewPlugin("test", "", []string{"*"}, []string{"secret-*"})
	if p.Match("secret-tool") {
		t.Error("exclude pattern should prevent match")
	}
	if !p.Match("public-tool") {
		t.Error("non-excluded tool should match")
	}
}

func TestPluginMatchExactInclude(t *testing.T) {
	p := NewPlugin("test", "", []string{"exactly-this"}, nil)
	if !p.Match("exactly-this") {
		t.Error("should match exact tool name")
	}
	if p.Match("other-tool") {
		t.Error("should not match other tool")
	}
}

func TestPluginMatchGlobPrefix(t *testing.T) {
	p := NewPlugin("test", "", []string{"tool-*"}, nil)
	if !p.Match("tool-foo") {
		t.Error("'tool-*' should match 'tool-foo'")
	}
	if p.Match("foo-tool") {
		t.Error("'tool-*' should not match 'foo-tool'")
	}
}

func TestPluginMatchGlobSuffix(t *testing.T) {
	p := NewPlugin("test", "", []string{"*-tool"}, nil)
	if !p.Match("awesome-tool") {
		t.Error("'*-tool' should match 'awesome-tool'")
	}
	if p.Match("tool-awesome") {
		t.Error("'*-tool' should not match 'tool-awesome'")
	}
}

func TestPluginMatchExcludeOnly(t *testing.T) {
	p := NewPlugin("test", "", nil, []string{"skip-*"})
	if !p.Match("keep-this") {
		t.Error("should match unfiltered tool")
	}
	if p.Match("skip-me") {
		t.Error("should not match excluded tool")
	}
}

func TestPluginIsAllTools(t *testing.T) {
	p1 := NewPlugin("a", "", nil, nil)
	if !p1.IsAllTools() {
		t.Error("no patterns should be all-tools")
	}
	p2 := NewPlugin("b", "", []string{"x"}, nil)
	if p2.IsAllTools() {
		t.Error("include patterns should not be all-tools")
	}
	p3 := NewPlugin("c", "", nil, []string{"y"})
	if p3.IsAllTools() {
		t.Error("exclude patterns should not be all-tools")
	}
}

// ── matchGlob ──────────────────────────────────────────────────────────

func TestMatchGlobWildcard(t *testing.T) {
	if !matchGlob("*", "anything") {
		t.Error("'*' should match 'anything'")
	}
	if !matchGlob("*", "") {
		t.Error("'*' should match empty string")
	}
}

func TestMatchGlobExact(t *testing.T) {
	if !matchGlob("hello", "hello") {
		t.Error("exact match should succeed")
	}
	if matchGlob("hello", "world") {
		t.Error("different strings should not match")
	}
}

func TestMatchGlobPrefixWildcard(t *testing.T) {
	if !matchGlob("prefix-*", "prefix-123") {
		t.Error("'prefix-*' should match 'prefix-123'")
	}
	if matchGlob("prefix-*", "other-prefix-123") {
		t.Error("'prefix-*' should not match 'other-prefix-123'")
	}
}

func TestMatchGlobSuffixWildcard(t *testing.T) {
	if !matchGlob("*-suffix", "abc-suffix") {
		t.Error("'*-suffix' should match 'abc-suffix'")
	}
	if matchGlob("*-suffix", "abc-suffix-other") {
		t.Error("'*-suffix' should not match 'abc-suffix-other'")
	}
}

func TestMatchGlobMiddleWildcard(t *testing.T) {
	if !matchGlob("a*b", "aXb") {
		t.Error("'a*b' should match 'aXb'")
	}
	if !matchGlob("a*b", "ab") {
		t.Error("'a*b' should match 'ab' (zero-length match)")
	}
	if matchGlob("a*b", "ba") {
		t.Error("'a*b' should not match 'ba'")
	}
}

func TestMatchGlobEmptyPattern(t *testing.T) {
	if matchGlob("", "any") {
		t.Error("empty pattern should not match non-empty")
	}
	if !matchGlob("", "") {
		t.Error("empty pattern should match empty name")
	}
}

func TestMatchGlobDoubleStar(t *testing.T) {
	if !matchGlob("**", "anything") {
		t.Error("'**' should match anything")
	}
}

func TestMatchGlobNoWildcard(t *testing.T) {
	if !matchGlob("nocontent", "nocontent") {
		t.Error("exact match without wildcard should work")
	}
	if matchGlob("nocontent", "nocontent2") {
		t.Error("different strings should not match without wildcard")
	}
}
