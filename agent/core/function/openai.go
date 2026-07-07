package function

import (
	"github.com/RedHuang-0622/Seele/types"
)

// OpenAIStrategy implements the Strategy interface for the OpenAI function
// calling format. Tools are passed through as-is; tool calls are decoded from
// the standard OpenAI map representation.
type OpenAIStrategy struct{}

// EncodeTools returns the tools slice unchanged. OpenAI natively accepts the
// same Tool structure defined in types.
func (s *OpenAIStrategy) EncodeTools(tools []types.Tool) interface{} {
	return tools
}

// DecodeToolCall attempts to decode raw as a map[string]interface{} in the
// OpenAI tool_call format:
//
//	{
//	  "id": "call_xxx",
//	  "type": "function",
//	  "function": {"name": "foo", "arguments": "{\"bar\":1}"}
//	}
func (s *OpenAIStrategy) DecodeToolCall(raw interface{}) *types.ToolCall {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	tc := &types.ToolCall{
		ID:   toString(m["id"]),
		Type: toString(m["type"]),
	}

	if fn, ok := m["function"].(map[string]interface{}); ok {
		tc.Function.Name = toString(fn["name"])
		tc.Function.Arguments = toString(fn["arguments"])
	}

	return tc
}

// toString is a small helper that returns the string value of v when v is a
// string, or an empty string otherwise.
func toString(v interface{}) string {
	s, _ := v.(string)
	return s
}

// Ensure OpenAIStrategy satisfies Strategy at compile time.
var _ Strategy = (*OpenAIStrategy)(nil)

// init registers the OpenAI strategy under the key "openai".
func init() {
	Register("openai", &OpenAIStrategy{})
}
