package bridge

import (
	"context"
	"errors"
	"testing"

	roottools "github.com/RedHuang-0622/Seele/tools"
	"github.com/RedHuang-0622/Seele/types"
)

func TestRegistryRuntimeAdaptsDefinitionsAndDispatch(t *testing.T) {
	registry := roottools.NewRegistry()
	provider, err := roottools.NewFunctionProvider("test",
		entry("echo", func(_ context.Context, arguments string) (string, error) {
			return "echo:" + arguments, nil
		}),
		entry("_checkpoint", func(context.Context, string) (string, error) {
			return "checkpointed", nil
		}),
	)
	if err != nil {
		t.Fatalf("NewFunctionProvider() error = %v", err)
	}
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	runtime, err := NewRegistryRuntime(registry)
	if err != nil {
		t.Fatalf("NewRegistryRuntime() error = %v", err)
	}
	definitions := runtime.VisibleTools(context.Background())
	if len(definitions) != 1 || definitions[0].Function.Name != "echo" {
		t.Fatalf("VisibleTools() = %#v, want only echo", definitions)
	}

	result, err := runtime.Dispatch(context.Background(), "echo", `{"value":"ok"}`)
	if err != nil || result != `echo:{"value":"ok"}` {
		t.Fatalf("Dispatch() = %q, %v", result, err)
	}
	if _, err := runtime.Dispatch(context.Background(), "_checkpoint", `{}`); !errors.Is(err, ErrToolNotVisible) {
		t.Fatalf("hidden Dispatch() error = %v, want ErrToolNotVisible", err)
	}
}

func TestRegistryRuntimeAppliesVisibilityToCatalogAndDispatch(t *testing.T) {
	registry := roottools.NewRegistry()
	provider, err := roottools.NewFunctionProvider("test",
		entry("read", func(context.Context, string) (string, error) { return "read", nil }),
		entry("write", func(context.Context, string) (string, error) { return "write", nil }),
	)
	if err != nil {
		t.Fatalf("NewFunctionProvider() error = %v", err)
	}
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	type accessKey struct{}
	runtime, err := NewRegistryRuntime(registry, WithVisibilityPolicy(func(ctx context.Context, definitions []types.Tool) []types.Tool {
		if ctx.Value(accessKey{}) != "write" {
			return definitions[:1]
		}
		return definitions[1:]
	}))
	if err != nil {
		t.Fatalf("NewRegistryRuntime() error = %v", err)
	}

	readContext := context.Background()
	if visible := runtime.VisibleTools(readContext); len(visible) != 1 || visible[0].Function.Name != "read" {
		t.Fatalf("read VisibleTools() = %#v", visible)
	}
	if _, err := runtime.Dispatch(readContext, "write", `{}`); !errors.Is(err, ErrToolNotVisible) {
		t.Fatalf("write from read context error = %v, want ErrToolNotVisible", err)
	}

	writeContext := context.WithValue(context.Background(), accessKey{}, "write")
	if visible := runtime.VisibleTools(writeContext); len(visible) != 1 || visible[0].Function.Name != "write" {
		t.Fatalf("write VisibleTools() = %#v", visible)
	}
	if result, err := runtime.Dispatch(writeContext, "write", `{}`); err != nil || result != "write" {
		t.Fatalf("write Dispatch() = %q, %v", result, err)
	}
}

func TestNewRegistryRuntimeRejectsNilRegistry(t *testing.T) {
	if _, err := NewRegistryRuntime(nil); err == nil {
		t.Fatal("NewRegistryRuntime(nil) error = nil")
	}
	var registry *roottools.Registry
	if _, err := NewRegistryRuntime(registry); err == nil {
		t.Fatal("NewRegistryRuntime(typed nil) error = nil")
	}
}

func entry(name string, handler roottools.HandlerFunc) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: types.Tool{Type: "function", Function: types.ToolFunction{Name: name}},
		Handler:    handler,
	}
}
