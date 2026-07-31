package adapter

import (
	"context"
	"errors"
	"testing"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

func TestProtocolProvidersRefreshAndInvoke(t *testing.T) {
	tests := []struct {
		name string
		new  func(string, Catalog, Invoker, ...Option) (*Provider, error)
		kind string
	}{
		{name: "mcp", new: NewMCPProvider, kind: KindMCP},
		{name: "microhub", new: NewMicroHubProvider, kind: KindMicroHub},
		{name: "skills", new: NewSkillsProvider, kind: KindSkills},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var invoked string
			provider, err := test.new(test.name,
				CatalogFunc(func(context.Context) ([]Descriptor, error) {
					return []Descriptor{{Name: "search", RemoteName: "remote.search", Description: "find"}}, nil
				}),
				InvokeFunc(func(_ context.Context, name, arguments string) (string, error) {
					invoked = name + ":" + arguments
					return "done", nil
				}),
				WithNamespace("remote"),
			)
			if err != nil {
				t.Fatalf("new provider: %v", err)
			}
			registry := roottools.NewRegistry()
			if err := registry.Register(provider); err != nil {
				t.Fatalf("Register: %v", err)
			}
			if len(registry.Tools()) != 0 {
				t.Fatal("catalog I/O must be explicit; provider starts empty")
			}
			if err := registry.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh: %v", err)
			}
			definitions := registry.Tools()
			if len(definitions) != 1 || definitions[0].Function.Name != "remote__search" {
				t.Fatalf("definitions = %+v", definitions)
			}
			result, err := registry.Dispatch(context.Background(), roottools.ToolCall{
				Name: "remote__search", ArgumentsJSON: `{"q":"go"}`,
			})
			if err != nil || result != "done" || invoked != `remote.search:{"q":"go"}` {
				t.Fatalf("result=%q invoked=%q err=%v", result, invoked, err)
			}
			entry := registry.Snapshot().Entries["remote__search"]
			if entry.Metadata["provider_kind"] != test.kind {
				t.Fatalf("metadata = %+v", entry.Metadata)
			}
		})
	}
}

func TestProviderRefreshPreservesLastSuccessfulSnapshot(t *testing.T) {
	failed := false
	provider, err := NewMCPProvider("mcp",
		CatalogFunc(func(context.Context) ([]Descriptor, error) {
			if failed {
				return nil, errors.New("offline")
			}
			return []Descriptor{{Name: "one"}}, nil
		}),
		InvokeFunc(func(context.Context, string, string) (string, error) { return "", nil }),
	)
	if err != nil {
		t.Fatalf("NewMCPProvider: %v", err)
	}
	if err := provider.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	failed = true
	if err := provider.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if entries := provider.Tools(); len(entries) != 1 || entries[0].Definition.Function.Name != "one" {
		t.Fatalf("last successful snapshot was lost: %+v", entries)
	}
}

func TestProviderRejectsInvalidAndDuplicateDescriptors(t *testing.T) {
	provider, err := NewSkillsProvider("skills",
		CatalogFunc(func(context.Context) ([]Descriptor, error) {
			return []Descriptor{{Name: "same"}, {Name: "same"}}, nil
		}),
		InvokeFunc(func(context.Context, string, string) (string, error) { return "", nil }),
	)
	if err != nil {
		t.Fatalf("NewSkillsProvider: %v", err)
	}
	if err := provider.Refresh(context.Background()); !errors.Is(err, roottools.ErrDuplicateTool) {
		t.Fatalf("expected duplicate error, got %v", err)
	}
	if _, err := NewProvider("", "", nil, nil); !errors.Is(err, roottools.ErrInvalidEntry) {
		t.Fatalf("expected invalid provider error, got %v", err)
	}
}
