// 06_provider_switch/main.go
//
// Seele 号池多 Provider 切换演示：
//   1. OpenAI -> "你好，请记住我的名字：小明"
//   2. Anthropic -> "刚才我说了什么？"（需历史上下文）
//   3. OpenAI -> "你上次回复了什么？"（追忆对话）
//
// 核心机制：ChatClient.SetProviderFilter(ProviderType) 切换号池，
// 对话历史跨 Provider 共享。
//
// 运行：go run .
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/types"
)

func main() {
	// ===================================================================
	// 1. Mock 服务
	// ===================================================================
	mkOpenAI := func(req mockReq) mockResp {
		msg := fmt.Sprintf(`"【OpenAI回复】你说的是: %s。我是GPT-4！"`, lastUserMsg(req.Messages))
		return mockResp{Content: msg}
	}
	openaiSrv := newMockProvider("openai", "/chat/completions", mkOpenAI)
	defer openaiSrv.Close()

	mkAnthropic := func(req mockReq) mockResp {
		var history string
		for _, m := range req.Messages {
			if m.Content != nil {
				history += "[" + m.Role + "] " + *m.Content + " "
			}
		}
		msg := fmt.Sprintf(`"【Anthropic回复】历史: %s。Claude说: 我记住了！"`, history)
		return mockResp{Content: msg}
	}
	anthropicSrv := newMockProvider("anthropic", "/v1/messages", mkAnthropic)
	defer anthropicSrv.Close()

	fmt.Println("=== Seele 多 Provider 切换演示 ===")
	fmt.Println()

	// ===================================================================
	// 2. 号池
	// ===================================================================
	pool := api.NewAccountPool(
		&api.Account{Name: "openai-main", Provider: api.ProviderOpenAI, BaseURL: openaiSrv.URL, APIKey: "sk-1", Model: "gpt-4", Priority: 1},
		&api.Account{Name: "anthropic-main", Provider: api.ProviderAnthropic, BaseURL: anthropicSrv.URL, APIKey: "sk-2", Model: "claude-3-5", Priority: 2},
	)

	// ===================================================================
	// 3. 装配
	// ===================================================================
	llmClient := api.NewChatClient(types.LLMConfig{Model: "gpt-4"}).WithAccountPool(pool)
	session := newSession(llmClient, "你是一个多 Provider 助手，会记住对话历史。")

	// ---- Step 1: OpenAI ----
	fmt.Println("--- Step 1: /switch openai ---")
	llmClient.SetProviderFilter(api.ProviderOpenAI)
	r1 := chat(session, "你好，请记住我的名字：小明")
	fmt.Println("  OpenAI:", r1)
	fmt.Println()

	// ---- Step 2: Anthropic ----
	fmt.Println("--- Step 2: /switch anthropic ---")
	llmClient.SetProviderFilter(api.ProviderAnthropic)
	r2 := chat(session, "刚才我说了什么？我的名字是什么？")
	fmt.Println("  Anthropic:", r2)
	fmt.Println()

	// ---- Step 3: OpenAI 追问 ----
	fmt.Println("--- Step 3: /switch openai ---")
	llmClient.SetProviderFilter(api.ProviderOpenAI)
	r3 := chat(session, "刚才 Anthropic 回复了什么内容？总结一下。")
	fmt.Println("  OpenAI:", r3)
	fmt.Println()

	// ---- 历史 ----
	fmt.Println("--- 完整对话历史 ---")
	for i, m := range session.msgs {
		c := ""
		if m.Content != nil {
			c = *m.Content
		}
		if len(c) > 100 {
			c = c[:100] + "..."
		}
		fmt.Printf("  [%d] %-9s %s\n", i, m.Role, c)
	}
	fmt.Println()
	fmt.Println("要点：对话历史跨 Provider 共享，/switch 只切换号池，不丢上下文。")
}

// =====================================================================
// 最小 AnthropicStrategy（演示扩展）
// =====================================================================
func init() {
	api.RegisterProviderStrategy(&exampleAnthropicStrategy{})
}

type exampleAnthropicStrategy struct{}

func (s *exampleAnthropicStrategy) Name() string                         { return "anthropic" }
func (s *exampleAnthropicStrategy) Endpoint() string                      { return "/v1/messages" }
func (s *exampleAnthropicStrategy) AuthHeader(apiKey string) (string, string) { return "x-api-key", apiKey }
func (s *exampleAnthropicStrategy) SSEHeaders() map[string]string {
	return map[string]string{"Accept": "text/event-stream", "anthropic-version": "2023-06-01"}
}
func (s *exampleAnthropicStrategy) ParseSSEEvent(_, _ string) ([]api.SSEEvent, error) { return nil, nil }

func (s *exampleAnthropicStrategy) BuildRequest(model string, messages []types.Message, _ []types.Tool, stream bool) ([]byte, error) {
	type aMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type aReq struct {
		Model     string `json:"model"`
		Messages  []aMsg `json:"messages"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system,omitempty"`
		Stream    bool   `json:"stream,omitempty"`
	}
	var sys string
	var msgs []aMsg
	for _, m := range messages {
		c := ""
		if m.Content != nil {
			c = *m.Content
		}
		if m.Role == "system" {
			sys = c
		} else {
			msgs = append(msgs, aMsg{Role: m.Role, Content: c})
		}
	}
	req := aReq{Model: model, Messages: msgs, MaxTokens: 4096, Stream: stream}
	if sys != "" {
		req.System = sys
	}
	return json.Marshal(req)
}

func (s *exampleAnthropicStrategy) ParseResponse(body []byte) (types.Message, error) {
	var raw struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return types.Message{}, fmt.Errorf("Anthropic parse: %w", err)
	}
	var full string
	for _, c := range raw.Content {
		full += c.Text
	}
	return types.Message{Role: "assistant", Content: &full}, nil
}

var _ api.ProviderStrategy = (*exampleAnthropicStrategy)(nil)

// =====================================================================
// session
// =====================================================================
type session struct {
	llm  types.ChatCompleter
	msgs []types.Message
}

func newSession(llm types.ChatCompleter, sysPrompt string) *session {
	return &session{llm: llm, msgs: []types.Message{{Role: "system", Content: &sysPrompt}}}
}

func (s *session) chat(ctx context.Context, input string) string {
	s.msgs = append(s.msgs, types.Message{Role: "user", Content: &input})
	msg, err := s.llm.Complete(ctx, s.msgs, nil)
	if err != nil {
		return fmt.Sprintf("[error: %v]", err)
	}
	if msg.Content != nil {
		s.msgs = append(s.msgs, msg)
		return *msg.Content
	}
	return ""
}

func chat(s *session, input string) string {
	return s.chat(context.Background(), input)
}

// =====================================================================
// Mock
// =====================================================================
type mockReq struct {
	Messages []types.Message `json:"messages"`
}
type mockResp struct{ Content string }
type mockHandler func(mockReq) mockResp

func newMockProvider(name, ep string, h mockHandler) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(ep, func(w http.ResponseWriter, r *http.Request) {
		var req mockReq
		json.NewDecoder(r.Body).Decode(&req)
		resp := h(req)
		switch name {
		case "openai":
			json.NewEncoder(w).Encode(map[string]any{
				"id": "mock-o",
				"choices": []map[string]any{{
					"index": 0,
					"message":       map[string]any{"role": "assistant", "content": resp.Content},
					"finish_reason": "stop",
				}},
			})
		case "anthropic":
			json.NewEncoder(w).Encode(map[string]any{
				"id": "mock-a",
				"content": []map[string]any{{"type": "text", "text": resp.Content}},
				"role":    "assistant",
			})
		}
	})
	return httptest.NewServer(mux)
}

func lastUserMsg(msgs []types.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && msgs[i].Content != nil {
			return *msgs[i].Content
		}
	}
	return ""
}
