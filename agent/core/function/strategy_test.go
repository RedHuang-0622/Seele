package function

import (
	"encoding/json"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// Register / Get / Names — basic operations
// ---------------------------------------------------------------------------

func TestRegisterGet(t *testing.T) {
	name := "test_register_get"
	Register(name, &AnthropicStrategy{})

	s := Get(name)
	if s == nil {
		t.Fatal("Get should return the registered strategy")
	}
	if _, ok := s.(*AnthropicStrategy); !ok {
		t.Error("Get should return the exact strategy that was registered")
	}
}

func TestNames(t *testing.T) {
	name := "test_names_check"
	Register(name, &OpenAIStrategy{})

	names := Names()
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Names should include %q, got %v", name, names)
	}

	// Should include built-in strategies registered via init().
	hasBuiltin := false
	for _, n := range names {
		if n == "openai" || n == "anthropic" {
			hasBuiltin = true
		}
	}
	if !hasBuiltin {
		t.Error("Names should include built-in strategies 'openai' and 'anthropic'")
	}
}

// ---------------------------------------------------------------------------
// Register duplicate panics
// ---------------------------------------------------------------------------

func TestRegisterDuplicate_Panics(t *testing.T) {
	name := "test_dup_panic"
	Register(name, &AnthropicStrategy{})

	defer func() {
		if r := recover(); r == nil {
			t.Error("Register of duplicate name should panic")
		}
	}()
	Register(name, &OpenAIStrategy{})
}

// ---------------------------------------------------------------------------
// Get returns nil for unknown name
// ---------------------------------------------------------------------------

func TestGet_ReturnsNilForUnknown(t *testing.T) {
	s := Get("strategy_that_does_not_exist_42")
	if s != nil {
		t.Errorf("Get should return nil for unknown name, got %v", s)
	}
}

// ---------------------------------------------------------------------------
// Names returns a copy (modifying result doesn't affect registry)
// ---------------------------------------------------------------------------

func TestNames_ReturnsCopy(t *testing.T) {
	name := "test_names_copy"
	Register(name, &AnthropicStrategy{})

	// Save a reference to the original names.
	originalNames := Names()

	// Mutate the returned slice.
	for i := range originalNames {
		originalNames[i] = "hacked_" + originalNames[i]
	}

	// Get names again — the original names should be intact.
	updatedNames := Names()
	for _, n := range updatedNames {
		if len(n) > 7 && n[:7] == "hacked_" {
			t.Errorf("Names() returned a mutated entry %q; should return a copy", n)
		}
	}

	// The registered name should still be reachable.
	if s := Get(name); s == nil {
		t.Error("Get should still return the registered strategy after mutating Names() result")
	}
}

// ---------------------------------------------------------------------------
// AnthropicStrategy — EncodeTools
// ---------------------------------------------------------------------------

func TestAnthropicStrategy_EncodeTools(t *testing.T) {
	s := &AnthropicStrategy{}

	tools := []types.Tool{
		{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "get_weather",
				Description: "Get the weather for a location",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"loc": map[string]interface{}{"type": "string"}},
				},
			},
		},
	}

	result := s.EncodeTools(tools)
	encoded, ok := result.([]anthropicTool)
	if !ok {
		t.Fatalf("EncodeTools returned %T, want []anthropicTool", result)
	}
	if len(encoded) != 1 {
		t.Fatalf("got %d tools, want 1", len(encoded))
	}
	if encoded[0].Name != "get_weather" {
		t.Errorf("Name = %q, want %q", encoded[0].Name, "get_weather")
	}
	if encoded[0].Description != "Get the weather for a location" {
		t.Errorf("Description = %q", encoded[0].Description)
	}
	if encoded[0].InputSchema["type"] != "object" {
		t.Errorf("InputSchema['type'] = %v", encoded[0].InputSchema["type"])
	}
}

func TestAnthropicStrategy_EncodeTools_Empty(t *testing.T) {
	s := &AnthropicStrategy{}
	result := s.EncodeTools(nil)
	encoded, ok := result.([]anthropicTool)
	if !ok {
		t.Fatalf("EncodeTools(nil) returned %T, want []anthropicTool", result)
	}
	if len(encoded) != 0 {
		t.Errorf("expected 0 tools, got %d", len(encoded))
	}
}

func TestAnthropicStrategy_EncodeTools_InjectTypeObject(t *testing.T) {
	s := &AnthropicStrategy{}
	tools := []types.Tool{
		{
			Function: types.ToolFunction{
				Name:        "no_type",
				Description: "Tool without type in parameters",
				Parameters:  map[string]interface{}{"properties": map[string]interface{}{}},
			},
		},
	}

	result := s.EncodeTools(tools)
	encoded, ok := result.([]anthropicTool)
	if !ok {
		t.Fatalf("EncodeTools returned %T", result)
	}
	if encoded[0].InputSchema["type"] != "object" {
		t.Errorf("type should be injected as 'object', got %v", encoded[0].InputSchema["type"])
	}
}

func TestAnthropicStrategy_EncodeTools_NilParameters(t *testing.T) {
	s := &AnthropicStrategy{}
	tools := []types.Tool{
		{
			Function: types.ToolFunction{
				Name:        "nil_params",
				Description: "Tool with nil parameters",
				Parameters:  nil,
			},
		},
	}

	result := s.EncodeTools(tools)
	encoded, ok := result.([]anthropicTool)
	if !ok {
		t.Fatalf("EncodeTools returned %T", result)
	}
	if encoded[0].InputSchema == nil {
		t.Error("InputSchema should not be nil even when Parameters is nil")
	}
	if encoded[0].InputSchema["type"] != "object" {
		t.Errorf("type should be 'object', got %v", encoded[0].InputSchema["type"])
	}
}

// ---------------------------------------------------------------------------
// AnthropicStrategy — DecodeToolCall
// ---------------------------------------------------------------------------

func TestAnthropicStrategy_DecodeToolCall(t *testing.T) {
	s := &AnthropicStrategy{}

	raw := map[string]interface{}{
		"type":  "tool_use",
		"id":    "toolu_abc123",
		"name":  "get_weather",
		"input": map[string]interface{}{"city": "Tokyo"},
	}

	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.ID != "toolu_abc123" {
		t.Errorf("ID = %q, want %q", tc.ID, "toolu_abc123")
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q, want 'function'", tc.Type)
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", tc.Function.Name, "get_weather")
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("Arguments is not valid JSON: %v", err)
	}
	if args["city"] != "Tokyo" {
		t.Errorf("Arguments.city = %v, want 'Tokyo'", args["city"])
	}
}

func TestAnthropicStrategy_DecodeToolCall_InvalidType(t *testing.T) {
	s := &AnthropicStrategy{}

	raw := map[string]interface{}{
		"type": "text",
		"id":   "toolu_123",
		"name": "foo",
	}

	tc := s.DecodeToolCall(raw)
	if tc != nil {
		t.Error("DecodeToolCall should return nil for non-tool_use blocks")
	}
}

func TestAnthropicStrategy_DecodeToolCall_NonMap(t *testing.T) {
	s := &AnthropicStrategy{}
	tc := s.DecodeToolCall("not a map")
	if tc != nil {
		t.Error("DecodeToolCall should return nil for non-map input")
	}
}

func TestAnthropicStrategy_DecodeToolCall_NilInput(t *testing.T) {
	s := &AnthropicStrategy{}

	raw := map[string]interface{}{
		"type":  "tool_use",
		"id":    "toolu_nil",
		"name":  "nil_input",
		"input": nil,
	}
	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.Function.Arguments != "" {
		t.Errorf("Arguments should be empty for nil input, got %q", tc.Function.Arguments)
	}
}

func TestAnthropicStrategy_DecodeToolCall_MissingFields(t *testing.T) {
	s := &AnthropicStrategy{}

	raw := map[string]interface{}{
		"type": "tool_use",
	}
	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.Function.Name != "" {
		t.Errorf("Name = %q, want empty", tc.Function.Name)
	}
}

// ---------------------------------------------------------------------------
// OpenAIStrategy — EncodeTools
// ---------------------------------------------------------------------------

func TestOpenAIStrategy_EncodeTools(t *testing.T) {
	s := &OpenAIStrategy{}

	tools := []types.Tool{
		{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "search",
				Description: "Search the web",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{"q": map[string]interface{}{"type": "string"}},
				},
			},
		},
	}

	result := s.EncodeTools(tools)
	encoded, ok := result.([]types.Tool)
	if !ok {
		t.Fatalf("EncodeTools returned %T, want []types.Tool", result)
	}
	if len(encoded) != 1 {
		t.Fatalf("got %d tools, want 1", len(encoded))
	}
	if encoded[0].Function.Name != "search" {
		t.Errorf("Function.Name = %q, want %q", encoded[0].Function.Name, "search")
	}
	if encoded[0].Function.Description != "Search the web" {
		t.Errorf("Function.Description = %q", encoded[0].Function.Description)
	}
}

func TestOpenAIStrategy_EncodeTools_Empty(t *testing.T) {
	s := &OpenAIStrategy{}
	result := s.EncodeTools(nil)
	encoded, ok := result.([]types.Tool)
	if !ok {
		t.Fatalf("EncodeTools(nil) returned %T, want []types.Tool", result)
	}
	if len(encoded) != 0 {
		t.Errorf("expected 0 tools, got %d", len(encoded))
	}
}

// ---------------------------------------------------------------------------
// OpenAIStrategy — DecodeToolCall
// ---------------------------------------------------------------------------

func TestOpenAIStrategy_DecodeToolCall(t *testing.T) {
	s := &OpenAIStrategy{}

	raw := map[string]interface{}{
		"id":   "call_xyz",
		"type": "function",
		"function": map[string]interface{}{
			"name":      "search",
			"arguments": `{"q":"hello"}`,
		},
	}

	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.ID != "call_xyz" {
		t.Errorf("ID = %q, want %q", tc.ID, "call_xyz")
	}
	if tc.Type != "function" {
		t.Errorf("Type = %q, want 'function'", tc.Type)
	}
	if tc.Function.Name != "search" {
		t.Errorf("Function.Name = %q, want %q", tc.Function.Name, "search")
	}
	if tc.Function.Arguments != `{"q":"hello"}` {
		t.Errorf("Arguments = %q, want %q", tc.Function.Arguments, `{"q":"hello"}`)
	}
}

func TestOpenAIStrategy_DecodeToolCall_NonMap(t *testing.T) {
	s := &OpenAIStrategy{}
	tc := s.DecodeToolCall(42)
	if tc != nil {
		t.Error("DecodeToolCall should return nil for non-map input")
	}
}

func TestOpenAIStrategy_DecodeToolCall_MissingFunction(t *testing.T) {
	s := &OpenAIStrategy{}

	raw := map[string]interface{}{
		"id":   "call_missing",
		"type": "function",
	}
	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.Function.Name != "" {
		t.Errorf("Function.Name = %q, want empty", tc.Function.Name)
	}
	if tc.Function.Arguments != "" {
		t.Errorf("Function.Arguments = %q, want empty", tc.Function.Arguments)
	}
}

func TestOpenAIStrategy_DecodeToolCall_NonStringFields(t *testing.T) {
	s := &OpenAIStrategy{}

	raw := map[string]interface{}{
		"id":   "call_nonstr",
		"type": "function",
		"function": map[string]interface{}{
			"name":      123,
			"arguments": map[string]interface{}{},
		},
	}
	tc := s.DecodeToolCall(raw)
	if tc == nil {
		t.Fatal("DecodeToolCall returned nil")
	}
	if tc.Function.Name != "" {
		t.Errorf("Name should be empty for non-string name, got %q", tc.Function.Name)
	}
	if tc.Function.Arguments != "" {
		t.Errorf("Arguments should be empty for non-string arguments, got %q", tc.Function.Arguments)
	}
}

// ---------------------------------------------------------------------------
// Built-in strategy registration (from init())
// ---------------------------------------------------------------------------

func TestBuiltinStrategies_Registered(t *testing.T) {
	anthropic := Get("anthropic")
	if anthropic == nil {
		t.Fatal("'anthropic' strategy should be registered via init()")
	}
	if _, ok := anthropic.(*AnthropicStrategy); !ok {
		t.Errorf("'anthropic' strategy is %T, want *AnthropicStrategy", anthropic)
	}

	openai := Get("openai")
	if openai == nil {
		t.Fatal("'openai' strategy should be registered via init()")
	}
	if _, ok := openai.(*OpenAIStrategy); !ok {
		t.Errorf("'openai' strategy is %T, want *OpenAIStrategy", openai)
	}
}
