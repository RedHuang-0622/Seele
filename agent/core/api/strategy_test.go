package api

import (
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// mockStrategy implements ProviderStrategy for testing.
type mockStrategy struct {
	name       string
	endpoint   string
	authKey    string
	authValue  string
	sseHeaders map[string]string
}

func (m *mockStrategy) Name() string                              { return m.name }
func (m *mockStrategy) Endpoint() string                           { return m.endpoint }
func (m *mockStrategy) AuthHeader(apiKey string) (string, string)  { return m.authKey, m.authValue }
func (m *mockStrategy) SSEHeaders() map[string]string              { return m.sseHeaders }
func (m *mockStrategy) BuildRequest(model string, messages []types.Message, tools []types.Tool, stream bool, opts RequestOptions) ([]byte, error) {
	return nil, nil
}
func (m *mockStrategy) ParseResponse(body []byte) (types.Message, error) {
	return types.Message{}, nil
}
func (m *mockStrategy) ParseSSEEvent(eventType string, payload string) ([]SSEEvent, error) {
	return nil, nil
}

func TestRegisterProviderStrategyAndGet(t *testing.T) {
	s := &mockStrategy{name: "test-strategy"}
	RegisterProviderStrategy(s)
	defer func() {
		providerStrategiesMu.Lock()
		delete(providerStrategies, "test-strategy")
		providerStrategiesMu.Unlock()
	}()

	got := GetProviderStrategy("test-strategy")
	if got == nil {
		t.Fatal("expected non-nil strategy")
	}
	if got.Name() != "test-strategy" {
		t.Errorf("expected 'test-strategy', got %q", got.Name())
	}
}

func TestGetProviderStrategyNotFound(t *testing.T) {
	got := GetProviderStrategy("nonexistent-strategy")
	if got != nil {
		t.Errorf("expected nil for unregistered strategy, got %v", got)
	}
}

func TestRegisterProviderStrategyDuplicatePanics(t *testing.T) {
	s := &mockStrategy{name: "dup-test-strategy"}
	RegisterProviderStrategy(s)
	defer func() {
		providerStrategiesMu.Lock()
		delete(providerStrategies, "dup-test-strategy")
		providerStrategiesMu.Unlock()
	}()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
	}()
	RegisterProviderStrategy(&mockStrategy{name: "dup-test-strategy"})
}

func TestProviderStrategyNamesReturnsAllRegistered(t *testing.T) {
	// Register a temporary strategy to verify names list includes it.
	names := ProviderStrategyNames()
	foundAnthropic := false
	foundOpenAI := false
	for _, n := range names {
		if n == "anthropic" {
			foundAnthropic = true
		}
		if n == "openai" {
			foundOpenAI = true
		}
	}
	if !foundOpenAI {
		t.Error("expected 'openai' in ProviderStrategyNames")
	}
	if !foundAnthropic {
		t.Error("expected 'anthropic' in ProviderStrategyNames")
	}
}

func TestAnthropicStrategyName(t *testing.T) {
	s := &AnthropicStrategy{}
	if got := s.Name(); got != "anthropic" {
		t.Errorf("expected 'anthropic', got %q", got)
	}
}

func TestAnthropicStrategyEndpoint(t *testing.T) {
	s := &AnthropicStrategy{}
	if got := s.Endpoint(); got != "/v1/messages" {
		t.Errorf("expected '/v1/messages', got %q", got)
	}
}

func TestAnthropicStrategyAuthHeader(t *testing.T) {
	s := &AnthropicStrategy{}
	key, val := s.AuthHeader("sk-ant-test")
	if key != "x-api-key" {
		t.Errorf("expected 'x-api-key', got %q", key)
	}
	if val != "sk-ant-test" {
		t.Errorf("expected 'sk-ant-test', got %q", val)
	}
}

func TestAnthropicStrategySSEHeaders(t *testing.T) {
	s := &AnthropicStrategy{}
	h := s.SSEHeaders()
	if h["Accept"] != "text/event-stream" {
		t.Errorf("expected Accept: text/event-stream, got %q", h["Accept"])
	}
	if h["anthropic-version"] != "2023-06-01" {
		t.Errorf("expected anthropic-version: 2023-06-01, got %q", h["anthropic-version"])
	}
}

func TestAnthropicStrategyBuildRequest(t *testing.T) {
	s := &AnthropicStrategy{}
	msg := "Hello"
	messages := []types.Message{
		{Role: "user", Content: &msg},
	}
	data, err := s.BuildRequest("claude-3-opus", messages, nil, false, RequestOptions{MaxTokens: 1000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty request body")
	}
}

func TestAnthropicStrategyBuildRequestStream(t *testing.T) {
	s := &AnthropicStrategy{}
	msg := "Hello"
	messages := []types.Message{
		{Role: "user", Content: &msg},
	}
	data, err := s.BuildRequest("claude-3-opus", messages, nil, true, RequestOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty request body")
	}
}

func TestOpenAIStrategyName(t *testing.T) {
	s := &OpenAIStrategy{}
	if got := s.Name(); got != "openai" {
		t.Errorf("expected 'openai', got %q", got)
	}
}

func TestOpenAIStrategyEndpoint(t *testing.T) {
	s := &OpenAIStrategy{}
	if got := s.Endpoint(); got != "/chat/completions" {
		t.Errorf("expected '/chat/completions', got %q", got)
	}
}

func TestOpenAIStrategyAuthHeader(t *testing.T) {
	s := &OpenAIStrategy{}
	key, val := s.AuthHeader("sk-test")
	if key != "Authorization" {
		t.Errorf("expected 'Authorization', got %q", key)
	}
	if val != "Bearer sk-test" {
		t.Errorf("expected 'Bearer sk-test', got %q", val)
	}
}

func TestOpenAIStrategySSEHeaders(t *testing.T) {
	s := &OpenAIStrategy{}
	h := s.SSEHeaders()
	if h["Accept"] != "text/event-stream" {
		t.Errorf("expected Accept: text/event-stream, got %q", h["Accept"])
	}
}

func TestOpenAIStrategyBuildRequest(t *testing.T) {
	s := &OpenAIStrategy{}
	msg := "Hello"
	messages := []types.Message{
		{Role: "user", Content: &msg},
	}
	data, err := s.BuildRequest("gpt-4", messages, nil, false, RequestOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty request body")
	}
}

func TestOpenAIStrategyBuildRequestStream(t *testing.T) {
	s := &OpenAIStrategy{}
	msg := "Hello"
	messages := []types.Message{
		{Role: "user", Content: &msg},
	}
	data, err := s.BuildRequest("gpt-4", messages, nil, true, RequestOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty request body")
	}
}

func TestOpenAIStrategyBuildRequestWithTools(t *testing.T) {
	s := &OpenAIStrategy{}
	msg := "What's the weather?"
	messages := []types.Message{
		{Role: "user", Content: &msg},
	}
	tools := []types.Tool{
		{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
	}
	data, err := s.BuildRequest("gpt-4", messages, tools, false, RequestOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty request body with tools")
	}
}

func TestOpenAIStrategyParseResponse(t *testing.T) {
	s := &OpenAIStrategy{}
	body := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"Hello!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	msg, err := s.ParseResponse([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content == nil || *msg.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %v", msg.Content)
	}
	if msg.Usage == nil {
		t.Fatal("expected usage")
	}
	if msg.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens 15, got %d", msg.Usage.TotalTokens)
	}
}

func TestOpenAIStrategyParseResponseError(t *testing.T) {
	s := &OpenAIStrategy{}
	body := `{"error":{"message":"Rate limit","type":"rate_limit","code":"rate_limit_exceeded"}}`
	_, err := s.ParseResponse([]byte(body))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
}

func TestOpenAIStrategyParseResponseEmptyChoices(t *testing.T) {
	s := &OpenAIStrategy{}
	body := `{"id":"chatcmpl-1","choices":[]}`
	_, err := s.ParseResponse([]byte(body))
	if err == nil {
		t.Fatal("expected error for empty choices")
	}
}

func TestOpenAIStrategyParseResponseToolCalls(t *testing.T) {
	s := &OpenAIStrategy{}
	body := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"loc\":\"NYC\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	msg, err := s.ParseResponse([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "call_1" {
		t.Errorf("expected 'call_1', got %q", msg.ToolCalls[0].ID)
	}
	if msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", msg.ToolCalls[0].Function.Name)
	}
}

func TestAnthropicStrategyParseResponse(t *testing.T) {
	s := &AnthropicStrategy{}
	body := `{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"text","text":"Hello!"}],"usage":{"input_tokens":10,"output_tokens":5}}`
	msg, err := s.ParseResponse([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Content == nil || *msg.Content != "Hello!" {
		t.Errorf("expected content 'Hello!', got %v", msg.Content)
	}
	if msg.Usage == nil {
		t.Fatal("expected usage")
	}
	if msg.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens 15, got %d", msg.Usage.TotalTokens)
	}
}

func TestAnthropicStrategyParseResponseError(t *testing.T) {
	s := &AnthropicStrategy{}
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`
	_, err := s.ParseResponse([]byte(body))
	if err == nil {
		t.Fatal("expected error for API error response")
	}
}

func TestAnthropicStrategyParseResponseToolUse(t *testing.T) {
	s := &AnthropicStrategy{}
	body := `{"id":"msg_01","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"loc":"NYC"}}],"usage":{"input_tokens":10,"output_tokens":5}}`
	msg, err := s.ParseResponse([]byte(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "toolu_1" {
		t.Errorf("expected 'toolu_1', got %q", msg.ToolCalls[0].ID)
	}
	if msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected 'get_weather', got %q", msg.ToolCalls[0].Function.Name)
	}
}

func TestRequestOptionsFields(t *testing.T) {
	opts := RequestOptions{
		MaxTokens:   2000,
		Temperature: 0.5,
	}
	if opts.MaxTokens != 2000 {
		t.Errorf("expected MaxTokens 2000, got %d", opts.MaxTokens)
	}
	if opts.Temperature != 0.5 {
		t.Errorf("expected Temperature 0.5, got %f", opts.Temperature)
	}
}

func TestInitRegistrationsAnthropic(t *testing.T) {
	s := GetProviderStrategy("anthropic")
	if s == nil {
		t.Fatal("expected 'anthropic' strategy registered by init()")
	}
	if _, ok := s.(*AnthropicStrategy); !ok {
		t.Errorf("expected *AnthropicStrategy, got %T", s)
	}
}

func TestInitRegistrationsOpenAI(t *testing.T) {
	s := GetProviderStrategy("openai")
	if s == nil {
		t.Fatal("expected 'openai' strategy registered by init()")
	}
	if _, ok := s.(*OpenAIStrategy); !ok {
		t.Errorf("expected *OpenAIStrategy, got %T", s)
	}
}
