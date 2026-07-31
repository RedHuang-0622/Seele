package seelectx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

func message(role, content string) types.Message {
	return types.Message{Role: role, Content: &content}
}

func TestMemoryHistoryCopiesOnLoadAndSave(t *testing.T) {
	original := []types.Message{message("user", "hello")}
	history := NewMemoryHistory(original...)
	original[0].Role = "changed"

	loaded, err := history.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded[0].Role != "user" {
		t.Fatalf("stored role = %q, want user", loaded[0].Role)
	}
	loaded[0].Role = "mutated"
	reloaded, _ := history.Load(context.Background())
	if reloaded[0].Role != "user" {
		t.Fatal("Load returned storage-owned slice")
	}
}

func TestDefaultRequestAssemblerPreservesBlockOrder(t *testing.T) {
	assembler := DefaultRequestAssembler{}
	request := AssemblyRequest{
		Blocks: []PromptBlock{
			{Name: "system", Messages: []types.Message{message("system", "system")}},
			{Name: "skill", Messages: []types.Message{message("system", "skill")}},
		},
		WorkingHistory: []types.Message{message("user", "work")},
		Tools:          []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "test"}}},
	}
	got, err := assembler.Assemble(context.Background(), request)
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	want := []types.Message{
		message("system", "system"),
		message("system", "skill"),
		message("user", "work"),
	}
	if !reflect.DeepEqual(got.Messages, want) {
		t.Fatalf("messages = %#v, want %#v", got.Messages, want)
	}
	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "test" {
		t.Fatalf("tools = %#v", got.Tools)
	}
}

func TestRawToolResultProcessor(t *testing.T) {
	processor := RawToolResultProcessor{}
	view, err := processor.Process(context.Background(), ToolResult{Raw: "raw-value"})
	if err != nil || view.Content != "raw-value" {
		t.Fatalf("Process() = %#v, %v", view, err)
	}

	view, err = processor.Process(context.Background(), ToolResult{Err: errors.New("denied")})
	if err != nil || view.Content != `{"error":"denied"}` {
		t.Fatalf("error Process() = %#v, %v", view, err)
	}
}

type quickChatCompleter struct {
	seenMessages []types.Message
	seenTools    []types.Tool
}

func (c *quickChatCompleter) Complete(_ context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	c.seenMessages = messages
	c.seenTools = tools
	return message("assistant", "done"), nil
}

func TestQuickChatDoesNotKeepSessionState(t *testing.T) {
	client := &quickChatCompleter{}
	chat, err := NewQuickChat(client)
	if err != nil {
		t.Fatalf("NewQuickChat() error = %v", err)
	}
	request := QuickChatRequest{Messages: []types.Message{message("user", "one")}}
	if _, err := chat.Complete(context.Background(), request); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	request.Messages[0].Role = "changed"
	if client.seenMessages[0].Role != "user" {
		t.Fatal("QuickChat forwarded caller-owned message slice")
	}

	if _, err := chat.Complete(context.Background(), QuickChatRequest{Messages: []types.Message{message("user", "two")}}); err != nil {
		t.Fatalf("second Complete() error = %v", err)
	}
	if len(client.seenMessages) != 1 || *client.seenMessages[0].Content != "two" {
		t.Fatal("QuickChat inherited messages from the previous request")
	}
}

func TestTelemetryContextControllerAndCompressor(t *testing.T) {
	tracer := telemetry.NewMemoryTracer()
	hook, err := telemetry.NewLifecycleHook(tracer)
	if err != nil {
		t.Fatalf("NewLifecycleHook() error = %v", err)
	}
	controller := TelemetryContextController{
		Next: ContextControllerFunc(func(_ context.Context, event ContextEvent) (ContextDecision, error) {
			return ContextDecision{ReplaceHistory: event.Kind == ContextBeforeModel, History: []types.Message{message("system", "checkpoint")}}, nil
		}),
		Hook: hook,
	}
	decision, err := controller.Handle(context.Background(), ContextEvent{Kind: ContextBeforeModel, History: []types.Message{message("user", "hello")}})
	if err != nil || !decision.ReplaceHistory {
		t.Fatalf("Handle() = %#v, %v", decision, err)
	}

	compressor := TelemetryCompressor{Next: CompressorFunc(func(context.Context, CompressionRequest) (CompressionResult, error) {
		return CompressionResult{Messages: []types.Message{message("system", "summary")}, Depth: 1}, nil
	}), Hook: hook}
	if _, err := compressor.Compress(context.Background(), CompressionRequest{History: []types.Message{message("user", "long")}, MaxTokens: 10}); err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	view, err := tracer.Query(context.Background(), telemetry.Query{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(view.Events) != 4 {
		t.Fatalf("telemetry events = %d, want 4", len(view.Events))
	}
}

type CompressorFunc func(context.Context, CompressionRequest) (CompressionResult, error)

func (f CompressorFunc) Compress(ctx context.Context, request CompressionRequest) (CompressionResult, error) {
	return f(ctx, request)
}
