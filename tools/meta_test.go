package tools

import (
	"context"
	"testing"
)

func withMeta(name string, meta *ToolMeta) ToolEntry {
	entry := entry(name, HandlerFunc(func(context.Context, string) (string, error) {
		return "ok:" + name, nil
	}))
	entry.Meta = meta
	return entry
}

// TestMetaMiddlewareRoutesByKind 中间件能读到 ToolMeta.Kind，未声明 Meta 的旧
// provider 收到零值、不受影响。
func TestMetaMiddlewareRoutesByKind(t *testing.T) {
	observed := map[string]ToolKind{}
	middleware := func(name string, meta ToolMeta, next ToolHandler) ToolHandler {
		observed[name] = meta.Kind
		return next
	}
	registry := NewRegistry(WithMetaMiddleware(middleware))
	if err := registry.Register(provider(t, "meta",
		withMeta("ctl_stop", &ToolMeta{Kind: ToolKindControl, Signal: SignalTerm}),
		withMeta("read_doc", &ToolMeta{Kind: ToolKindRead, Groups: []string{"ro"}}),
		entry("plain", HandlerFunc(func(context.Context, string) (string, error) { return "plain", nil })),
	)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if observed["ctl_stop"] != ToolKindControl {
		t.Errorf("ctl_stop kind = %q, want control", observed["ctl_stop"])
	}
	if observed["read_doc"] != ToolKindRead {
		t.Errorf("read_doc kind = %q, want read", observed["read_doc"])
	}
	if kind, exists := observed["plain"]; !exists || kind != "" {
		t.Errorf("plain kind = %q exists=%v, want zero value", kind, exists)
	}

	result, err := registry.Dispatch(context.Background(), ToolCall{Name: "ctl_stop"})
	if err != nil || result != "ok:ctl_stop" {
		t.Fatalf("dispatch result=%q err=%v", result, err)
	}
}

// TestSnapshotClonesMeta 快照必须深拷贝 ToolMeta，避免外部改动污染注册表。
func TestSnapshotClonesMeta(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(provider(t, "meta", withMeta("read_doc", &ToolMeta{
		Kind:       ToolKindRead,
		Groups:     []string{"ro"},
		Visibility: []string{"main"},
	}))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	snapshot := registry.Snapshot()
	snapshot.Entries["read_doc"].Meta.Groups[0] = "mutated"
	snapshot.Entries["read_doc"].Meta.Visibility[0] = "mutated"

	again := registry.Snapshot().Entries["read_doc"].Meta
	if again == nil || again.Groups[0] != "ro" || again.Visibility[0] != "main" {
		t.Fatalf("Snapshot leaked mutable Meta: %+v", again)
	}
}

// TestNilMetaIsZeroState 未声明 Meta 的条目零值安全。
func TestNilMetaIsZeroState(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(provider(t, "plain", entry("plain", HandlerFunc(func(context.Context, string) (string, error) {
		return "", nil
	})))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if meta := registry.Snapshot().Entries["plain"].Meta; meta != nil {
		t.Fatalf("expected nil Meta, got %+v", meta)
	}
}
