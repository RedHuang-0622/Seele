package main

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

func TestRunDemo(t *testing.T) {
	result, err := runDemo(context.Background())
	if err != nil {
		t.Fatalf("runDemo() error = %v", err)
	}
	if !strings.Contains(result.Reply, `"result":42`) {
		t.Fatalf("reply = %q", result.Reply)
	}
	if result.CompletionCalls != 2 {
		t.Fatalf("completion calls = %d, want 2", result.CompletionCalls)
	}
	if result.VisibleTools != 3 {
		t.Fatalf("visible tools = %d, want 3", result.VisibleTools)
	}
	if result.ToolEvents != 2 {
		t.Fatalf("tool events = %d, want intent/effect pair", result.ToolEvents)
	}
}

func TestHasToolReturnsFalseForUnknownName(t *testing.T) {
	available := []types.Tool{{Function: types.ToolFunction{Name: "calculate"}}}
	if hasTool(available, "missing") {
		t.Fatal("hasTool() returned true for an unknown tool")
	}
}

func TestMainEntrypoint(t *testing.T) {
	main()
}
