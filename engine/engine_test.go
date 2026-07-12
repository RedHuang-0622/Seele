// Package engine tests provide smoke tests for the Engine ReAct loop.
//
// Tests use an HTTP mock LLM server (no real API key needed) and a minimal
// agent.Agent created with agent.New(). All tests verify the public
// Chat() / ChatStream() API with various response scenarios.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/types"
)

// =============================================================================
// mockLLMResponse — preset response for one LLM call
// =============================================================================

type mockLLMResponse struct {
	content   string
	toolCalls []types.ToolCall
}

// =============================================================================
// mockLLMServer — OpenAI-compatible /chat/completions mock
//
// Supports both sync (JSON) and streaming (SSE) responses.
// Responses are consumed from a FIFO queue; when empty the defaultText is used.
// =============================================================================

type mockLLMServer struct {
	server      *httptest.Server
	mu          sync.Mutex
	queue       []mockLLMResponse
	defaultText string
}

func newMockLLMServer() *mockLLMServer {
	m := &mockLLMServer{defaultText: "Hello from mock LLM"}
	m.server = httptest.NewServer(http.HandlerFunc(m.handler))
	return m
}

func (m *mockLLMServer) URL() string { return m.server.URL }

func (m *mockLLMServer) Close() { m.server.Close() }

func (m *mockLLMServer) EnqueueText(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResponse{content: content})
}

func (m *mockLLMServer) EnqueueToolCalls(tcs []types.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResponse{toolCalls: tcs})
}

func (m *mockLLMServer) pop() mockLLMResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) > 0 {
		resp := m.queue[0]
		m.queue = m.queue[1:]
		return resp
	}
	return mockLLMResponse{content: m.defaultText}
}

// handler serves POST /chat/completions for both sync and streaming modes.
func (m *mockLLMServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []types.Message `json:"messages"`
		Tools    []types.Tool    `json:"tools"`
		Stream   bool            `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	resp := m.pop()

	// Build the assistant message payload
	msg := map[string]interface{}{
		"role": "assistant",
	}
	if len(resp.toolCalls) > 0 {
		msg["content"] = nil
		msg["tool_calls"] = resp.toolCalls
	} else {
		msg["content"] = resp.content
	}

	if req.Stream {
		// ── Streaming (SSE) mode ─────────────────────────────────────
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		if len(resp.toolCalls) > 0 {
			// One SSE frame per tool call delta
			for i, tc := range resp.toolCalls {
				deltaData, _ := json.Marshal(map[string]interface{}{
					"choices": []map[string]interface{}{
						{
							"index": 0,
							"delta": map[string]interface{}{
								"tool_calls": []map[string]interface{}{
									{
										"index": i,
										"id":    tc.ID,
										"type":  tc.Type,
										"function": map[string]interface{}{
											"name":      tc.Function.Name,
											"arguments": tc.Function.Arguments,
										},
									},
								},
							},
							"finish_reason": nil,
						},
					},
				})
				fmt.Fprintf(w, "data: %s\n\n", deltaData)
				flusher.Flush()
			}
		} else {
			// Text content frame
			textData, _ := json.Marshal(map[string]interface{}{
				"choices": []map[string]interface{}{
					{
						"index": 0,
						"delta": map[string]interface{}{
							"content": resp.content,
						},
						"finish_reason": nil,
					},
				},
			})
			fmt.Fprintf(w, "data: %s\n\n", textData)
			flusher.Flush()
		}

		// Stream end marker
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}

	// ── Sync mode (JSON) ─────────────────────────────────────────────
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id": "mock",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
	})
}

// =============================================================================
// Helpers
// =============================================================================

// newTestAgent creates a minimal Agent with its LLM client pointed at mockURL.
func newTestAgent(mockURL string) (*agent.Agent, error) {
	return agent.New(agent.Options{
		LLMConfig: types.LLMConfig{
			BaseURL: mockURL,
			APIKey:  "test-key",
			Model:   "test-model",
		},
		// Speed up tests by reducing the hub startup wait
		HubStartupDelay: 10,
	})
}

// =============================================================================
// Tests
// =============================================================================

// TestEngine_Chat_Basic verifies that Chat() returns the text response from
// the mock LLM.
func TestEngine_Chat_Basic(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	mockSrv.EnqueueText("Hello, I am a mock assistant.")

	eng := New(a)
	reply, err := eng.Chat(context.Background(), "Hello")
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}
	if reply == "" {
		t.Fatal("Chat() returned empty reply")
	}
	t.Logf("Chat reply: %q", reply)

	// Verify history is preserved after the call
	hist := eng.History()
	if len(hist) == 0 {
		t.Fatal("expected non-empty history after Chat()")
	}
}

// TestEngine_ChatStream_Basic verifies that ChatStream() calls onChunk for
// each text token and returns the final assembled reply.
func TestEngine_ChatStream_Basic(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	var mu sync.Mutex
	var chunks []string
	onChunk := func(chunk string) {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
	}

	mockSrv.EnqueueText("Streamed response text.")

	eng := New(a)
	reply, err := eng.ChatStream(context.Background(), "Hello stream", onChunk)
	if err != nil {
		t.Fatalf("ChatStream() failed: %v", err)
	}
	if reply == "" {
		t.Fatal("ChatStream() returned empty reply")
	}

	mu.Lock()
	chunkCount := len(chunks)
	mu.Unlock()
	if chunkCount == 0 {
		t.Fatal("onChunk was never called")
	}
	t.Logf("ChatStream reply: %q, chunks received: %d", reply, chunkCount)
}

// TestEngine_Chat_WithToolCalls verifies that Chat() correctly handles a
// tool_call round-trip: mock returns tool_calls first, then text.
func TestEngine_Chat_WithToolCalls(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	// Register a simple inline tool for the ReAct loop to dispatch
	a.RegisterTool(
		"echo_tool",
		"A test tool that echoes input",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(ctx context.Context, argsJSON string) (string, error) {
			return `{"status":"ok","echo":` + argsJSON + `}`, nil
		},
	)

	// Queue: first response is a tool_call, second is the final text
	mockSrv.EnqueueToolCalls([]types.ToolCall{
		{
			ID:   "call_echo_1",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      "echo_tool",
				Arguments: `{"input":"hello"}`,
			},
		},
	})
	mockSrv.EnqueueText("Tool call completed successfully.")

	eng := New(a)
	reply, err := eng.Chat(context.Background(), "Use echo tool")
	if err != nil {
		t.Fatalf("Chat() with tool calls failed: %v", err)
	}
	if reply == "" {
		t.Fatal("Chat() with tool calls returned empty reply")
	}
	t.Logf("Tool call Chat reply: %q", reply)
}

// TestEngine_Chat_EmptyInput verifies the engine handles an empty user
// input string without panicking and returns a response.
func TestEngine_Chat_EmptyInput(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	mockSrv.EnqueueText("Received empty input.")

	eng := New(a)
	reply, err := eng.Chat(context.Background(), "")
	if err != nil {
		t.Fatalf("Chat() with empty input failed: %v", err)
	}
	if reply == "" {
		t.Fatal("Chat() with empty input returned empty reply")
	}
	t.Logf("Empty input Chat reply: %q", reply)
}

// TestEngine_ClearHistory verifies that ClearHistory retains system messages
// but clears the rest.
func TestEngine_ClearHistory(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	mockSrv.EnqueueText("First response")
	mockSrv.EnqueueText("Second response")

	eng := New(a, WithSystemPrompt("You are a helpful assistant."))

	// Make two calls to build up history
	_, err = eng.Chat(context.Background(), "Hello")
	if err != nil {
		t.Fatalf("first Chat() failed: %v", err)
	}
	_, err = eng.Chat(context.Background(), "Again")
	if err != nil {
		t.Fatalf("second Chat() failed: %v", err)
	}

	histBefore := eng.History()
	if len(histBefore) < 3 {
		t.Fatalf("expected at least 3 history entries, got %d", len(histBefore))
	}

	eng.ClearHistory()
	histAfter := eng.History()

	// Should only contain system message
	if len(histAfter) != 1 {
		t.Fatalf("expected 1 history entry after ClearHistory, got %d", len(histAfter))
	}
	if histAfter[0].Role != "system" {
		t.Fatalf("expected remaining entry to be system role, got %q", histAfter[0].Role)
	}
}

// =============================================================================
// Engine + Tracer 集成测试
// =============================================================================

// TestEngine_Tracer_SimpleText 验证简单的文本回复生成完整 Trace Tree。
func TestEngine_Tracer_SimpleText(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	mockSrv.EnqueueText("Hello from mock.")
	tr := tracer.NewSimpleTracer()

	eng := New(a, WithTracer(tr), WithSystemPrompt("You are helpful."))

	reply, err := eng.Chat(context.Background(), "Say hello")
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}
	if reply == "" {
		t.Fatal("Chat() returned empty reply")
	}

	// Verify the tracer works via ExportTrace
	tree := eng.ExportTrace()
	if tree == nil {
		t.Fatal("ExportTrace returned nil")
	}
	if tree.Root == nil {
		t.Fatal("Trace tree has nil root")
	}
	t.Logf("Trace tree: %s", tree.String())

	if tree.TraceID == "" {
		t.Error("TraceID should be non-empty")
	}
	if tree.Root.Kind != tracer.SpanReActLoop {
		t.Errorf("Root kind should be %s, got %s", tracer.SpanReActLoop, tree.Root.Kind)
	}
	if tree.Root.Duration <= 0 {
		t.Errorf("Root Duration should be positive, got %v", tree.Root.Duration)
	}
	if len(tree.Root.Children) < 1 {
		t.Errorf("Root should have at least 1 child, got %d", len(tree.Root.Children))
	}
}

// TestEngine_Tracer_WithToolCalls 验证工具调度场景的 Trace Tree。
func TestEngine_Tracer_WithToolCalls(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	a.RegisterTool(
		"echo_tool", "A test tool that echoes input",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(ctx context.Context, argsJSON string) (string, error) {
			return `{"status":"ok","echo":` + argsJSON + `}`, nil
		},
	)

	mockSrv.EnqueueToolCalls([]types.ToolCall{
		{ID: "call_echo_1", Type: "function",
			Function: types.ToolCallFunction{Name: "echo_tool", Arguments: `{"input":"hello"}`}},
	})
	mockSrv.EnqueueText("Tool call completed successfully.")

	tr := tracer.NewSimpleTracer()
	eng := New(a, WithTracer(tr), WithSystemPrompt("You are helpful."))

	reply, err := eng.Chat(context.Background(), "Use echo tool")
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}
	if reply == "" {
		t.Fatal("empty reply")
	}

	tree := eng.ExportTrace()
	if tree.Root == nil {
		t.Fatal("nil root in tool call test")
	}
	if len(tree.Root.Children) < 2 {
		t.Fatalf("expected >=2 children (1 LLM + 1 tool), got %d", len(tree.Root.Children))
	}

	// Verify tool dispatch span
	hasToolSpan := false
	for _, c := range tree.Root.Children {
		if c.Kind == tracer.SpanToolDispatch && c.Attrs["tool"] == "echo_tool" {
			hasToolSpan = true
			break
		}
	}
	if !hasToolSpan {
		t.Error("No tool_dispatch span for echo_tool")
	}

	t.Logf("Trace tree:\n%s", tree.String())
}

// TestEngine_Tracer_NoopIsDefault 验证默认 NoopTracer 不产生追踪数据。
func TestEngine_Tracer_NoopIsDefault(t *testing.T) {
	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	mockSrv.EnqueueText("Noop test")

	// 不传 WithTracer = NoopTracer
	eng := New(a, WithSystemPrompt("You are helpful."))
	_, err = eng.Chat(context.Background(), "test")
	if err != nil {
		t.Fatalf("Chat() failed: %v", err)
	}

	tree := eng.ExportTrace()
	// NoopTracer 返回空的 Tree
	if tree.Root != nil {
		t.Error("NoopTracer should return nil Root")
	}
}
