package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// ChatClient 是对 LLM API 的轻量 HTTP 封装。
//
// 职责：
//   - HTTP 生命周期管理（超时、重试、连接池）
//   - 通过 ProviderStrategy 处理协议差异（请求构建 / 响应解析 / SSE 帧）
//   - 账号池集成（round-robin、按 provider 筛选）
//
// 无第三方依赖，纯标准库 net/http。
type ChatClient struct {
	Cfg    types.LLMConfig
	Client *http.Client
	pool            *AccountPool     // 账号池，非必填
	strategy        ProviderStrategy // 传输层策略，nil 时通过 effectiveStrategy 自动选择
	provider        ProviderType     // llm_config.provider: 锁死本轮消息格式，优先于 account.Provider
	providerFilter  ProviderType     // 非空时只从 pool 获取该 provider 的账号
}

// WithAccountPool 设置账号池，返回自身以便链式调用。
// 设置后 ChatClient 从 pool 获取 API key 和 BaseURL，优先于 LLMConfig。
func (c *ChatClient) WithAccountPool(pool *AccountPool) *ChatClient {
	c.pool = pool
	return c
}

// WithStrategy 设置传输层策略，覆盖按 provider 自动选择的默认行为。
func (c *ChatClient) WithStrategy(s ProviderStrategy) *ChatClient {
	c.strategy = s
	return c
}

// SetProvider 设置会话级 provider，决定消息格式（策略选择依据）。
// 应在首次请求前调用，调用后不可更改（历史格式一致性）。
func (c *ChatClient) SetProvider(p ProviderType) *ChatClient {
	c.provider = p
	return c
}

// SelectAccount 按名称切换到号池内的指定账号。
// 找到账号时将后续请求定位到该账号，返回 true。
// 账号不存在或已禁用时不做任何切换，返回 false。
func (c *ChatClient) SelectAccount(name string) bool {
	if c.pool == nil {
		return false
	}
	return c.pool.Select(name) != nil
}

// Provider 返回当前会话级 provider，空值表示默认 "openai" 格式。
func (c *ChatClient) Provider() ProviderType {
	return c.provider
}

// SetProviderFilter 设置账号筛选器，后续 Get 只返回该 provider 的账号。
// 空值清除筛选，恢复 round-robin。不影响消息格式（格式由 SetProvider 决定）。
func (c *ChatClient) SetProviderFilter(p ProviderType) *ChatClient {
	c.providerFilter = p
	return c
}

// ProviderFilter 返回当前的 provider 筛选器。空值表示 round-robin 模式。
func (c *ChatClient) ProviderFilter() ProviderType {
	return c.providerFilter
}

// AccountPool 返回关联的账号池，可能为 nil。
func (c *ChatClient) AccountPool() *AccountPool {
	return c.pool
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

// ── 策略选择 ──────────────────────────────────────────────────────

// effectiveAccount 从 pool 获取一个可用账号。
// 当 providerFilter 非空时只返回该 provider 的账号（用 GetByProvider），
// 否则 round-robin（Get）。
// 没有 pool 或没有可用账号时返回 nil。
func (c *ChatClient) effectiveAccount() *Account {
	if c.pool != nil {
		if c.providerFilter != "" {
			return c.pool.GetByProvider(c.providerFilter)
		}
		return c.pool.Get()
	}
	return nil
}

// effectiveStrategy 根据会话级 provider（由 llm_config.provider 设置）选择传输层策略。
//
// 优先级：
//  1. ChatClient 上显式设置的 strategy（WithStrategy）
//  2. c.provider（llm_config.provider 设定，推荐）
//  3. acct.Provider（账号级 provider，兼容旧格式）
//  4. 默认 "openai"
//
// 注意：llm_config.provider 优先于 acct.Provider，确保同一 session 内消息格式一致。
//
// 当指定名称的策略未注册时，回退到 "openai" 并记录警告日志。
func (c *ChatClient) effectiveStrategy(acct *Account) ProviderStrategy {
	if c.strategy != nil {
		return c.strategy
	}
	name := "openai"
	switch {
	case c.provider != "":
		name = string(c.provider)
	case acct != nil && acct.Provider != "":
		name = string(acct.Provider)
	}
	if s := GetProviderStrategy(name); s != nil {
		return s
	}
	slog.Warn("provider strategy not found, falling back to openai",
		"requested", name,
	)
	return GetProviderStrategy("openai")
}

// ── 公共方法 ──────────────────────────────────────────────────────

// Complete 发送一次对话补全请求，返回模型的回复 Message。
//
//   - 若模型发起 tool_calls，Message.ToolCalls 非空，Message.Content 可能为空。
//   - 若模型直接回复，Message.Content 为文本，Message.ToolCalls 为空。
func (c *ChatClient) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	acct := c.effectiveAccount()
	if acct == nil && c.pool != nil {
		return types.Message{}, fmt.Errorf("ChatClient: all accounts rate-limited or disabled")
	}
	strategy := c.effectiveStrategy(acct)

	baseURL := c.Cfg.BaseURL
	apiKey := c.Cfg.APIKey
	if acct != nil {
		baseURL = acct.BaseURL
		apiKey = acct.APIKey
	}

	raw, err := strategy.BuildRequest(effectiveModel(c.Cfg, acct), messages, tools, false, requestOpts(c.Cfg, acct))
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: build request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+strategy.Endpoint(),
		bytes.NewReader(raw),
	)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: build HTTP request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	hdrKey, hdrVal := strategy.AuthHeader(apiKey)
	httpReq.Header.Set(hdrKey, hdrVal)

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: HTTP: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return types.Message{}, fmt.Errorf("ChatClient: read response: %w", err)
	}

	return strategy.ParseResponse(data)
}

// ── 流式接口 ──────────────────────────────────────────────────────

// sseState 保存 SSE 读取过程中的累积状态。
// 每次 completeStream 调用创建一个新实例，不跨请求复用。
type sseState struct {
	tcMap       map[int]*types.ToolCall // tool_call index → 累积的工具调用
	sb          strings.Builder         // 累积纯文本回复
	reasoningSB strings.Builder         // 累积思索文段
	isToolMode  bool                   // 是否已收到 tool_call 帧
}

func newSSEState() *sseState {
	return &sseState{
		tcMap: make(map[int]*types.ToolCall),
	}
}

// applySSEEvents 将 Strategy 解析出的 SSE 事件切片应用到 sseState。
// text delta 通过 onChunk 实时推送。
func (s *sseState) applySSEEvents(events []SSEEvent, onChunk func(string)) {
	for i := range events {
		ev := &events[i]
		switch ev.Type {
		case SSEEventToolCall:
			s.isToolMode = true
			entry, exists := s.tcMap[ev.ToolCallIndex]
			if !exists {
				entry = &types.ToolCall{Type: "function"}
				s.tcMap[ev.ToolCallIndex] = entry
			}
			if meta := ev.Meta; meta != nil {
				if id, _ := meta["id"].(string); id != "" {
					entry.ID = id
				}
				if name, _ := meta["name"].(string); name != "" {
					entry.Function.Name = name
				}
				if args, _ := meta["arguments"].(string); args != "" {
					entry.Function.Arguments += args
				}
			}
		case SSEEventText:
			if !s.isToolMode {
				s.sb.WriteString(ev.Content)
				if onChunk != nil {
					onChunk(ev.Content)
				}
			}
		case SSEEventReasoning:
			s.reasoningSB.WriteString(ev.Content)
		}
	}
}

// buildToolCalls 将 tcMap 整理成有序的 []ToolCall。
func buildToolCalls(tcMap map[int]*types.ToolCall) []types.ToolCall {
	result := make([]types.ToolCall, 0, len(tcMap))
	for i := 0; i < len(tcMap); i++ {
		if tc, ok := tcMap[i]; ok {
			result = append(result, *tc)
		}
	}
	return result
}

// doStreamRequest 构造并发送流式 HTTP 请求，返回响应 body。
// 调用方负责关闭 body。
func (c *ChatClient) doStreamRequest(ctx context.Context, messages []types.Message, tools []types.Tool) (io.ReadCloser, error) {
	acct := c.effectiveAccount()
	if acct == nil && c.pool != nil {
		return nil, fmt.Errorf("ChatClient: all accounts rate-limited or disabled")
	}
	strategy := c.effectiveStrategy(acct)

	baseURL := c.Cfg.BaseURL
	apiKey := c.Cfg.APIKey
	if acct != nil {
		baseURL = acct.BaseURL
		apiKey = acct.APIKey
	}

	raw, err := strategy.BuildRequest(effectiveModel(c.Cfg, acct), messages, tools, true, requestOpts(c.Cfg, acct))
	if err != nil {
		return nil, fmt.Errorf("marshal stream request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+strategy.Endpoint(), bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	hdrKey, hdrVal := strategy.AuthHeader(apiKey)
	httpReq.Header.Set(hdrKey, hdrVal)
	for k, v := range strategy.SSEHeaders() {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP stream: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %.512s", resp.StatusCode, body)
	}

	return resp.Body, nil
}

// CompleteStream 发起流式 chat completion 请求。
//
// 行为：
//   - 纯文本回复：每个 token 到达时调用 onChunk 实时推出，返回 (完整文本, nil, nil)
//   - tool_call 回复：静默累积所有帧，返回 ("", toolCalls, nil)
//
// 调用方只需判断返回的 toolCalls 是否为空来区分两种情况。
func (c *ChatClient) CompleteStream(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onChunk func(delta string),
) (content string, reasoningContent string, toolCalls []types.ToolCall, err error) {
	return c.completeStreamInternal(ctx, messages, tools, onChunk)
}

// CompleteStreamEvents 与 CompleteStream 功能相同，但通过 onEvent 传递结构化事件。
func (c *ChatClient) CompleteStreamEvents(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onEvent func(types.StreamEvent),
) (content string, reasoningContent string, toolCalls []types.ToolCall, err error) {
	onChunk := func(delta string) {
		onEvent(types.StreamEvent{Type: types.StreamEventText, Content: delta})
	}
	return c.completeStreamInternal(ctx, messages, tools, onChunk)
}

// completeStreamInternal 是流式请求的内部实现。
// 使用 strategy.ParseSSEEvent 解析每一帧，通过 sseState 累积结果。
func (c *ChatClient) completeStreamInternal(
	ctx context.Context,
	messages []types.Message,
	tools []types.Tool,
	onChunk func(string),
) (content string, reasoningContent string, toolCalls []types.ToolCall, err error) {

	strategy := c.effectiveStrategy(c.effectiveAccount())

	body, err := c.doStreamRequest(ctx, messages, tools)
	if err != nil {
		return "", "", nil, fmt.Errorf("ChatClient stream: %w", err)
	}
	defer body.Close()

	state := newSSEState()
	reader := bufio.NewReader(body)

	currentEventType := "" // track event: lines (Anthropic SSE format)
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		if line == "data: [DONE]" {
			break
		}

		if eventVal, ok := strings.CutPrefix(line, "event: "); ok {
			currentEventType = eventVal
			continue
		}

		if payload, ok := strings.CutPrefix(line, "data: "); ok && payload != "" {
			events, parseErr := strategy.ParseSSEEvent(currentEventType, payload)
			currentEventType = ""
			if parseErr != nil {
				return "", "", nil, fmt.Errorf("ChatClient stream: %w", parseErr)
			}
			state.applySSEEvents(events, onChunk)
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", "", nil, fmt.Errorf("ChatClient stream: read SSE: %w", readErr)
		}
	}

	if state.isToolMode {
		return state.sb.String(), state.reasoningSB.String(), buildToolCalls(state.tcMap), nil
	}
	return state.sb.String(), state.reasoningSB.String(), nil, nil
}

// requestOpts 从 ChatClient 配置和 Account 合并出请求级参数。
// Account 级设置优先于全局配置。
func requestOpts(cfg types.LLMConfig, acct *Account) RequestOptions {
	opts := RequestOptions{
		MaxTokens:   cfg.MaxTokens,
		Temperature: cfg.Temperature,
	}
	if acct != nil {
		if acct.MaxTokens > 0 {
			opts.MaxTokens = acct.MaxTokens
		}
		if acct.Temperature > 0 {
			opts.Temperature = acct.Temperature
		}
	}
	return opts
}

// effectiveModel 返回当前请求使用的模型名。
// Account 级 model 优先于 ChatClient 全局配置。
func effectiveModel(cfg types.LLMConfig, acct *Account) string {
	if acct != nil && acct.Model != "" {
		return acct.Model
	}
	return cfg.Model
}
