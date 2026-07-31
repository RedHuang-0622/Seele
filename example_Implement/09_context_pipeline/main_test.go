package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunContextDemo(t *testing.T) {
	result, err := runContextDemo(context.Background())
	if err != nil {
		t.Fatalf("runContextDemo() error = %v", err)
	}
	if len(result.AssembledMessages) != 4 {
		t.Fatalf("assembled messages = %d, want 4", len(result.AssembledMessages))
	}
	if got := value(result.AssembledMessages[0].Content); got != "Plan: inspect -> implement -> test" {
		t.Fatalf("plan block = %q", got)
	}
	if !strings.Contains(result.ToolResultView, "artifact://test/42") || strings.Contains(result.ToolResultView, "verbose_log") {
		t.Fatalf("tool result view = %q", result.ToolResultView)
	}
	if result.ShortChatCalls != 0 {
		t.Fatalf("short quickchat calls = %d, want 0", result.ShortChatCalls)
	}
	if result.LongChatCalls != 1 {
		t.Fatalf("long quickchat calls = %d, want 1", result.LongChatCalls)
	}
	if len(result.Compressed) != 1 || !strings.Contains(value(result.Compressed[0].Content), "checkpoint") {
		t.Fatalf("compressed = %#v", result.Compressed)
	}
}

func TestValueHandlesNilContent(t *testing.T) {
	if got := value(nil); got != "" {
		t.Fatalf("value(nil) = %q", got)
	}
}

func TestMainEntrypoint(t *testing.T) {
	main()
}
