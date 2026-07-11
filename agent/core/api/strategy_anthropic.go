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
	ToolChoice  json.RawMessage    `json:"tool_choice,omitempty"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"` // DeepSeek 思维链
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicResponse struct {
	ID      string                   `json:"id"`
	Type    string                   `json:"type"`
	Role    string                   `json:"role"`
	Content []anthropicContentBlock  `json:"content"`
	Usage   *anthropicUsage          `json:"usage,omitempty"`
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

// ── SSE 解析 ───────────────────────────────────────────────────

// ParseSSEEvent 解析 Anthropic SSE 帧。
//
// Anthropic SSE 使用 event: 行标记事件类型，data: 行包含 JSON 负载。
// eventType 由 client.go 从 event: 行提取传入；若为空（兼容旧格式），
// 则尝试从 data JSON 的 "type" 字段推断。
//
// 返回的事件：
//   - SSEEventText       — text_delta / message_start 中的文本
//   - SSEEventToolCall   — content_block_start (tool_use) +
//     content_block_delta (input_json_delta)
//   - SSEEventError      — error 帧
//   - 空切片             — ping/content_block_stop/message_delta/message_stop
func (s *AnthropicStrategy) ParseSSEEvent(eventType string, payload string) ([]SSEEvent, error) {
	// Fallback: 从 data JSON 推断事件类型（无 event: 行时）
	if eventType == "" {
		var raw struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &raw); err != nil {
			return nil, nil
		}
		eventType = raw.Type
	}

	switch eventType {
	case "ping":
		return nil, nil

	case "message_start":
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				ID      string                  `json:"id"`
				Content []anthropicContentBlock `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &msg); err != nil {
			return nil, fmt.Errorf("anthropic SSE message_start: %w", err)
		}
		// 短响应可能直接在 message_start 中包含内容块
		events := make([]SSEEvent, 0, len(msg.Message.Content))
		for i, block := range msg.Message.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					events = append(events, SSEEvent{
						Type:    SSEEventText,
						Content: block.Text,
					})
				}
			case "thinking":
				if block.Thinking != "" {
					events = append(events, SSEEvent{
						Type:    SSEEventReasoning,
						Content: block.Thinking,
					})
				}
			case "tool_use":
				events = append(events, SSEEvent{
					Type:          SSEEventToolCall,
					ToolCallIndex: i,
					Meta: map[string]any{
						"id":   block.ID,
						"name": block.Name,
					},
				})
			}
		}
		return events, nil

	case "content_block_start":
		var block struct {
			Type         string                `json:"type"`
			Index        int                   `json:"index"`
			ContentBlock anthropicContentBlock `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(payload), &block); err != nil {
			return nil, fmt.Errorf("anthropic SSE content_block_start: %w", err)
		}
		if block.ContentBlock.Type == "tool_use" {
			return []SSEEvent{{
				Type:          SSEEventToolCall,
				ToolCallIndex: block.Index,
				Meta: map[string]any{
					"id":   block.ContentBlock.ID,
					"name": block.ContentBlock.Name,
				},
			}}, nil
		}
		if block.ContentBlock.Type == "thinking" {
			if block.ContentBlock.Thinking != "" {
				return []SSEEvent{{Type: SSEEventReasoning, Content: block.ContentBlock.Thinking}}, nil
			}
			return nil, nil
		}
		return nil, nil

	case "content_block_delta":
		var delta struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				Thinking    string `json:"thinking,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &delta); err != nil {
			return nil, fmt.Errorf("anthropic SSE content_block_delta: %w", err)
		}
		switch delta.Delta.Type {
		case "text_delta":
			if delta.Delta.Text != "" {
				return []SSEEvent{{
					Type:    SSEEventText,
					Content: delta.Delta.Text,
				}}, nil
			}
		case "input_json_delta":
			if delta.Delta.PartialJSON != "" {
				return []SSEEvent{{
					Type:          SSEEventToolCall,
					ToolCallIndex: delta.Index,
					Meta: map[string]any{
						"arguments": delta.Delta.PartialJSON,
					},
				}}, nil
			}
		case "thinking_delta":
			text := delta.Delta.Thinking
			if text == "" {
				text = delta.Delta.Text
			}
			if text != "" {
				return []SSEEvent{{
					Type:    SSEEventReasoning,
					Content: delta.Delta.Text,
				}}, nil
			}
		}
		return nil, nil

	case "content_block_stop":
		return nil, nil

	case "message_delta":
		var delta struct {
			Type  string `json:"type"`
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &delta); err == nil && delta.Usage.InputTokens > 0 {
			return []SSEEvent{{
				Type: SSEEventUsage,
				Meta: map[string]any{
					"prompt_tokens":     delta.Usage.InputTokens,
					"completion_tokens": delta.Usage.OutputTokens,
					"total_tokens":      delta.Usage.InputTokens + delta.Usage.OutputTokens,
				},
			}}, nil
		}
		return nil, nil

	case "message_stop":
		return nil, nil

	case "error":
		var errPayload struct {
			Type  string `json:"type"`
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &errPayload); err != nil {
			return nil, fmt.Errorf("anthropic SSE error: %w", err)
		}
		return []SSEEvent{{
			Type:    SSEEventError,
			Content: fmt.Sprintf("[%s] %s", errPayload.Error.Type, errPayload.Error.Message),
		}}, nil

	default:
		return nil, nil
	}
}

// BuildRequest: 转换历史消息为 Anthropic 格式。
//
//	role="tool"  -> user + tool_result block
//	assistant with ToolCalls -> assistant + tool_use blocks
//	assistant text only      -> assistant + string content
//	user                     -> user + string content
func (s *AnthropicStrategy) BuildRequest(model string, messages []types.Message, tools []types.Tool, stream bool, opts RequestOptions) ([]byte, error) {
	var sys string
	var msgs []anthropicMessage

	// Buffer for merging consecutive tool messages into one anthropic message.
	// Anthropic requires ALL tool_results for a round to be in a single message.
	var pendingTools []map[string]any

	flushTools := func() error {
		if len(pendingTools) == 0 {
			return nil
		}
		b, err := json.Marshal(pendingTools)
		if err != nil {
			return fmt.Errorf("anthropic BuildRequest: marshal tool_results: %w", err)
		}
		msgs = append(msgs, anthropicMessage{Role: "user", Content: b})
		pendingTools = nil
		return nil
	}

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
			pendingTools = append(pendingTools, map[string]any{
				"type":        "tool_result",
				"tool_use_id": m.ToolCallID,
				"content":     content,
			})

		case "assistant":
			// Flush any pending tool results before a new assistant message
			if err := flushTools(); err != nil {
				return nil, err
			}
			if len(m.ToolCalls) > 0 {
				blocks := make([]map[string]any, 0, len(m.ToolCalls)+2)
				if m.ReasoningContent != "" {
					blocks = append(blocks, map[string]any{"type": "thinking", "thinking": m.ReasoningContent})
				}
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
				b, err := json.Marshal(blocks)
				if err != nil {
					return nil, fmt.Errorf("anthropic BuildRequest: marshal assistant blocks: %w", err)
				}
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: b})
			} else if m.Content != nil {
				content, err := json.Marshal(*m.Content)
				if err != nil {
					return nil, fmt.Errorf("anthropic BuildRequest: marshal assistant content: %w", err)
				}
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: content})
			}

		default:
			if m.Content != nil {
				content, err := json.Marshal(*m.Content)
				if err != nil {
					return nil, fmt.Errorf("anthropic BuildRequest: marshal %s content: %w", m.Role, err)
				}
				msgs = append(msgs, anthropicMessage{Role: m.Role, Content: content})
			}
		}
	}
	// flush trailing tool results (end of conversation)
	if err := flushTools(); err != nil {
		return nil, err
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
				b, err := json.Marshal(encoded)
				if err != nil {
					return nil, fmt.Errorf("anthropic BuildRequest: marshal tools: %w", err)
				}
				req.Tools = b
				req.ToolChoice = json.RawMessage(`{"type":"auto"}`)
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
	var thinkingContent string
	var toolCalls []types.ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			textContent += block.Text
		case "thinking":
			thinkingContent = block.Thinking // 保存 thinking，后续注入 ReasoningContent
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
	if thinkingContent != "" {
		msg.ReasoningContent = thinkingContent
	}
	if resp.Usage != nil {
		msg.Usage = &types.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
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
