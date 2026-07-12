package types

import (
	"context"
	"encoding/json"
	"testing"
)

// test helpers
func ptrStr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Message
// ---------------------------------------------------------------------------

func TestMessage_ContentNil(t *testing.T) {
	m := Message{Role: "assistant"}
	if m.Content != nil {
		t.Error("Content should be nil by default")
	}
	if m.Role != "assistant" {
		t.Errorf("Role = %q, want %q", m.Role, "assistant")
	}
}

func TestMessage_ContentSet(t *testing.T) {
	s := "Hello, world!"
	m := Message{Role: "user", Content: &s}
	if m.Content == nil {
		t.Fatal("Content should be non-nil after setting")
	}
	if *m.Content != "Hello, world!" {
		t.Errorf("Content = %q, want %q", *m.Content, "Hello, world!")
	}
}

func TestMessage_JSONOmitEmptyContent(t *testing.T) {
	m := Message{Role: "assistant"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["content"]; ok {
		t.Error("nil Content should be omitted from JSON (omitempty)")
	}
	if parsed["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", parsed["role"])
	}
}

func TestMessage_UsageJSONOmitted(t *testing.T) {
	m := Message{
		Role:    "assistant",
		Content: ptrStr("ok"),
		Usage:   &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["usage"]; ok {
		t.Error("Usage field should be omitted from JSON (json:\"-\")")
	}
	if _, ok := parsed["input_tokens"]; ok {
		t.Error("Usage sub-fields should not appear at Message level")
	}
	// Verify that the rest of the message still serializes
	if parsed["role"] != "assistant" {
		t.Errorf("role = %v", parsed["role"])
	}
}

func TestMessage_JSONRoundTrip(t *testing.T) {
	content := "Hello"
	orig := Message{
		Role:             "assistant",
		Content:          &content,
		ReasoningContent: "thinking step by step",
		ToolCalls: []ToolCall{
			{
				ID:   "call_abc",
				Type: "function",
				Function: ToolCallFunction{
					Name:      "get_weather",
					Arguments: `{"city":"Beijing"}`,
				},
			},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal error: %v\nJSON: %s", err, data)
	}
	if restored.Role != "assistant" {
		t.Errorf("Role = %q", restored.Role)
	}
	if restored.Content == nil {
		t.Fatal("Content should be non-nil after round-trip")
	}
	if *restored.Content != *orig.Content {
		t.Errorf("Content = %q, want %q", *restored.Content, *orig.Content)
	}
	if restored.ReasoningContent != "thinking step by step" {
		t.Errorf("ReasoningContent = %q", restored.ReasoningContent)
	}
	if len(restored.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(restored.ToolCalls))
	}
	if restored.ToolCalls[0].ID != "call_abc" {
		t.Errorf("ToolCall.ID = %q", restored.ToolCalls[0].ID)
	}
	if restored.ToolCalls[0].Type != "function" {
		t.Errorf("ToolCall.Type = %q", restored.ToolCalls[0].Type)
	}
	if restored.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("ToolCall.Function.Name = %q", restored.ToolCalls[0].Function.Name)
	}
}

func TestMessage_ToolMessageRoundTrip(t *testing.T) {
	content := `{"result":"ok"}`
	orig := Message{
		Role:       "tool",
		Content:    &content,
		ToolCallID: "call_xyz",
		Name:       "search_tool",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal error: %v\nJSON: %s", err, data)
	}
	if restored.Role != "tool" {
		t.Errorf("Role = %q", restored.Role)
	}
	if restored.ToolCallID != "call_xyz" {
		t.Errorf("ToolCallID = %q", restored.ToolCallID)
	}
	if restored.Name != "search_tool" {
		t.Errorf("Name = %q", restored.Name)
	}
	if restored.Content == nil {
		t.Fatal("Content should be non-nil")
	}
	if *restored.Content != `{"result":"ok"}` {
		t.Errorf("Content = %q", *restored.Content)
	}
}

// ---------------------------------------------------------------------------
// ToolCall / ToolCallFunction
// ---------------------------------------------------------------------------

func TestToolCall_Construction(t *testing.T) {
	tc := ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: ToolCallFunction{
			Name:      "search",
			Arguments: `{"q":"test"}`,
		},
	}
	if tc.ID != "call_1" {
		t.Errorf("ID = %q", tc.ID)
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q (want 'function')", tc.Type)
	}
	if tc.Function.Name != "search" {
		t.Errorf("Function.Name = %q", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"q":"test"}` {
		t.Errorf("Function.Arguments = %q", tc.Function.Arguments)
	}
}

func TestToolCall_JSONRoundTrip(t *testing.T) {
	orig := ToolCall{
		ID:   "tc_1",
		Type: "function",
		Function: ToolCallFunction{
			Name:      "my_func",
			Arguments: `{"x":1}`,
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored ToolCall
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ID != "tc_1" {
		t.Errorf("ID = %q", restored.ID)
	}
	if restored.Type != "function" {
		t.Errorf("Type = %q", restored.Type)
	}
	if restored.Function.Name != "my_func" {
		t.Errorf("Function.Name = %q", restored.Function.Name)
	}
	if restored.Function.Arguments != `{"x":1}` {
		t.Errorf("Function.Arguments = %q", restored.Function.Arguments)
	}
}

func TestToolCall_DefaultZero(t *testing.T) {
	var tc ToolCall
	if tc.ID != "" {
		t.Error("zero ToolCall should have empty ID")
	}
	if tc.Type != "" {
		t.Error("zero ToolCall should have empty Type")
	}
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func TestUsage_JSONTags(t *testing.T) {
	u := Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		key  string
		want float64
	}{
		{"input_tokens", 100},
		{"output_tokens", 50},
		{"total_tokens", 150},
	}
	for _, tc := range tests {
		v, ok := parsed[tc.key]
		if !ok {
			t.Errorf("missing JSON key %q", tc.key)
			continue
		}
		fv, ok := v.(float64)
		if !ok {
			t.Errorf("key %q is not a number: %T %v", tc.key, v, v)
			continue
		}
		if fv != tc.want {
			t.Errorf("%s = %v, want %v", tc.key, fv, tc.want)
		}
	}
}

func TestUsage_ZeroValues(t *testing.T) {
	u := Usage{}
	if u.PromptTokens != 0 {
		t.Errorf("PromptTokens = %d", u.PromptTokens)
	}
	if u.CompletionTokens != 0 {
		t.Errorf("CompletionTokens = %d", u.CompletionTokens)
	}
	if u.TotalTokens != 0 {
		t.Errorf("TotalTokens = %d", u.TotalTokens)
	}
}

func TestUsage_JSONRoundTrip(t *testing.T) {
	orig := Usage{PromptTokens: 20, CompletionTokens: 15, TotalTokens: 35}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored Usage
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored != orig {
		t.Errorf("round-trip: %+v vs %+v", restored, orig)
	}
}

// ---------------------------------------------------------------------------
// Tool / ToolFunction
// ---------------------------------------------------------------------------

func TestTool_Construction(t *testing.T) {
	tl := Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "my_tool",
			Description: "A test tool",
			Parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
	if tl.Type != "function" {
		t.Errorf("Type = %q", tl.Type)
	}
	if tl.Function.Name != "my_tool" {
		t.Errorf("Function.Name = %q", tl.Function.Name)
	}
	if tl.Function.Description != "A test tool" {
		t.Errorf("Function.Description = %q", tl.Function.Description)
	}
	if tl.Function.Parameters["type"] != "object" {
		t.Errorf("Parameters['type'] = %v", tl.Function.Parameters["type"])
	}
}

func TestToolFunction_JSONRoundTrip(t *testing.T) {
	orig := ToolFunction{
		Name:        "search",
		Description: "Search the web",
		Parameters: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored ToolFunction
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Name != "search" {
		t.Errorf("Name = %q", restored.Name)
	}
	if restored.Description != "Search the web" {
		t.Errorf("Description = %q", restored.Description)
	}
	if restored.Parameters["type"] != "object" {
		t.Errorf("Parameters['type'] = %v", restored.Parameters["type"])
	}
	props, ok := restored.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Parameters['properties'] is not a map")
	}
	q, ok := props["q"].(map[string]interface{})
	if !ok {
		t.Fatal("properties['q'] is not a map")
	}
	if q["type"] != "string" {
		t.Errorf("properties.q.type = %v", q["type"])
	}
}

func TestTool_JSONRoundTrip(t *testing.T) {
	orig := Tool{
		Type: "function",
		Function: ToolFunction{
			Name:        "tool_a",
			Description: "desc",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	var restored Tool
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Type != "function" {
		t.Errorf("Type = %q", restored.Type)
	}
	if restored.Function.Name != "tool_a" {
		t.Errorf("Function.Name = %q", restored.Function.Name)
	}
}

// ---------------------------------------------------------------------------
// StreamEvent / StreamEventType
// ---------------------------------------------------------------------------

func TestStreamEventType_Values(t *testing.T) {
	tests := []struct {
		val  StreamEventType
		want int
		name string
	}{
		{StreamEventText, 0, "StreamEventText"},
		{StreamEventToolCall, 1, "StreamEventToolCall"},
		{StreamEventToolResult, 2, "StreamEventToolResult"},
		{StreamEventReasoning, 3, "StreamEventReasoning"},
		{StreamEventError, 4, "StreamEventError"},
		{StreamEventDone, 5, "StreamEventDone"},
	}
	for _, tc := range tests {
		if int(tc.val) != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.val, tc.want)
		}
	}
}

func TestStreamEvent_Construction(t *testing.T) {
	ev := StreamEvent{
		Type:    StreamEventToolCall,
		Content: "get_weather",
		Index:   0,
		Meta:    map[string]any{"id": "call_1", "idx": 0},
	}
	if ev.Type != StreamEventToolCall {
		t.Errorf("Type = %v", ev.Type)
	}
	if ev.Content != "get_weather" {
		t.Errorf("Content = %q", ev.Content)
	}
	if ev.Index != 0 {
		t.Errorf("Index = %d", ev.Index)
	}
	if ev.Meta["id"] != "call_1" {
		t.Errorf("Meta['id'] = %v", ev.Meta["id"])
	}
}

func TestStreamEvent_DefaultZero(t *testing.T) {
	var ev StreamEvent
	if ev.Type != 0 {
		t.Errorf("zero StreamEvent.Type = %d, want 0 (StreamEventText)", ev.Type)
	}
	if ev.Content != "" {
		t.Errorf("zero StreamEvent.Content = %q, want empty", ev.Content)
	}
	if ev.Index != 0 {
		t.Errorf("zero StreamEvent.Index = %d, want 0", ev.Index)
	}
	if ev.Meta != nil {
		t.Errorf("zero StreamEvent.Meta = %v, want nil", ev.Meta)
	}
}

func TestStreamEvent_AllTypesConstruction(t *testing.T) {
	events := []StreamEvent{
		{Type: StreamEventText, Content: "delta chunk"},
		{Type: StreamEventToolCall, Content: "tool_name", Index: 1},
		{Type: StreamEventToolResult, Content: `{"ok":true}`},
		{Type: StreamEventReasoning, Content: "thinking..."},
		{Type: StreamEventError, Content: "timeout"},
		{Type: StreamEventDone, Content: ""},
	}
	for _, ev := range events {
		switch ev.Type {
		case StreamEventText:
			if ev.Content != "delta chunk" {
				t.Errorf("StreamEventText content = %q", ev.Content)
			}
		case StreamEventToolCall:
			if ev.Index != 1 {
				t.Errorf("StreamEventToolCall index = %d", ev.Index)
			}
		case StreamEventError:
			if ev.Content != "timeout" {
				t.Errorf("StreamEventError content = %q", ev.Content)
			}
		case StreamEventDone:
			// no special checks
		}
	}
}

// ---------------------------------------------------------------------------
// SkillInfo
// ---------------------------------------------------------------------------

func TestSkillInfo_Fields(t *testing.T) {
	si := SkillInfo{
		Name:        "test-skill",
		Description: "A test skill for testing",
		Method:      "GET",
		Addr:        ":8080",
	}
	if si.Name != "test-skill" {
		t.Errorf("Name = %q", si.Name)
	}
	if si.Description != "A test skill for testing" {
		t.Errorf("Description = %q", si.Description)
	}
	if si.Method != "GET" {
		t.Errorf("Method = %q", si.Method)
	}
	if si.Addr != ":8080" {
		t.Errorf("Addr = %q", si.Addr)
	}
}

func TestSkillInfo_DefaultZero(t *testing.T) {
	var si SkillInfo
	if si.Name != "" {
		t.Error("zero SkillInfo should have empty Name")
	}
	if si.Description != "" {
		t.Error("zero SkillInfo should have empty Description")
	}
	if si.Method != "" {
		t.Error("zero SkillInfo should have empty Method")
	}
	if si.Addr != "" {
		t.Error("zero SkillInfo should have empty Addr")
	}
}

// ---------------------------------------------------------------------------
// AppConfig / LLMConfig / HubConfig / RegistryConfig
// ---------------------------------------------------------------------------

func TestAppConfig_ZeroValues(t *testing.T) {
	var cfg AppConfig
	if cfg.LLM.BaseURL != "" {
		t.Errorf("LLM.BaseURL should be empty")
	}
	if cfg.Hub.Addr != "" {
		t.Errorf("Hub.Addr should be empty")
	}
	if cfg.Registry.Path != "" {
		t.Errorf("Registry.Path should be empty")
	}
}

func TestAppConfig_FullConstruction(t *testing.T) {
	cfg := AppConfig{
		LLM: LLMConfig{
			BaseURL: "https://api.example.com", APIKey: "sk-key",
			Model: "gpt-4", MaxTokens: 4096, Timeout: 30, Temperature: 0.5,
		},
		Hub: HubConfig{
			Addr: ":9090", StartupDelayMs: 200,
		},
		Registry: RegistryConfig{
			Path: "./cfg/registry.yaml",
		},
	}
	if cfg.LLM.BaseURL != "https://api.example.com" {
		t.Errorf("LLM.BaseURL = %q", cfg.LLM.BaseURL)
	}
	if cfg.Hub.Addr != ":9090" {
		t.Errorf("Hub.Addr = %q", cfg.Hub.Addr)
	}
	if cfg.Registry.Path != "./cfg/registry.yaml" {
		t.Errorf("Registry.Path = %q", cfg.Registry.Path)
	}
}

func TestLLMConfig_Fields(t *testing.T) {
	cfg := LLMConfig{
		BaseURL: "https://api.test.com",
		APIKey:  "sk-test",
		Model:   "claude-3",
	}
	if cfg.BaseURL != "https://api.test.com" {
		t.Errorf("BaseURL = %q", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Model != "claude-3" {
		t.Errorf("Model = %q", cfg.Model)
	}
}

func TestHubConfig_Fields(t *testing.T) {
	cfg := HubConfig{Addr: ":50051", StartupDelayMs: 150}
	if cfg.Addr != ":50051" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.StartupDelayMs != 150 {
		t.Errorf("StartupDelayMs = %d", cfg.StartupDelayMs)
	}
}

func TestRegistryConfig_Fields(t *testing.T) {
	cfg := RegistryConfig{Path: "./config/registry.yaml"}
	if cfg.Path != "./config/registry.yaml" {
		t.Errorf("Path = %q", cfg.Path)
	}
}

// ---------------------------------------------------------------------------
// ChatCompleter compile-time check
// ---------------------------------------------------------------------------

// testChatCompleter implements ChatCompleter for compile-time verification.
type testChatCompleter struct{}

func (testChatCompleter) Complete(_ context.Context, _ []Message, _ []Tool) (Message, error) {
	return Message{}, nil
}

func (testChatCompleter) CompleteStream(_ context.Context, _ []Message, _ []Tool, _ func(delta string)) (string, string, []ToolCall, error) {
	return "", "", nil, nil
}

func (testChatCompleter) CompleteStreamEvents(_ context.Context, _ []Message, _ []Tool, _ func(StreamEvent)) (string, string, []ToolCall, error) {
	return "", "", nil, nil
}

// Compile-time assertion: both value and pointer satisfy ChatCompleter.
var (
	_ ChatCompleter = testChatCompleter{}
	_ ChatCompleter = (*testChatCompleter)(nil)
)
