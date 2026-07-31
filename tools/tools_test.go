package tools

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	seeletypes "github.com/RedHuang-0622/Seele/types"
)

func entry(name string, handler ToolHandler) ToolEntry {
	return ToolEntry{
		Definition: seeletypes.Tool{Type: "function", Function: seeletypes.ToolFunction{
			Name: name, Parameters: map[string]interface{}{"type": "object"},
		}},
		Handler: handler,
	}
}

func provider(t *testing.T, name string, entries ...ToolEntry) *FunctionProvider {
	t.Helper()
	result, err := NewFunctionProvider(name, entries...)
	if err != nil {
		t.Fatalf("NewFunctionProvider: %v", err)
	}
	return result
}

func TestRegistryRegisterSnapshotAndDispatch(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(provider(t, "local", entry("echo", HandlerFunc(
		func(_ context.Context, arguments string) (string, error) { return arguments, nil },
	)))); err != nil {
		t.Fatalf("Register: %v", err)
	}

	definitions := registry.Tools()
	if len(definitions) != 1 || definitions[0].Function.Name != "echo" {
		t.Fatalf("unexpected definitions: %+v", definitions)
	}
	result, err := registry.Dispatch(context.Background(), ToolCall{Name: "echo", ArgumentsJSON: `{"ok":true}`})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if result != `{"ok":true}` {
		t.Fatalf("result = %q", result)
	}

	snapshot := registry.Snapshot()
	delete(snapshot.Entries, "echo")
	definitions[0].Function.Parameters["mutated"] = true
	if len(registry.Snapshot().Entries) != 1 {
		t.Fatal("Snapshot must not expose the registry map")
	}
	if _, exists := registry.Tools()[0].Function.Parameters["mutated"]; exists {
		t.Fatal("Tools must not expose mutable schema maps")
	}
}

func TestRegistryRejectsDuplicateProvider(t *testing.T) {
	registry := NewRegistry()
	first := provider(t, "same-provider", entry("one", HandlerFunc(func(context.Context, string) (string, error) {
		return "", nil
	})))
	if err := registry.Register(first); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := registry.Register(first); !errors.Is(err, ErrDuplicateProvider) {
		t.Fatalf("expected duplicate provider, got %v", err)
	}
	if err := registry.Unregister("same-provider"); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if len(registry.Tools()) != 0 {
		t.Fatal("provider tools remain after unregister")
	}
}

func TestRegistryRejectsDuplicateAndInvalidEntries(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(provider(t, "one", entry("same", HandlerFunc(func(context.Context, string) (string, error) {
		return "", nil
	})))); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := registry.Register(provider(t, "two", entry("same", HandlerFunc(func(context.Context, string) (string, error) {
		return "", nil
	})))); !errors.Is(err, ErrDuplicateTool) {
		t.Fatalf("expected duplicate tool error, got %v", err)
	}
	if got := len(registry.Tools()); got != 1 {
		t.Fatalf("failed registration changed snapshot: %d tools", got)
	}
	if _, err := NewFunctionProvider("bad", ToolEntry{}); !errors.Is(err, ErrInvalidEntry) {
		t.Fatalf("expected invalid entry, got %v", err)
	}
}

func TestRegistryRetriesOnlyUnavailableErrors(t *testing.T) {
	var calls atomic.Int32
	registry := NewRegistry(WithDispatchRetries(2, 0))
	handler := HandlerFunc(func(context.Context, string) (string, error) {
		if calls.Add(1) < 3 {
			return "", ErrUnavailable
		}
		return "ok", nil
	})
	if err := registry.Register(provider(t, "retry", entry("unstable", handler))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	result, err := registry.Dispatch(context.Background(), ToolCall{Name: "unstable"})
	if err != nil || result != "ok" || calls.Load() != 3 {
		t.Fatalf("result=%q calls=%d err=%v", result, calls.Load(), err)
	}

	calls.Store(0)
	registry = NewRegistry(WithDispatchRetries(3, 0))
	plainErr := errors.New("invalid arguments")
	if err := registry.Register(provider(t, "plain", entry("plain", HandlerFunc(func(context.Context, string) (string, error) {
		calls.Add(1)
		return "", plainErr
	})))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err = registry.Dispatch(context.Background(), ToolCall{Name: "plain"})
	if !errors.Is(err, plainErr) || calls.Load() != 1 {
		t.Fatalf("plain error must not retry: calls=%d err=%v", calls.Load(), err)
	}
}

func TestRegistryTimeoutAndMiddleware(t *testing.T) {
	var observedName string
	middleware := func(name string, next ToolHandler) ToolHandler {
		return HandlerFunc(func(ctx context.Context, arguments string) (string, error) {
			observedName = name
			return next.Execute(ctx, arguments)
		})
	}
	registry := NewRegistry(WithCallTimeout(10*time.Millisecond), WithMiddleware(middleware))
	if err := registry.Register(provider(t, "slow", entry("wait", HandlerFunc(func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := registry.Dispatch(context.Background(), ToolCall{Name: "wait"})
	if !errors.Is(err, context.DeadlineExceeded) || observedName != "wait" {
		t.Fatalf("middleware/timeout mismatch: name=%q err=%v", observedName, err)
	}
}

func TestRegistryHidesInternalDefinitionButAllowsDispatch(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(provider(t, "internal", entry("_checkpoint", HandlerFunc(func(context.Context, string) (string, error) {
		return "stored", nil
	})))); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(registry.Tools()) != 0 {
		t.Fatal("internal tools must not be model-visible")
	}
	if result, err := registry.Dispatch(context.Background(), ToolCall{Name: "_checkpoint"}); err != nil || result != "stored" {
		t.Fatalf("internal dispatch result=%q err=%v", result, err)
	}
}

func TestSchemaOfAndEnumOf(t *testing.T) {
	type request struct {
		Name string   `json:"name" desc:"display name"`
		Tags []string `json:"tags,omitempty" enum:"a,b"`
	}
	schema := SchemaOf(request{})
	properties := schema["properties"].(map[string]interface{})
	if properties["name"].(map[string]interface{})["description"] != "display name" {
		t.Fatal("description tag not preserved")
	}
	if !reflect.DeepEqual(schema["required"], []string{"name"}) {
		t.Fatalf("required = %#v", schema["required"])
	}
	if !reflect.DeepEqual(EnumOf("x", "y")["enum"], []interface{}{"x", "y"}) {
		t.Fatal("EnumOf mismatch")
	}
}

func TestDispatchUnknownTool(t *testing.T) {
	_, err := NewRegistry().Dispatch(context.Background(), ToolCall{Name: "missing"})
	if !errors.Is(err, ErrToolNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}
