package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mockCompleter implements types.ChatCompleter for use in tests that need
// to verify the interface without a real ChatClient.
type mockCompleter struct {
	completeFn       func(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
	completeStreamFn func(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error)
}

func (m *mockCompleter) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return m.completeFn(ctx, messages, tools)
}

func (m *mockCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
	return m.completeStreamFn(ctx, messages, tools, onChunk)
}

func (m *mockCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	onChunk := func(delta string) {
		onEvent(types.StreamEvent{Type: types.StreamEventText, Content: delta})
	}
	return m.completeStreamFn(ctx, messages, tools, onChunk)
}

// errorBuildStrategy returns an error from BuildRequest.
type errorBuildStrategy struct{}

func (e *errorBuildStrategy) Name() string                                { return "error-build" }
func (e *errorBuildStrategy) Endpoint() string                             { return "/v1/test" }
func (e *errorBuildStrategy) AuthHeader(apiKey string) (string, string)    { return "Authorization", "Bearer " + apiKey }
func (e *errorBuildStrategy) SSEHeaders() map[string]string                { return nil }
func (e *errorBuildStrategy) BuildRequest(string, []types.Message, []types.Tool, bool, RequestOptions) ([]byte, error) {
	return nil, fmt.Errorf("build error")
}
func (e *errorBuildStrategy) ParseResponse(body []byte) (types.Message, error) { return types.Message{}, nil }
func (e *errorBuildStrategy) ParseSSEEvent(string, string) ([]SSEEvent, error) { return nil, nil }

// captureRequestStrategy records HTTP request details for inspection.
type captureRequestStrategy struct {
	lastURL string
	mu      sync.Mutex
}

func (c *captureRequestStrategy) Name() string                                           { return "capture" }
func (c *captureRequestStrategy) Endpoint() string                                        { return "/chat/completions" }
func (c *captureRequestStrategy) AuthHeader(apiKey string) (string, string)               { return "Authorization", "Bearer " + apiKey }
func (c *captureRequestStrategy) SSEHeaders() map[string]string                           { return map[string]string{"Accept": "text/event-stream"} }
func (c *captureRequestStrategy) BuildRequest(string, []types.Message, []types.Tool, bool, RequestOptions) ([]byte, error) {
	return json.Marshal(map[string]string{"test": "true"})
}
func (c *captureRequestStrategy) ParseResponse(body []byte) (types.Message, error) {
	var resp struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return types.Message{}, err
	}
	msg := types.Message{Role: "assistant", Content: &resp.Content}
	return msg, nil
}
func (c *captureRequestStrategy) ParseSSEEvent(string, string) ([]SSEEvent, error) { return nil, nil }

// errorParseStrategy returns an error from ParseSSEEvent.
type errorParseStrategy struct{}

func (e *errorParseStrategy) Name() string                                { return "error-parse" }
func (e *errorParseStrategy) Endpoint() string                             { return "/chat/completions" }
func (e *errorParseStrategy) AuthHeader(apiKey string) (string, string)    { return "Authorization", "Bearer " + apiKey }
func (e *errorParseStrategy) SSEHeaders() map[string]string                { return nil }
func (e *errorParseStrategy) BuildRequest(string, []types.Message, []types.Tool, bool, RequestOptions) ([]byte, error) {
	return json.Marshal(map[string]string{"test": "true"})
}
func (e *errorParseStrategy) ParseResponse(body []byte) (types.Message, error) { return types.Message{}, nil }
func (e *errorParseStrategy) ParseSSEEvent(string, string) ([]SSEEvent, error) {
	return nil, fmt.Errorf("parse SSE error")
}

// ---------------------------------------------------------------------------
// NewChatClient
// ---------------------------------------------------------------------------

func TestNewChatClientCreatesWithCorrectTimeout(t *testing.T) {
	cfg := types.LLMConfig{Timeout: 30}
	c := NewChatClient(cfg)
	if c.Client.Timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %v", c.Client.Timeout)
	}
}

func TestNewChatClientDefaultTimeout(t *testing.T) {
	cfg := types.LLMConfig{Timeout: 0}
	c := NewChatClient(cfg)
	if c.Client.Timeout != 60*time.Second {
		t.Errorf("expected default 60s timeout, got %v", c.Client.Timeout)
	}
}

// ---------------------------------------------------------------------------
// Builder methods
// ---------------------------------------------------------------------------

func TestChatClientWithAccountPool(t *testing.T) {
	pool := NewAccountPool(&Account{Name: "test"})
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	if c.AccountPool() != pool {
		t.Error("WithAccountPool did not set the pool")
	}
}

func TestChatClientWithStrategy(t *testing.T) {
	s := &captureRequestStrategy{}
	c := NewChatClient(types.LLMConfig{}).WithStrategy(s)
	if c.strategy != s {
		t.Error("WithStrategy did not set the strategy")
	}
}

func TestChatClientSetProvider(t *testing.T) {
	c := NewChatClient(types.LLMConfig{}).SetProvider(ProviderAnthropic)
	if c.Provider() != ProviderAnthropic {
		t.Errorf("expected ProviderAnthropic, got %q", c.Provider())
	}
}

func TestChatClientSetProviderFilter(t *testing.T) {
	c := NewChatClient(types.LLMConfig{}).SetProviderFilter(ProviderAnthropic)
	if c.ProviderFilter() != ProviderAnthropic {
		t.Errorf("expected ProviderAnthropic filter, got %q", c.ProviderFilter())
	}
	// Clear filter
	c.SetProviderFilter("")
	if c.ProviderFilter() != "" {
		t.Errorf("expected empty filter after clearing, got %q", c.ProviderFilter())
	}
}

// ---------------------------------------------------------------------------
// SelectAccount
// ---------------------------------------------------------------------------

func TestChatClientSelectAccountWithPool(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
		&Account{Name: "b", Priority: 2},
	)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	if !c.SelectAccount("b") {
		t.Error("SelectAccount should return true for existing account")
	}
	if c.SelectAccount("nonexistent") {
		t.Error("SelectAccount should return false for non-existing account")
	}
}

func TestChatClientSelectAccountWithoutPool(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})
	if c.SelectAccount("anything") {
		t.Error("SelectAccount should return false when pool is nil")
	}
}

// ---------------------------------------------------------------------------
// AccountPool accessor
// ---------------------------------------------------------------------------

func TestChatClientAccountPoolAccessor(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})
	if c.AccountPool() != nil {
		t.Error("expected nil pool initially")
	}
	pool := NewAccountPool()
	c.WithAccountPool(pool)
	if c.AccountPool() != pool {
		t.Error("AccountPool accessor mismatch")
	}
}

// ---------------------------------------------------------------------------
// effectiveStrategy
// ---------------------------------------------------------------------------

func TestChatClientEffectiveStrategyExplicitPriority(t *testing.T) {
	explicit := &captureRequestStrategy{}
	c := NewChatClient(types.LLMConfig{})
	c.WithStrategy(explicit)
	c.SetProvider(ProviderAnthropic)

	s := c.effectiveStrategy(&Account{Provider: ProviderOpenAI})
	if s != explicit {
		t.Error("explicit strategy should take priority over provider and account")
	}
}

func TestChatClientEffectiveStrategyProviderOverAccount(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})
	c.SetProvider(ProviderAnthropic)

	s := c.effectiveStrategy(&Account{Provider: ProviderOpenAI})
	if s.Name() != "anthropic" {
		t.Errorf("expected 'anthropic' (provider), got %q", s.Name())
	}
}

func TestChatClientEffectiveStrategyAccountProviderFallback(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})

	s := c.effectiveStrategy(&Account{Provider: ProviderAnthropic})
	if s.Name() != "anthropic" {
		t.Errorf("expected 'anthropic' (account provider), got %q", s.Name())
	}
}

func TestChatClientEffectiveStrategyDefaultOpenAI(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})

	s := c.effectiveStrategy(nil)
	if s.Name() != "openai" {
		t.Errorf("expected default 'openai', got %q", s.Name())
	}
}

func TestChatClientEffectiveStrategyFallbackWhenNotRegistered(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})
	c.SetProvider("nonexistent-provider")

	s := c.effectiveStrategy(nil)
	if s.Name() != "openai" {
		t.Errorf("expected fallback 'openai', got %q", s.Name())
	}
}

// ---------------------------------------------------------------------------
// effectiveAccount
// ---------------------------------------------------------------------------

func TestChatClientEffectiveAccountWithoutPool(t *testing.T) {
	c := NewChatClient(types.LLMConfig{})
	if acct := c.effectiveAccount(); acct != nil {
		t.Errorf("expected nil account without pool, got %v", acct)
	}
}

func TestChatClientEffectiveAccountWithPool(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "pooled", Priority: 1},
	)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	acct := c.effectiveAccount()
	if acct == nil || acct.Name != "pooled" {
		t.Errorf("expected 'pooled' account, got %v", acct)
	}
}

func TestChatClientEffectiveAccountWithProviderFilter(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "openai-1", Priority: 1, Provider: ProviderOpenAI},
		&Account{Name: "anthropic-1", Priority: 2, Provider: ProviderAnthropic},
	)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	c.SetProviderFilter(ProviderAnthropic)

	acct := c.effectiveAccount()
	if acct == nil || acct.Name != "anthropic-1" {
		t.Errorf("expected 'anthropic-1' with provider filter, got %v", acct)
	}
}

func TestChatClientEffectiveAccountAllRateLimited(t *testing.T) {
	a1 := &Account{Name: "limited", Priority: 1, MaxRPM: 0}
	a1.Disabled = true
	pool := NewAccountPool(a1)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	if acct := c.effectiveAccount(); acct != nil {
		t.Errorf("expected nil when all disabled, got %v", acct)
	}
}

// ---------------------------------------------------------------------------
// requestOpts
// ---------------------------------------------------------------------------

func TestRequestOptsMergesAccountOverrides(t *testing.T) {
	cfg := types.LLMConfig{
		MaxTokens:   100,
		Temperature: 0.5,
	}
	acct := &Account{
		MaxTokens:   200,
		Temperature: 0.8,
	}
	opts := requestOpts(cfg, acct)
	if opts.MaxTokens != 200 {
		t.Errorf("expected account MaxTokens 200, got %d", opts.MaxTokens)
	}
	if opts.Temperature != 0.8 {
		t.Errorf("expected account Temperature 0.8, got %f", opts.Temperature)
	}
}

func TestRequestOptsUsesConfigWhenNoAccount(t *testing.T) {
	cfg := types.LLMConfig{
		MaxTokens:   100,
		Temperature: 0.5,
	}
	opts := requestOpts(cfg, nil)
	if opts.MaxTokens != 100 {
		t.Errorf("expected cfg MaxTokens 100, got %d", opts.MaxTokens)
	}
	if opts.Temperature != 0.5 {
		t.Errorf("expected cfg Temperature 0.5, got %f", opts.Temperature)
	}
}

func TestRequestOptsAccountZeroDoesNotOverride(t *testing.T) {
	cfg := types.LLMConfig{
		MaxTokens:   100,
		Temperature: 0.5,
	}
	acct := &Account{
		MaxTokens:   0,
		Temperature: 0,
	}
	opts := requestOpts(cfg, acct)
	if opts.MaxTokens != 100 {
		t.Errorf("expected cfg MaxTokens 100 when account has 0, got %d", opts.MaxTokens)
	}
	if opts.Temperature != 0.5 {
		t.Errorf("expected cfg Temperature 0.5 when account has 0, got %f", opts.Temperature)
	}
}

// ---------------------------------------------------------------------------
// effectiveModel
// ---------------------------------------------------------------------------

func TestEffectiveModelAccountOverridesConfig(t *testing.T) {
	cfg := types.LLMConfig{Model: "gpt-4"}
	acct := &Account{Model: "claude-3-opus"}
	if m := effectiveModel(cfg, acct); m != "claude-3-opus" {
		t.Errorf("expected account model 'claude-3-opus', got %q", m)
	}
}

func TestEffectiveModelUsesConfigWhenNoAccountModel(t *testing.T) {
	cfg := types.LLMConfig{Model: "gpt-4"}
	acct := &Account{}
	if m := effectiveModel(cfg, acct); m != "gpt-4" {
		t.Errorf("expected cfg model 'gpt-4', got %q", m)
	}
}

func TestEffectiveModelUsesConfigWhenNilAccount(t *testing.T) {
	cfg := types.LLMConfig{Model: "gpt-4"}
	if m := effectiveModel(cfg, nil); m != "gpt-4" {
		t.Errorf("expected cfg model 'gpt-4', got %q", m)
	}
}

// ---------------------------------------------------------------------------
// Complete -- without pool (uses Cfg directly)
// ---------------------------------------------------------------------------

func TestChatClientCompleteNoPool(t *testing.T) {
	var gotAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "sk-test-key",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	msg := "Hi"
	reply, err := c.Complete(context.Background(), []types.Message{{Role: "user", Content: &msg}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply.Content == nil || *reply.Content != "Hello!" {
		t.Errorf("expected 'Hello!', got %v", reply.Content)
	}
	if reply.Usage == nil {
		t.Fatal("expected usage")
	}
	if reply.Usage.TotalTokens != 8 {
		t.Errorf("expected 8 total tokens, got %d", reply.Usage.TotalTokens)
	}
	if gotAuthHeader != "Bearer sk-test-key" {
		t.Errorf("expected 'Bearer sk-test-key', got %q", gotAuthHeader)
	}
}

// ---------------------------------------------------------------------------
// Complete -- with pool (uses account)
// ---------------------------------------------------------------------------

func TestChatClientCompleteWithPool(t *testing.T) {
	var gotAuthHeader string
	var gotURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		gotAuthHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Anthropic reply"}],"usage":{"input_tokens":5,"output_tokens":3}}`))
	}))
	defer server.Close()

	pool := NewAccountPool(&Account{
		Name:     "ant-main",
		Provider: ProviderAnthropic,
		BaseURL:  server.URL,
		APIKey:   "sk-ant-test",
		Model:    "claude-3-opus",
		Priority: 1,
	})
	cfg := types.LLMConfig{
		APIKey: "sk-should-not-be-used",
		Model:  "should-not-be-used",
	}
	c := NewChatClient(cfg).WithAccountPool(pool)

	msg := "Hello"
	reply, err := c.Complete(context.Background(), []types.Message{{Role: "user", Content: &msg}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply.Content == nil || *reply.Content != "Anthropic reply" {
		t.Errorf("expected 'Anthropic reply', got %v", reply.Content)
	}
	if !strings.Contains(gotURL, "/v1/messages") {
		t.Errorf("expected /v1/messages in URL, got %q", gotURL)
	}
	if gotAuthHeader != "sk-ant-test" {
		t.Errorf("expected 'sk-ant-test', got %q", gotAuthHeader)
	}
}

// ---------------------------------------------------------------------------
// Complete -- all accounts rate-limited
// ---------------------------------------------------------------------------

func TestChatClientCompleteAllRateLimited(t *testing.T) {
	a := &Account{Name: "limited", Priority: 1, Disabled: true}
	pool := NewAccountPool(a)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)

	_, err := c.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error when all accounts rate-limited or disabled")
	}
}

// ---------------------------------------------------------------------------
// Complete -- error paths
// ---------------------------------------------------------------------------

func TestChatClientCompleteBuildRequestError(t *testing.T) {
	c := NewChatClient(types.LLMConfig{}).WithStrategy(&errorBuildStrategy{})
	_, err := c.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error from BuildRequest")
	}
}

func TestChatClientCompleteHTTPError(t *testing.T) {
	// Use a server that closes immediately (connection refused).
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			conn.Close()
		}
	}))
	server.Close() // Close so requests fail

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)
	_, err := c.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected HTTP error with closed server")
	}
}

func TestChatClientCompleteParseResponseError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`invalid json`))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)
	_, err := c.Complete(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error from ParseResponse with invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// SSEState -- newSSEState
// ---------------------------------------------------------------------------

func TestNewSSEStateCreatesEmptyState(t *testing.T) {
	s := newSSEState()
	if s == nil {
		t.Fatal("expected non-nil state")
	}
	if s.sb.Len() != 0 {
		t.Error("expected empty strings.Builder")
	}
	if s.reasoningSB.Len() != 0 {
		t.Error("expected empty reasoning strings.Builder")
	}
	if s.tcMap == nil {
		t.Error("expected non-nil tcMap")
	}
	if s.isToolMode {
		t.Error("expected isToolMode false")
	}
}

// ---------------------------------------------------------------------------
// SSEState -- applySSEEvents
// ---------------------------------------------------------------------------

func TestSSEStateApplySSEEventsTextAccumulates(t *testing.T) {
	state := newSSEState()
	var chunks []string
	events := []SSEEvent{
		{Type: SSEEventText, Content: "Hello "},
		{Type: SSEEventText, Content: "World"},
	}
	state.applySSEEvents(events, func(s string) { chunks = append(chunks, s) })

	if state.sb.String() != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", state.sb.String())
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 onChunk calls, got %d", len(chunks))
	}
	if chunks[0] != "Hello " || chunks[1] != "World" {
		t.Errorf("unexpected chunks: %v", chunks)
	}
}

func TestSSEStateApplySSEEventsToolCallTracked(t *testing.T) {
	state := newSSEState()
	events := []SSEEvent{
		{
			Type:          SSEEventToolCall,
			ToolCallIndex: 0,
			Meta: map[string]any{
				"id":        "call_1",
				"name":      "get_weather",
				"arguments": `{"location": "`,
			},
		},
		{
			Type:          SSEEventToolCall,
			ToolCallIndex: 0,
			Meta: map[string]any{
				"arguments": `New York"}`,
			},
		},
		{
			Type:          SSEEventToolCall,
			ToolCallIndex: 1,
			Meta: map[string]any{
				"id":   "call_2",
				"name": "get_time",
			},
		},
	}
	state.applySSEEvents(events, nil)

	if !state.isToolMode {
		t.Error("expected isToolMode true")
	}
	if len(state.tcMap) != 2 {
		t.Fatalf("expected 2 tool calls in map, got %d", len(state.tcMap))
	}
	tc0 := state.tcMap[0]
	if tc0.ID != "call_1" {
		t.Errorf("expected ID 'call_1', got %q", tc0.ID)
	}
	if tc0.Function.Name != "get_weather" {
		t.Errorf("expected Name 'get_weather', got %q", tc0.Function.Name)
	}
	if tc0.Function.Arguments != `{"location": "New York"}` {
		t.Errorf("expected accumulated arguments, got %q", tc0.Function.Arguments)
	}
	tc1 := state.tcMap[1]
	if tc1.ID != "call_2" {
		t.Errorf("expected ID 'call_2', got %q", tc1.ID)
	}
}

func TestSSEStateApplySSEEventsReasoningAccumulates(t *testing.T) {
	state := newSSEState()
	events := []SSEEvent{
		{Type: SSEEventReasoning, Content: "Let me "},
		{Type: SSEEventReasoning, Content: "think..."},
	}
	state.applySSEEvents(events, nil)

	if state.reasoningSB.String() != "Let me think..." {
		t.Errorf("expected 'Let me think...', got %q", state.reasoningSB.String())
	}
}

func TestSSEStateApplySSEEventsToolModeSuppressesText(t *testing.T) {
	state := newSSEState()
	var chunks []string
	events := []SSEEvent{
		{Type: SSEEventText, Content: "Before tool"},
		{Type: SSEEventToolCall, ToolCallIndex: 0, Meta: map[string]any{"id": "call_1"}},
		{Type: SSEEventText, Content: "Should NOT appear"},
	}
	state.applySSEEvents(events, func(s string) { chunks = append(chunks, s) })

	if state.sb.String() != "Before tool" {
		t.Errorf("expected only 'Before tool', got %q", state.sb.String())
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 onChunk call, got %d", len(chunks))
	}
}

func TestSSEStateApplySSEEventsNilOnChunk(t *testing.T) {
	state := newSSEState()
	events := []SSEEvent{
		{Type: SSEEventText, Content: "Hello"},
	}
	// Should not panic when onChunk is nil.
	state.applySSEEvents(events, nil)
	if state.sb.String() != "Hello" {
		t.Errorf("expected 'Hello', got %q", state.sb.String())
	}
}

func TestSSEStateApplySSEEventsDoneAndErrorIgnored(t *testing.T) {
	state := newSSEState()
	events := []SSEEvent{
		{Type: SSEEventDone},
		{Type: SSEEventError, Content: "something failed"},
	}
	// These should not add to text or tool calls.
	state.applySSEEvents(events, nil)
	if state.sb.Len() != 0 {
		t.Error("expected no text accumulated")
	}
	if state.isToolMode {
		t.Error("isToolMode should still be false")
	}
}

func TestSSEStateApplySSEEventsTextBeforeToolMode(t *testing.T) {
	state := newSSEState()
	var chunks []string
	// Text -> ToolCall -> Text -> ToolCall -> Text
	events := []SSEEvent{
		{Type: SSEEventText, Content: "Text1"},
		{Type: SSEEventToolCall, ToolCallIndex: 0, Meta: map[string]any{"id": "c1"}},
		{Type: SSEEventText, Content: "Ignored"},
		{Type: SSEEventToolCall, ToolCallIndex: 1, Meta: map[string]any{"id": "c2"}},
		{Type: SSEEventText, Content: "Also ignored"},
	}
	state.applySSEEvents(events, func(s string) { chunks = append(chunks, s) })

	if state.sb.String() != "Text1" {
		t.Errorf("expected 'Text1', got %q", state.sb.String())
	}
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk, got %d", len(chunks))
	}
	if len(state.tcMap) != 2 {
		t.Errorf("expected 2 tool calls, got %d", len(state.tcMap))
	}
}

// ---------------------------------------------------------------------------
// buildToolCalls
// ---------------------------------------------------------------------------

func TestBuildToolCallsOrdered(t *testing.T) {
	tcMap := map[int]*types.ToolCall{
		0: {ID: "call_0", Function: types.ToolCallFunction{Name: "foo"}},
		1: {ID: "call_1", Function: types.ToolCallFunction{Name: "bar"}},
		2: {ID: "call_2", Function: types.ToolCallFunction{Name: "baz"}},
	}
	result := buildToolCalls(tcMap)
	if len(result) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(result))
	}
	if result[0].ID != "call_0" {
		t.Errorf("expected first tool call 'call_0', got %q", result[0].ID)
	}
	if result[1].ID != "call_1" {
		t.Errorf("expected second tool call 'call_1', got %q", result[1].ID)
	}
	if result[2].ID != "call_2" {
		t.Errorf("expected third tool call 'call_2', got %q", result[2].ID)
	}
}

func TestBuildToolCallsEmptyMap(t *testing.T) {
	result := buildToolCalls(nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil map, got %d", len(result))
	}
	result = buildToolCalls(map[int]*types.ToolCall{})
	if len(result) != 0 {
		t.Errorf("expected empty result for empty map, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// doStreamRequest
// ---------------------------------------------------------------------------

func TestDoStreamRequestWithoutPool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"test\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	body, err := c.doStreamRequest(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer body.Close()

	data, _ := io.ReadAll(body)
	if len(data) == 0 {
		t.Error("expected response body")
	}
}

func TestDoStreamRequestAllAccountsRateLimited(t *testing.T) {
	a := &Account{Name: "limited", Priority: 1, Disabled: true}
	pool := NewAccountPool(a)
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)

	_, err := c.doStreamRequest(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error when all accounts rate-limited or disabled")
	}
}

func TestDoStreamRequestNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	_, err := c.doStreamRequest(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestDoStreamRequestHTTPClientError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("force error")
	}))
	server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)
	c.Client.Timeout = time.Millisecond

	_, err := c.doStreamRequest(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected HTTP error with closed server")
	}
}

// ---------------------------------------------------------------------------
// CompleteStream -- integration via httptest
// ---------------------------------------------------------------------------

func TestChatClientCompleteStreamText(t *testing.T) {
	var requestBodyStr string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodyStr = string(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Simulate OpenAI SSE stream
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	var chunks []string
	content, reasoning, toolCalls, err := c.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Hello world" {
		t.Errorf("expected 'Hello world', got %q", content)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(toolCalls))
	}
	if len(chunks) != 2 {
		t.Errorf("expected 2 chunks, got %d", len(chunks))
	}
	if !strings.Contains(requestBodyStr, `"stream":true`) {
		t.Error("expected stream:true in request body")
	}
}

func TestChatClientCompleteStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"A\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"B\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	var events []types.StreamEvent
	content, reasoning, toolCalls, err := c.CompleteStreamEvents(context.Background(), nil, nil, func(ev types.StreamEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "AB" {
		t.Errorf("expected 'AB', got %q", content)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(toolCalls))
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
}

func TestChatClientCompleteStreamWithPool(t *testing.T) {
	var gotAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Anthropic-style SSE with event: lines.
		// Text from message_start content blocks + content_block_delta.
		w.Write([]byte("event: message_start\n"))
		w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"content\":[{\"type\":\"text\",\"text\":\"Hello\"}]}}\n"))
		w.Write([]byte("event: content_block_delta\n"))
		w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\" from Anthropic\"}}\n"))
		w.Write([]byte("event: content_block_stop\n"))
		w.Write([]byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n"))
		w.Write([]byte("event: message_stop\n"))
		w.Write([]byte("data: {\"type\":\"message_stop\"}\n"))
	}))
	defer server.Close()

	pool := NewAccountPool(&Account{
		Name:     "ant-main",
		Provider: ProviderAnthropic,
		BaseURL:  server.URL,
		APIKey:   "sk-ant-stream",
		Model:    "claude-3-opus",
		Priority: 1,
	})
	c := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)

	var chunks []string
	content, reasoning, toolCalls, err := c.CompleteStream(context.Background(), nil, nil, func(delta string) {
		chunks = append(chunks, delta)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Hello from Anthropic" {
		t.Errorf("expected 'Hello from Anthropic', got %q", content)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(toolCalls))
	}
	if gotAuthHeader != "sk-ant-stream" {
		t.Errorf("expected 'sk-ant-stream', got %q", gotAuthHeader)
	}
	_ = reasoning
}

// ---------------------------------------------------------------------------
// CompleteStream -- error paths
// ---------------------------------------------------------------------------

func TestChatClientCompleteStreamBuildRequestError(t *testing.T) {
	c := NewChatClient(types.LLMConfig{}).WithStrategy(&errorBuildStrategy{})
	_, _, _, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from BuildRequest in stream")
	}
}

func TestChatClientCompleteStreamHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	_, _, _, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for non-200 HTTP response in stream")
	}
}

func TestChatClientCompleteStreamNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "bad-key",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	_, _, _, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
}

func TestChatClientCompleteStreamParseSSEError(t *testing.T) {
	estr := &errorParseStrategy{}
	c := NewChatClient(types.LLMConfig{}).WithStrategy(estr)

	_, _, _, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error from ParseSSEEvent")
	}
}

// ---------------------------------------------------------------------------
// CompleteStream -- reasoning content
// ---------------------------------------------------------------------------

func TestChatClientCompleteStreamReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// DeepSeek-style reasoning content
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Let me think\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" carefully\"},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Final answer\"},\"finish_reason\":\"stop\"}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	content, reasoning, toolCalls, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Final answer" {
		t.Errorf("expected 'Final answer', got %q", content)
	}
	if reasoning != "Let me think carefully" {
		t.Errorf("expected 'Let me think carefully', got %q", reasoning)
	}
	if len(toolCalls) != 0 {
		t.Errorf("expected 0 tool calls, got %d", len(toolCalls))
	}
}

// ---------------------------------------------------------------------------
// CompleteStream -- tool calls in response
// ---------------------------------------------------------------------------

func TestChatClientCompleteStreamWithToolsInResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// OpenAI tool call stream
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":null,\"function\":{\"arguments\":\"{\\\"loc\\\":\\\"NYC\\\"}\"}}]},\"finish_reason\":null}]}\n"))
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n"))
		w.Write([]byte("data: [DONE]\n"))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	content, reasoning, toolCalls, err := c.CompleteStream(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty content in tool mode, got %q", content)
	}
	if reasoning != "" {
		t.Errorf("expected empty reasoning, got %q", reasoning)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].ID != "call_1" {
		t.Errorf("expected 'call_1', got %q", toolCalls[0].ID)
	}
	if toolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", toolCalls[0].Function.Name)
	}
}

// ---------------------------------------------------------------------------
// Complete -- with tools
// ---------------------------------------------------------------------------

func TestChatClientCompleteWithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"loc\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()

	cfg := types.LLMConfig{
		BaseURL: server.URL,
		APIKey:  "test",
		Model:   "gpt-4",
	}
	c := NewChatClient(cfg)

	tools := []types.Tool{
		{
			Type:     "function",
			Function: types.ToolFunction{Name: "get_weather", Description: "Get weather", Parameters: map[string]interface{}{"type": "object"}},
		},
	}
	msg := "What's the weather?"
	reply, err := c.Complete(context.Background(), []types.Message{{Role: "user", Content: &msg}}, tools)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(reply.ToolCalls))
	}
	if reply.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", reply.ToolCalls[0].Function.Name)
	}
}
