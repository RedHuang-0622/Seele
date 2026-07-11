package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RedHuang-0622/Seele/types"
)

// ── 请求 / 响应结构体（OpenAI 专有）────────────────────────────

// openaiCompletionRequest 对应 POST /chat/completions 的同步请求体。
type openaiCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []types.Message `json:"messages"`
	Tools       []types.Tool    `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

// openaiStreamRequest 对应 POST /chat/completions 的流式请求体（含 stream:true）。
type openaiStreamRequest struct {
	Model       string          `json:"model"`
	Messages    []types.Message `json:"messages"`
	Tools       []types.Tool    `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
}

type openaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// openaiCompletionResponse 对应同步响应的 JSON body。
type openaiCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message      types.Message `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
	Usage *openaiUsage `json:"usage,omitempty"`
}

// openaiStreamDelta 对应 SSE data 帧中 choices[0].delta 的字段。
type openaiStreamDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id,omitempty"`
		Type     string `json:"type,omitempty"`
		Function struct {
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
		} `json:"function"`
	} `json:"tool_calls,omitempty"`
}

// openaiStreamResponse 对应 SSE data 帧的 JSON payload。
type openaiStreamResponse struct {
	Choices []struct {
		Delta        openaiStreamDelta `json:"delta"`
		FinishReason string            `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ── OpenAIStrategy ─────────────────────────────────────────────

// OpenAIStrategy 实现 ProviderStrategy 接口，处理 OpenAI 兼容协议的传输层。
type OpenAIStrategy struct{}

func (s *OpenAIStrategy) Name() string { return "openai" }

func (s *OpenAIStrategy) Endpoint() string { return "/chat/completions" }

func (s *OpenAIStrategy) BuildRequest(model string, messages []types.Message, tools []types.Tool, stream bool, opts RequestOptions) ([]byte, error) {
	if stream {
		req := openaiStreamRequest{
			Model:    model,
			Messages: messages,
			Stream:   true,
		}
		if len(tools) > 0 {
			req.Tools = tools
		}
		return json.Marshal(req)
	}
	req := openaiCompletionRequest{
		Model:    model,
		Messages: messages,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}
	return json.Marshal(req)
}

func (s *OpenAIStrategy) ParseResponse(body []byte) (types.Message, error) {
	var cr openaiCompletionResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return types.Message{}, fmt.Errorf("OpenAI parse response: %w\nraw: %.512s", err, body)
	}
	if cr.Error != nil {
		return types.Message{}, fmt.Errorf("OpenAI API error [%s/%s]: %s",
			cr.Error.Type, cr.Error.Code, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return types.Message{}, fmt.Errorf("OpenAI empty choices\nraw: %.512s", body)
	}
	msg := cr.Choices[0].Message
	if cr.Usage != nil {
		msg.Usage = &types.Usage{
			PromptTokens:     cr.Usage.PromptTokens,
			CompletionTokens: cr.Usage.CompletionTokens,
			TotalTokens:      cr.Usage.TotalTokens,
		}
	}
	return msg, nil
}

func (s *OpenAIStrategy) SSEHeaders() map[string]string {
	return map[string]string{"Accept": "text/event-stream"}
}

// ParseSSEEvent 解析 OpenAI SSE data 帧。
//
// 返回的事件：
//   - SSEEventText — delta.Content 非空
//   - SSEEventToolCall — delta.ToolCalls 非空（每帧可能包含多个并发调用）
//   - SSEEventReasoning — delta.ReasoningContent 非空
//   - SSEEventError — 帧中包含 error 字段
//   - 空切片 — 无法解析的帧（如心跳）
func (s *OpenAIStrategy) ParseSSEEvent(eventType string, payload string) ([]SSEEvent, error) {
	if eventType != "data" && eventType != "" {
		return nil, nil
	}

	var frame openaiStreamResponse
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return nil, nil
	}

	if frame.Error != nil {
		return []SSEEvent{{
			Type:    SSEEventError,
			Content: fmt.Sprintf("[%s/%s] %s", frame.Error.Type, frame.Error.Code, frame.Error.Message),
		}}, nil
	}

	// 流式结束帧可能带 usage（choices 为空但有 usage 字段）
	if len(frame.Choices) == 0 {
		if frame.Usage != nil {
			return []SSEEvent{{
				Type: SSEEventUsage,
				Meta: map[string]any{
					"prompt_tokens":     frame.Usage.PromptTokens,
					"completion_tokens": frame.Usage.CompletionTokens,
					"total_tokens":      frame.Usage.TotalTokens,
				},
			}}, nil
		}
		return nil, nil
	}

	delta := frame.Choices[0].Delta

	// tool_call 帧：可能包含多个并发工具调用
	if len(delta.ToolCalls) > 0 {
		events := make([]SSEEvent, 0, len(delta.ToolCalls))
		for _, tc := range delta.ToolCalls {
			evContent := tc.ID
			if evContent == "" {
				evContent = tc.Function.Name
			}
			if evContent == "" {
				evContent = tc.Function.Arguments
			}
			events = append(events, SSEEvent{
				Type:          SSEEventToolCall,
				Content:       evContent,
				ToolCallIndex: tc.Index,
				Meta: map[string]any{
					"id":        tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
					"type":      tc.Type,
				},
			})
		}
		return events, nil
	}

	// 推理内容帧
	if delta.ReasoningContent != "" {
		return []SSEEvent{{
			Type:    SSEEventReasoning,
			Content: delta.ReasoningContent,
		}}, nil
	}

	// 文本帧
	if delta.Content != "" {
		return []SSEEvent{{
			Type:    SSEEventText,
			Content: delta.Content,
		}}, nil
	}

	return nil, nil
}

func (s *OpenAIStrategy) AuthHeader(apiKey string) (string, string) {
	return "Authorization", "Bearer " + apiKey
}

// ── 辅助 ──────────────────────────────────────────────────────

// StreamToolCallMeta 从 SSEEvent.Meta 提取 OpenAI 工具调用元数据。
func StreamToolCallMeta(meta map[string]any) (id, name, arguments, typ string) {
	if meta == nil {
		return
	}
	id, _ = meta["id"].(string)
	name, _ = meta["name"].(string)
	arguments, _ = meta["arguments"].(string)
	typ, _ = meta["type"].(string)
	return
}

// IsOpenAIStreamFrame 判断 payload 是否为 OpenAI 格式的流式帧。
func IsOpenAIStreamFrame(payload string) bool {
	return strings.Contains(payload, `"choices"`) && strings.Contains(payload, `"delta"`)
}

// Ensure OpenAIStrategy satisfies ProviderStrategy at compile time.
var _ ProviderStrategy = (*OpenAIStrategy)(nil)

// init 将 OpenAIStrategy 注册到全局策略注册表。
func init() {
	RegisterProviderStrategy(&OpenAIStrategy{})
}
