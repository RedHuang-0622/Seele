package react

import (
	"context"
	"errors"
	"testing"

	types "github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// mock ChatCompleter
// ---------------------------------------------------------------------------

type mockChatCompleter struct {
	completeFunc            func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
	completeStreamFunc      func(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(delta string)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error)
	completeStreamEventsFunc func(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error)
}

func (m *mockChatCompleter) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.completeFunc(ctx, messages, tools)
}

func (m *mockChatCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(delta string)) (string, string, []types.ToolCall, error) {
	return m.completeStreamFunc(ctx, messages, tools, onChunk)
}

func (m *mockChatCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return m.completeStreamEventsFunc(ctx, messages, tools, onEvent)
}

// ---------------------------------------------------------------------------
// SyncStrategy
// ---------------------------------------------------------------------------

func TestSyncStrategy_ReturnsContentReasoningAndToolCalls(t *testing.T) {
	content := "Hello, world!"
	reasoning := "step-by-step reasoning"
	toolCalls := []types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Tokyo"}`}},
		{ID: "call_2", Type: "function", Function: types.ToolCallFunction{Name: "get_time", Arguments: `{"tz":"UTC"}`}},
	}

	mock := &mockChatCompleter{
		completeFunc: func(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
			return types.Message{
				Content:          &content,
				ReasoningContent: reasoning,
				ToolCalls:        toolCalls,
			}, nil
		},
	}

	s := &SyncStrategy{}
	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != content {
		t.Errorf("Content = %q, want %q", result.Content, content)
	}
	if result.ReasoningContent != reasoning {
		t.Errorf("ReasoningContent = %q, want %q", result.ReasoningContent, reasoning)
	}
	if len(result.ToolCalls) != len(toolCalls) {
		t.Fatalf("ToolCalls length = %d, want %d", len(result.ToolCalls), len(toolCalls))
	}
	for i := range toolCalls {
		if result.ToolCalls[i].ID != toolCalls[i].ID {
			t.Errorf("ToolCalls[%d].ID = %q, want %q", i, result.ToolCalls[i].ID, toolCalls[i].ID)
		}
	}
}

func TestSyncStrategy_NilContent(t *testing.T) {
	mock := &mockChatCompleter{
		completeFunc: func(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
			return types.Message{Content: nil}, nil
		},
	}

	s := &SyncStrategy{}
	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "" {
		t.Errorf("Content should be empty when Message.Content is nil, got %q", result.Content)
	}
}

func TestSyncStrategy_ErrorPropagation(t *testing.T) {
	expectedErr := errors.New("llm call failed")
	mock := &mockChatCompleter{
		completeFunc: func(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
			return types.Message{}, expectedErr
		},
	}

	s := &SyncStrategy{}
	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != expectedErr {
		t.Errorf("err = %v, want %v", err, expectedErr)
	}
	if result != nil {
		t.Error("result should be nil when error is returned")
	}
}

// ---------------------------------------------------------------------------
// StreamStrategy
// ---------------------------------------------------------------------------

func TestStreamStrategy_ChunksCollected(t *testing.T) {
	var chunks []string
	s := &StreamStrategy{
		OnChunk: func(delta string) {
			chunks = append(chunks, delta)
		},
	}

	mock := &mockChatCompleter{
		completeStreamFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, onChunk func(delta string)) (string, string, []types.ToolCall, error) {
			onChunk("Hello ")
			onChunk("World!")
			return "Hello World!", "reasoning", nil, nil
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "Hello World!" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello World!")
	}
	if result.ReasoningContent != "reasoning" {
		t.Errorf("ReasoningContent = %q, want %q", result.ReasoningContent, "reasoning")
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0] != "Hello " || chunks[1] != "World!" {
		t.Errorf("chunks = %v, want [Hello  World!]", chunks)
	}
}

func TestStreamStrategy_EmptyChunks(t *testing.T) {
	var chunks []string
	s := &StreamStrategy{
		OnChunk: func(delta string) {
			chunks = append(chunks, delta)
		},
	}

	mock := &mockChatCompleter{
		completeStreamFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, onChunk func(delta string)) (string, string, []types.ToolCall, error) {
			return "", "", nil, nil
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "" {
		t.Errorf("Content = %q, want empty", result.Content)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks, got %d", len(chunks))
	}
}

func TestStreamStrategy_ErrorPath(t *testing.T) {
	expectedErr := errors.New("stream error")
	s := &StreamStrategy{
		OnChunk: func(delta string) {},
	}

	mock := &mockChatCompleter{
		completeStreamFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, _ func(delta string)) (string, string, []types.ToolCall, error) {
			return "", "", nil, expectedErr
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != expectedErr {
		t.Errorf("err = %v, want %v", err, expectedErr)
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

// ---------------------------------------------------------------------------
// StreamEventStrategy
// ---------------------------------------------------------------------------

func TestStreamEventStrategy_EventsFlow(t *testing.T) {
	var collected []types.StreamEvent
	s := &StreamEventStrategy{
		OnEvent: func(ev types.StreamEvent) {
			collected = append(collected, ev)
		},
	}

	mock := &mockChatCompleter{
		completeStreamEventsFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
			onEvent(types.StreamEvent{Type: types.StreamEventText, Content: "Hello "})
			onEvent(types.StreamEvent{Type: types.StreamEventText, Content: "World"})
			onEvent(types.StreamEvent{Type: types.StreamEventDone})
			return "Hello World", "reasoning", nil, nil
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "Hello World" {
		t.Errorf("Content = %q, want %q", result.Content, "Hello World")
	}
	if result.ReasoningContent != "reasoning" {
		t.Errorf("ReasoningContent = %q, want %q", result.ReasoningContent, "reasoning")
	}
	if len(collected) != 3 {
		t.Fatalf("expected 3 events, got %d", len(collected))
	}
	if collected[0].Type != types.StreamEventText || collected[0].Content != "Hello " {
		t.Errorf("event[0] = %+v, want StreamEventText/Hello ", collected[0])
	}
}

func TestStreamEventStrategy_NonTextEvents(t *testing.T) {
	var collected []types.StreamEvent
	s := &StreamEventStrategy{
		OnEvent: func(ev types.StreamEvent) {
			collected = append(collected, ev)
		},
	}

	mock := &mockChatCompleter{
		completeStreamEventsFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
			onEvent(types.StreamEvent{Type: types.StreamEventText, Content: "A"})
			onEvent(types.StreamEvent{Type: types.StreamEventToolCall, Content: "get_weather", Index: 0})
			onEvent(types.StreamEvent{Type: types.StreamEventDone})
			return "A", "", []types.ToolCall{{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "get_weather"}}}, nil
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Content != "A" {
		t.Errorf("Content = %q, want %q", result.Content, "A")
	}
	if len(collected) != 3 {
		t.Fatalf("expected 3 events, got %d", len(collected))
	}
	if collected[1].Type != types.StreamEventToolCall {
		t.Errorf("event[1].Type = %v, want StreamEventToolCall", collected[1].Type)
	}
}

func TestStreamEventStrategy_ErrorPath(t *testing.T) {
	expectedErr := errors.New("stream events error")
	s := &StreamEventStrategy{
		OnEvent: func(ev types.StreamEvent) {},
	}

	mock := &mockChatCompleter{
		completeStreamEventsFunc: func(_ context.Context, _ []types.Message, _ []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
			return "", "", nil, expectedErr
		},
	}

	result, err := s.Execute(context.Background(), mock, nil, nil)
	if err != expectedErr {
		t.Errorf("err = %v, want %v", err, expectedErr)
	}
	if result != nil {
		t.Error("result should be nil on error")
	}
}

// ---------------------------------------------------------------------------
// ContentPtr
// ---------------------------------------------------------------------------

func TestContentPtr_WithContentNoToolCalls_ReturnsNonNil(t *testing.T) {
	ptr := ContentPtr("hello", nil)
	if ptr == nil {
		t.Fatal("ContentPtr should return non-nil when content is non-empty")
	}
	if *ptr != "hello" {
		t.Errorf("Content = %q, want %q", *ptr, "hello")
	}
}

func TestContentPtr_EmptyContentWithToolCalls_ReturnsNil(t *testing.T) {
	ptr := ContentPtr("", []types.ToolCall{{ID: "call_1", Type: "function"}})
	if ptr != nil {
		t.Errorf("ContentPtr should return nil when content is empty and toolCalls exist, got %q", *ptr)
	}
}

func TestContentPtr_WithContentAndToolCalls_ReturnsNonNil(t *testing.T) {
	ptr := ContentPtr("response text", []types.ToolCall{{ID: "call_1", Type: "function"}})
	if ptr == nil {
		t.Fatal("ContentPtr should return non-nil when content is non-empty, even with toolCalls")
	}
	if *ptr != "response text" {
		t.Errorf("Content = %q, want %q", *ptr, "response text")
	}
}

func TestContentPtr_EmptyContentNoToolCalls_ReturnsNonNil(t *testing.T) {
	ptr := ContentPtr("", nil)
	if ptr == nil {
		t.Fatal("ContentPtr should return non-nil when both content and toolCalls are empty/nil")
	}
	if *ptr != "" {
		t.Errorf("Content = %q, want empty", *ptr)
	}
}

// ---------------------------------------------------------------------------
// CompletionResult zero values
// ---------------------------------------------------------------------------

func TestCompletionResult_ZeroValues(t *testing.T) {
	r := CompletionResult{}
	if r.Content != "" {
		t.Errorf("Content should be empty string, got %q", r.Content)
	}
	if r.ReasoningContent != "" {
		t.Errorf("ReasoningContent should be empty string, got %q", r.ReasoningContent)
	}
	if len(r.ToolCalls) != 0 {
		t.Errorf("ToolCalls should be nil/empty, got %d", len(r.ToolCalls))
	}
}
