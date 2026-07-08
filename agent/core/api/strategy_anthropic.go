package api

import (
	"encoding/json"
	"fmt"

	"github.com/RedHuang-0622/Seele/agent/core/function"
	"github.com/RedHuang-0622/Seele/types"
)

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	System      string             `json:"system,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
	Temperature float64            `json:"temperature,omitempty"`
	Tools       json.RawMessage    `json:"tools,omitempty"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type anthropicResponse struct {
	ID      string                   `json:"id"`
	Type    string                   `json:"type"`
	Role    string                   `json:"role"`
	Content []anthropicContentBlock  `json:"content"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type AnthropicStrategy struct{}

func (s *AnthropicStrategy) Name() string              { return "anthropic" }
func (s *AnthropicStrategy) Endpoint() string           { return "/v1/messages" }
func (s *AnthropicStrategy) AuthHeader(apiKey string) (string, string) { return "x-api-key", apiKey }
func (s *AnthropicStrategy) SSEHeaders() map[string]string {
	return map[string]string{"Accept": "text/event-stream", "anthropic-version": "2023-06-01"}
}
func (s *AnthropicStrategy) ParseSSEEvent(_, _ string) ([]SSEEvent, error) { return nil, nil }

// BuildRequest: 转换历史消息为 Anthropic 格式。
//
//	role="tool"  -> user + tool_result block
//	assistant with ToolCalls -> assistant + tool_use blocks
//	assistant text only      -> assistant + string content
//	user                     -> user + string content
func (s *AnthropicStrategy) BuildRequest(model string, messages []types.Message, tools []types.Tool, stream bool, opts RequestOptions) ([]byte, error) {
	var sys string
	var msgs []anthropicMessage

	for _, m := range messages {
		switch m.Role {
		case "system":
			if m.Content != nil {
				sys = *m.Content
			}

		case "tool":
			content := ""
			if m.Content != nil {
				content = *m.Content
			}
			block, _ := json.Marshal([]map[string]any{{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     content,
			}})
			msgs = append(msgs, anthropicMessage{Role: "user", Content: block})

		case "assistant":
			if len(m.ToolCalls) > 0 {
				blocks := make([]map[string]any, 0, len(m.ToolCalls)+1)
				if m.Content != nil && *m.Content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": *m.Content})
				}
				for _, tc := range m.ToolCalls {
					input := json.RawMessage(`{}`)
					if tc.Function.Arguments != "" {
						input = json.RawMessage(tc.Function.Arguments)
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": input,
					})
				}
				b, _ := json.Marshal(blocks)
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: b})
			} else if m.Content != nil {
				content, _ := json.Marshal(*m.Content)
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: content})
			}

		default:
			if m.Content != nil {
				content, _ := json.Marshal(*m.Content)
				msgs = append(msgs, anthropicMessage{Role: m.Role, Content: content})
			}
		}
	}

	req := anthropicRequest{
		Model:     model,
		Messages:  msgs,
		MaxTokens: maxTokensOr(opts.MaxTokens, 4096),
		Stream:    stream,
	}
	if sys != "" {
		req.System = sys
	}
	if opts.Temperature > 0 {
		req.Temperature = opts.Temperature
	}
	if len(tools) > 0 {
		fnStrat := function.Get("anthropic")
		if fnStrat != nil {
			if encoded := fnStrat.EncodeTools(tools); encoded != nil {
				if b, err := json.Marshal(encoded); err == nil {
					req.Tools = b
				}
			}
		}
	}
	return json.Marshal(req)
}

func (s *AnthropicStrategy) ParseResponse(body []byte) (types.Message, error) {
	var resp anthropicResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return types.Message{}, fmt.Errorf("Anthropic parse response: %w\nraw: %.512s", err, body)
	}
	if resp.Error != nil {
		return types.Message{}, fmt.Errorf("Anthropic API error [%s]: %s", resp.Error.Type, resp.Error.Message)
	}

	fnStrat := function.Get("anthropic")
	var textContent string
	var toolCalls []types.ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textContent += block.Text
		case "tool_use":
			if fnStrat != nil {
				raw := map[string]any{
					"type":  "tool_use",
					"id":    block.ID,
					"name":  block.Name,
					"input": block.Input,
				}
				if tc := fnStrat.DecodeToolCall(raw); tc != nil {
					toolCalls = append(toolCalls, *tc)
				}
			}
		}
	}

	msg := types.Message{Role: "assistant"}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	} else {
		msg.Content = &textContent
	}
	return msg, nil
}

func maxTokensOr(n, fallback int) int {
	if n > 0 {
		return n
	}
	return fallback
}

var _ ProviderStrategy = (*AnthropicStrategy)(nil)

func init() {
	RegisterProviderStrategy(&AnthropicStrategy{})
}
