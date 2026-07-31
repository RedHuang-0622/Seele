package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

type syncOnlyCompleter struct{}

func (syncOnlyCompleter) Complete(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
	content := "sync response"
	return types.Message{Role: "assistant", Content: &content}, nil
}

type injectedToolRuntime struct{}

func (injectedToolRuntime) VisibleTools(context.Context) []types.Tool {
	return []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "injected"}}}
}

func (injectedToolRuntime) Dispatch(_ context.Context, name, _ string) (string, error) {
	return "called:" + name, nil
}

func TestNewWithComponentsHasNoImplicitInfrastructure(t *testing.T) {
	a, err := NewWithComponents(Components{Completer: syncOnlyCompleter{}})
	if err != nil {
		t.Fatalf("NewWithComponents() error = %v", err)
	}
	defer a.Shutdown()
	if a.AccountPool() != nil || a.Hub() != nil || a.Tools() != nil {
		t.Fatal("explicit construction created legacy account, hub, or holder infrastructure")
	}
	if got := a.VisibleTools(context.Background()); len(got) != 0 {
		t.Fatalf("VisibleTools() = %#v, want none", got)
	}
	if _, err := a.Dispatch(context.Background(), "missing", `{}`); err == nil || !strings.Contains(err.Error(), "no tool runtime") {
		t.Fatalf("Dispatch() error = %v", err)
	}
}

func TestNewWithComponentsAdaptsSyncClientForStreaming(t *testing.T) {
	a, err := NewWithComponents(Components{
		Completer: syncOnlyCompleter{},
		Tools:     injectedToolRuntime{},
	})
	if err != nil {
		t.Fatalf("NewWithComponents() error = %v", err)
	}
	defer a.Shutdown()

	var chunks []string
	content, _, _, err := a.LLM().CompleteStream(context.Background(), nil, nil, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if content != "sync response" || len(chunks) != 1 || chunks[0] != content {
		t.Fatalf("stream fallback = %q, %#v", content, chunks)
	}
	if got := a.VisibleTools(context.Background()); len(got) != 1 || got[0].Function.Name != "injected" {
		t.Fatalf("VisibleTools() = %#v", got)
	}
	if got, err := a.Dispatch(context.Background(), "injected", `{}`); err != nil || got != "called:injected" {
		t.Fatalf("Dispatch() = %q, %v", got, err)
	}
}
