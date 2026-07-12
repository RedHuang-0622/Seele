package interfaces

import (
	"context"
	"errors"
	"fmt"
	"testing"

	types "github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// mock ToolHandler
// ---------------------------------------------------------------------------

type mockHandler struct {
	executeFunc func(ctx context.Context, argsJSON string) (string, error)
}

func (m *mockHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, argsJSON)
	}
	return "ok", nil
}

// ---------------------------------------------------------------------------
// ErrToolUnavailable
// ---------------------------------------------------------------------------

func TestErrToolUnavailable_IsNonNil(t *testing.T) {
	if ErrToolUnavailable == nil {
		t.Fatal("ErrToolUnavailable should not be nil")
	}
}

func TestErrToolUnavailable_ErrorMessage(t *testing.T) {
	msg := ErrToolUnavailable.Error()
	if msg == "" {
		t.Error("ErrToolUnavailable.Error() should return a non-empty message")
	}
}

func TestErrToolUnavailable_ErrorsIs(t *testing.T) {
	// Direct match.
	if !errors.Is(ErrToolUnavailable, ErrToolUnavailable) {
		t.Error("errors.Is should match itself")
	}

	// Wrapped via fmt.Errorf with %w.
	wrapped := fmt.Errorf("network timeout: %w", ErrToolUnavailable)
	if !errors.Is(wrapped, ErrToolUnavailable) {
		t.Error("errors.Is should detect ErrToolUnavailable wrapped with %%w")
	}

	// Wrapped via errors.Join.
	combined := errors.Join(errors.New("something went wrong"), ErrToolUnavailable)
	if !errors.Is(combined, ErrToolUnavailable) {
		t.Error("errors.Is should detect ErrToolUnavailable via errors.Join")
	}
}

func TestErrToolUnavailable_DoesNotMatchOtherErrors(t *testing.T) {
	other := errors.New("some other error")
	if errors.Is(other, ErrToolUnavailable) {
		t.Error("errors.Is should not match an unrelated error")
	}
}

// ---------------------------------------------------------------------------
// ToolEntry
// ---------------------------------------------------------------------------

func TestToolEntry_Construction(t *testing.T) {
	handler := &mockHandler{}
	entry := ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "test_tool",
				Description: "A test tool for unit tests",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		Handler: handler,
		OutputSchema: map[string]interface{}{
			"type": "string",
		},
	}

	if entry.Definition.Type != "function" {
		t.Errorf("Definition.Type = %q, want 'function'", entry.Definition.Type)
	}
	if entry.Definition.Function.Name != "test_tool" {
		t.Errorf("Definition.Function.Name = %q, want 'test_tool'", entry.Definition.Function.Name)
	}
	if entry.Definition.Function.Description != "A test tool for unit tests" {
		t.Errorf("Definition.Function.Description = %q, want 'A test tool for unit tests'",
			entry.Definition.Function.Description)
	}
	if entry.Handler == nil {
		t.Error("Handler should not be nil")
	}
	if entry.OutputSchema == nil {
		t.Error("OutputSchema should not be nil")
	}
	if entry.OutputSchema["type"] != "string" {
		t.Errorf("OutputSchema['type'] = %q, want 'string'", entry.OutputSchema["type"])
	}
}

func TestToolEntry_NilOutputSchema(t *testing.T) {
	entry := ToolEntry{
		Definition: types.Tool{
			Type:     "function",
			Function: types.ToolFunction{Name: "no_schema"},
		},
		Handler:      &mockHandler{},
		OutputSchema: nil,
	}

	if entry.OutputSchema != nil {
		t.Error("OutputSchema should be nil when not set")
	}
}

func TestToolEntry_HandlerCanExecute(t *testing.T) {
	handler := &mockHandler{
		executeFunc: func(_ context.Context, argsJSON string) (string, error) {
			if argsJSON == `{"fail":true}` {
				return "", errors.New("handler error")
			}
			return "handler result: " + argsJSON, nil
		},
	}

	entry := ToolEntry{
		Definition: types.Tool{
			Type:     "function",
			Function: types.ToolFunction{Name: "exec_tool"},
		},
		Handler: handler,
	}

	result, err := entry.Handler.Execute(context.Background(), `{"key":"value"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result != `handler result: {"key":"value"}` {
		t.Errorf("result = %q, want %q", result, `handler result: {"key":"value"}`)
	}

	// Error path.
	_, err = entry.Handler.Execute(context.Background(), `{"fail":true}`)
	if err == nil {
		t.Error("expected error from handler")
	}
	if err.Error() != "handler error" {
		t.Errorf("err = %q, want 'handler error'", err.Error())
	}
}

// ---------------------------------------------------------------------------
// ToolHandler interface contract
// ---------------------------------------------------------------------------

func TestToolHandler_Interface(t *testing.T) {
	// Compile-time check: mockHandler satisfies ToolHandler.
	var h ToolHandler = &mockHandler{}
	_ = h

	// Run the handler.
	result, err := h.Execute(context.Background(), `{"action":"test"}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == "" {
		t.Error("result should not be empty")
	}
}

func TestToolHandler_ContextCancellation(t *testing.T) {
	handler := &mockHandler{
		executeFunc: func(ctx context.Context, argsJSON string) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
				return "completed", nil
			}
		},
	}

	// Normal context.
	result, err := handler.Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute with background context: %v", err)
	}
	if result != "completed" {
		t.Errorf("result = %q, want 'completed'", result)
	}

	// Cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = handler.Execute(ctx, "{}")
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

// ---------------------------------------------------------------------------
// ToolProvider interface (structural check)
// ---------------------------------------------------------------------------

// mockProvider implements ToolProvider for interface testing.
type mockProvider struct {
	name  string
	tools []ToolEntry
}

func (p *mockProvider) ProviderName() string { return p.name }

func (p *mockProvider) Tools() []ToolEntry { return p.tools }

func TestToolProvider_Interface(t *testing.T) {
	var p ToolProvider = &mockProvider{
		name: "test-provider",
		tools: []ToolEntry{
			{
				Definition: types.Tool{
					Type:     "function",
					Function: types.ToolFunction{Name: "tool_1"},
				},
				Handler: &mockHandler{},
			},
		},
	}

	if p.ProviderName() != "test-provider" {
		t.Errorf("ProviderName = %q, want 'test-provider'", p.ProviderName())
	}
	tools := p.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Definition.Function.Name != "tool_1" {
		t.Errorf("tool name = %q, want 'tool_1'", tools[0].Definition.Function.Name)
	}
}
