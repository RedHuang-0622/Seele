package function

import (
	"encoding/json"

	"github.com/RedHuang-0622/Seele/types"
)

// AnthropicStrategy implements the Strategy interface for the Anthropic
// tool_use format. Tools are converted to Anthropic's definition structure;
// tool calls are decoded from the Anthropic content-block representation.
type AnthropicStrategy struct{}

// anthropicTool is the tool definition structure expected by the Anthropic API.
type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

// EncodeTools converts each types.Tool into the Anthropic tool definition
// format. The Parameters map is placed under input_schema; a top-level "type":
// "object" is injected if missing.
func (s *AnthropicStrategy) EncodeTools(tools []types.Tool) interface{} {
	if len(tools) == 0 {
		return []anthropicTool{}
	}

	out := make([]anthropicTool, 0, len(tools))
	for _, t := range tools {
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]interface{}{}
		}
		// Ensure a "type" key exists at the top level of the input schema.
		if _, ok := schema["type"]; !ok {
			schema["type"] = "object"
		}

		out = append(out, anthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	return out
}

// DecodeToolCall attempts to decode raw as a map[string]interface{} in the
// Anthropic content-block format:
//
//	{
//	  "type": "tool_use",
//	  "id": "toolu_xxx",
//	  "name": "foo",
//	  "input": {"bar": 1}
//	}
//
// The input map is marshalled to JSON and stored in Function.Arguments.
func (s *AnthropicStrategy) DecodeToolCall(raw interface{}) *types.ToolCall {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	// Only process tool_use blocks.
	if toString(m["type"]) != "tool_use" {
		return nil
	}

	var arguments string
	if input, ok := m["input"]; ok && input != nil {
		b, err := json.Marshal(input)
		if err == nil {
			arguments = string(b)
		}
	}

	return &types.ToolCall{
		ID:   toString(m["id"]),
		Type: "function",
		Function: types.ToolCallFunction{
			Name:      toString(m["name"]),
			Arguments: arguments,
		},
	}
}

// Ensure AnthropicStrategy satisfies Strategy at compile time.
var _ Strategy = (*AnthropicStrategy)(nil)

// init registers the Anthropic strategy under the key "anthropic".
func init() {
	Register("anthropic", &AnthropicStrategy{})
}
