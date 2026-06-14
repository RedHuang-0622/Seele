package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// ChatClient 是对 OpenAI 兼容 /v1/chat/completions 的轻量封装。
// 无第三方依赖，纯标准库 net/http。
type ChatClient struct {
	Cfg    types.LLMConfig
	Client *http.Client
}

func NewChatClient(cfg types.LLMConfig) *ChatClient {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	return &ChatClient{
		Cfg:    cfg,
		Client: &http.Client{Timeout: time.Duration(timeout) * time.Second},
	}
}

// ── 请求 / 响应结构体（仅在本文件使用）────────────────────────────

type chatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []types.Message `json:"messages"`
	Tools       []types.Tool    `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
}

type chatCompletionResponse struct {
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
}

// ── 核心方法 ──────────────────────────────────────────────────────

// Complete 发送一次对话补全请求，返回模型的回复 Message。
//
//   - 若模型发起 tool_calls，Message.ToolCalls 非空，Message.Content 可能为空。
//   - 若模型直接回复，Message.Content 为文本，Message.ToolCalls 为空。
func (c *ChatClient) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {

	reqBody := chatCompletionRequest{
		Model:       c.Cfg.Model,
		Messages:    messages,
		MaxTokens:   c.Cfg.MaxTokens,
		Temperature: c.Cfg.Temperature,
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.Cfg.BaseURL+"/chat/completions",
		bytes.NewReader(raw),
	)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Cfg.APIKey)

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: HTTP: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: read response: %w", err)
	}

	var cr chatCompletionResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: parse response: %w\nraw: %.512s", err, data)
	}
	if cr.Error != nil {
		return types.Message{}, fmt.Errorf("ChatClient: API error [%s/%s]: %s",
			cr.Error.Type, cr.Error.Code, cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return types.Message{}, fmt.Errorf("ChatClient: empty choices\nraw: %.512s", data)
	}

	return cr.Choices[0].Message, nil
}

// ── 流式请求结构体 ─────────────────────────────────────────────────

type chatCompletionStreamRequest struct {
	Model       string          `json:"model"`
	Messages    []types.Message `json:"messages"`
	Tools       []types.Tool    `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature float64         `json:"temperature,omitempty"`
	Stream      bool            `json:"stream"`
}

// streamDelta 对应一个 SSE data 帧中 choices[0].delta 的字段。
type streamDelta struct {
	Role             string `json:"role,omitempty"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"` // 思索文段
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

type chatCompletionStreamResponse struct {
	Choices []struct {
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// ── CompleteStream ────────────────────────────────────────────────
// CompleteStream 发起流式 chat completion 请求。
//
// 行为规则（与 OpenAI 流式协议对齐）：
//   - 若模型返回纯文本：每个 content delta 同步调用 onChunk；
//     最终返回 (完整文本, nil toolCalls, nil)
//   - 若模型返回 tool_calls：不调用 onChunk，累积所有 delta 后
//     返回 ("", toolCalls, nil)
//
// 调用方无需区分两种情况，只需检查返回的 toolCalls 是否为空。

// doStreamRequest 构造并发送流式 HTTP 请求，返回响应 body。
// 调用方负责关闭 body。
func (c *ChatClient) doStreamRequest(ctx context.Context, messages []types.Message, tools []types.Tool) (io.ReadCloser, error) {
	temperature := c.Cfg.Temperature
	reqBody := chatCompletionStreamRequest{
		Model:       c.Cfg.Model,
		Messages:    messages,
		MaxTokens:   c.Cfg.MaxTokens,
		Temperature: temperature,
		Stream:      true,
	}
	if len(tools) > 0 {
		reqBody.Tools = tools
	}

	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Cfg.BaseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.Cfg.APIKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %.512s", resp.StatusCode, body)
	}

	return resp.Body, nil
}

// sseState 保存 SSE 读取过程中的累积状态。
// 每次 completeStream 调用创建一个新实例，不跨请求复用。
type sseState struct {
	// tcMap 以 tool_call 的 index 为 key，累积每个工具调用的完整内容。
	// LLM 会把 arguments JSON 拆成多帧推送，这里负责把碎片拼回完整 JSON。
	tcMap map[int]*types.ToolCall

	// sb 累积纯文本回复，每个文本 delta 追加进来。
	sb strings.Builder
	// reasoningSB 累积思索文段，按需返回给调用方。
	reasoningSB strings.Builder // 累积思索文段，按需返回给调用方
	// isToolMode 标记当前流是否为 tool_call 模式。
	// 一旦收到第一个 tool_call 帧就锁定为 true，不可逆。
	// 锁定后忽略所有文本帧（正常情况下也不会再有文本帧）。
	isToolMode bool
}

func newSSEState() *sseState {
	return &sseState{
		tcMap: make(map[int]*types.ToolCall),
	}
}

// processFrame 解析单个 SSE data 帧的 JSON payload，更新 sseState。
//
// 两种帧：
//   - tool_call 帧：delta.ToolCalls 非空，累积进 tcMap，不调用 onChunk
//   - 文本帧：delta.Content 非空，追加进 sb，并调用 onChunk 实时推给调用方
//
// 返回 error 时调用方应中止整个流。
func (c *ChatClient) processFrame(payload string, state *sseState, onChunk func(string)) error {
	var frame chatCompletionStreamResponse
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		// 无法解析的帧直接跳过，不中断流。
		// 常见于心跳帧或格式略有差异的中间帧。
		return nil
	}
	if frame.Error != nil {
		return fmt.Errorf("API error [%s/%s]: %s",
			frame.Error.Type, frame.Error.Code, frame.Error.Message)
	}
	if len(frame.Choices) == 0 {
		return nil
	}

	delta := frame.Choices[0].Delta

	// ── tool_call 帧处理 ──────────────────────────────────────────
	// LLM 返回 tool_call 时，同一个工具的内容分散在多帧里：
	//   首帧：携带 id、函数名，arguments 为空字符串
	//   后续帧：只有 arguments 的 JSON 碎片，逐帧拼接才能得到完整参数
	if len(delta.ToolCalls) > 0 {
		state.isToolMode = true
		for _, tc := range delta.ToolCalls {
			entry, exists := state.tcMap[tc.Index]
			if !exists {
				entry = &types.ToolCall{Type: "function"}
				state.tcMap[tc.Index] = entry
			}
			if tc.ID != "" {
				entry.ID = tc.ID
			}
			if tc.Function.Name != "" {
				entry.Function.Name = tc.Function.Name
			}
			entry.Function.Arguments += tc.Function.Arguments
		}
	}

	// ── 文本帧处理 ────────────────────────────────────────────────
	// isToolMode 时不处理文本帧，正常流里两种帧不会混出现。
	if !state.isToolMode && delta.Content != "" {
		state.sb.WriteString(delta.Content)
		onChunk(delta.Content) // 实时推给调用方，用户立刻看到这个 token
	}
	if delta.ReasoningContent != "" {
		state.reasoningSB.WriteString(delta.ReasoningContent)
	}
	return nil
}

// buildToolCalls 将 tcMap（index → *ToolCall）整理成有序的 []ToolCall。
// 按索引 0,1,2... 顺序迭代，跳过缺失的索引，append 构建结果，
// 防止非连续索引导致的零值 ToolCall 注入 history。
func buildToolCalls(tcMap map[int]*types.ToolCall) []types.ToolCall {
	result := make([]types.ToolCall, 0, len(tcMap))
	for i := 0; i < len(tcMap); i++ {
		if tc, ok := tcMap[i]; ok {
			result = append(result, *tc)
		}
	}
	return result
}

// CompleteStream 发起流式 chat completion 请求。
//
// 行为：
//   - 纯文本回复：每个 token 到达时调用 onChunk 实时推出，返回 (完整文本, nil, nil)
//   - tool_call 回复：静默累积所有帧，返回 ("", toolCalls, nil)
//
// 调用方只需判断返回的 toolCalls 是否为空来区分两种情况。
// 可以理解为一个接收的loop
func (c *ChatClient) CompleteStream(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onChunk func(delta string),
) (content string, reasoning_content string, toolCalls []types.ToolCall, err error) {

	// 1. 建立 SSE 连接
	body, err := c.doStreamRequest(ctx, messages, tools)
	if err != nil {
		return "", "", nil, fmt.Errorf("ChatClient stream: %w", err)
	}
	defer body.Close()

	// 2. 逐行读取 SSE，解析每一帧
	state := newSSEState()
	reader := bufio.NewReader(body)

	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line == "data: [DONE]" {
			// 流正常结束
			break
		}

		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload != "" {
				if err := c.processFrame(payload, state, onChunk); err != nil {
					return "", "", nil, fmt.Errorf("ChatClient stream: %w", err)
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", "", nil, fmt.Errorf("ChatClient stream: read SSE: %w", readErr)
		}
	}

	// 3. 根据模式返回结果
	if state.isToolMode {
		return state.sb.String(), state.reasoningSB.String(), buildToolCalls(state.tcMap), nil
	}
	return state.sb.String(), state.reasoningSB.String(), nil, nil
}
