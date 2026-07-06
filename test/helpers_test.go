// test/helpers_test.go
//
// 测试基础设施：mock Provider 和通用辅助函数。
// 不含 LLM mock——编排查直接用 mock Agent，不需 LLM。
package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/RedHuang-0622/Seele/agent/api"
	"github.com/RedHuang-0622/Seele/agent/tool"
	seelectx "github.com/RedHuang-0622/Seele/context"
	
	types "github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
)

// =============================================================================
// mockProvider —— 基于内存 map 的 ToolProvider，用于单元测试
// 重构后实现新 ToolProvider 接口（Tools() []ToolEntry），执行逻辑在 mockHandler。
// =============================================================================

type mockProvider struct {
	name  string
	tools []types.Tool
}

func newMockProvider(name string) *mockProvider {
	return &mockProvider{name: name}
}

func (p *mockProvider) ProviderName() string { return p.name }

func (p *mockProvider) Tools() []tool.ToolEntry {
	entries := make([]tool.ToolEntry, len(p.tools))
	for i, t := range p.tools {
		name := t.Function.Name
		entries[i] = tool.ToolEntry{
			Definition: t,
			Handler:    &mockHandler{toolName: name},
		}
	}
	return entries
}

// mockHandler 实现 ToolHandler，返回预设的成功 JSON。
type mockHandler struct {
	toolName string
}

func (h *mockHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	return `{"status":"ok","tool":"` + h.toolName + `","args":` + argsJSON + `}`, nil
}

func (p *mockProvider) AddTool(name, desc string) {
	p.tools = append(p.tools, types.Tool{
		Type: "function",
		Function: types.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	})
}

// =============================================================================
// mockLLMServer —— OpenAI 兼容的 /chat/completions mock 端点
// =============================================================================

// mockLLMResponse 是一次 LLM 调用的预设回复。
// content 非空表示纯文本回复，toolCalls 非空表示工具调用回复。
type mockLLMResponse struct {
	content   string
	toolCalls []types.ToolCall
}

// mockLLMServer 提供 OpenAI 兼容的 /chat/completions 端点，
// 按 FIFO 顺序返回预先编排的回复，用于控制 Agent ReAct 循环。
type mockLLMServer struct {
	server           *httptest.Server
	mu               sync.Mutex
	queue            []mockLLMResponse
	defaultText      string
	compressResponse string // 若设置且请求无 tools，返回此压缩摘要
}

func newMockLLMServer() *mockLLMServer {
	m := &mockLLMServer{defaultText: `"ok"`}
	m.server = httptest.NewServer(http.HandlerFunc(m.handler))
	return m
}

// EnqueueText 向回复队列追加一条纯文本回复。
func (m *mockLLMServer) EnqueueText(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResponse{content: content})
}

// EnqueueToolCalls 向回复队列追加一条工具调用回复。
func (m *mockLLMServer) EnqueueToolCalls(toolCalls []types.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResponse{toolCalls: toolCalls})
}

func (m *mockLLMServer) URL() string { return m.server.URL }

func (m *mockLLMServer) Close() { m.server.Close() }

func (m *mockLLMServer) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []types.Message `json:"messages"`
		Tools    []types.Tool    `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	var resp mockLLMResponse

	// 压缩调用检测：无 tools 且设置了 compressResponse
	if len(req.Tools) == 0 && m.compressResponse != "" {
		resp = mockLLMResponse{content: m.compressResponse}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "compress_mock",
			"choices": []map[string]interface{}{
				{"index": 0, "message": map[string]interface{}{
					"role":    "assistant",
					"content": resp.content,
				}, "finish_reason": "stop"},
			},
		})
		return
	}

	m.mu.Lock()
	if len(m.queue) > 0 {
		resp = m.queue[0]
		m.queue = m.queue[1:]
	} else {
		resp = mockLLMResponse{content: m.defaultText}
	}
	m.mu.Unlock()

	msg := map[string]interface{}{
		"role": "assistant",
	}
	if len(resp.toolCalls) > 0 {
		msg["tool_calls"] = resp.toolCalls
		msg["content"] = nil
	} else {
		msg["content"] = resp.content
	}

	body := map[string]interface{}{
		"id": "mock",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"message":       msg,
				"finish_reason": "stop",
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(body)
}

// =============================================================================
// autoMockLLM —— 按对话状态自动切换 tool_calls / text 的 mock LLM
// =============================================================================

// autoMockLLM 根据对话历史自动决定返回 tool_calls 还是文本回复。
// 规则：
//   - 若最后一条消息是 tool 角色 → 返回 finalText（工具已执行完毕）
//   - 若提供了 tools 且最后一条消息是 user → 返回 tool_calls
//   - 其他情况 → 返回 defaultText
//
// 适合并发 Agent 测试：每个 Agent 的 ReAct 循环独立推进，不依赖队列顺序。
type autoMockLLM struct {
	server      *httptest.Server
	toolName    string
	toolArgs    string
	finalText   string
	defaultText string
}

func newAutoMockLLM(toolName, toolArgs, finalText string) *autoMockLLM {
	m := &autoMockLLM{
		toolName:    toolName,
		toolArgs:    toolArgs,
		finalText:   finalText,
		defaultText: `"ok"`,
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handler))
	return m
}

func (m *autoMockLLM) URL() string { return m.server.URL }
func (m *autoMockLLM) Close()      { m.server.Close() }

func (m *autoMockLLM) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []types.Message `json:"messages"`
		Tools    []types.Tool    `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	var content *string
	var toolCalls []types.ToolCall

	lastRole := ""
	if len(req.Messages) > 0 {
		lastRole = req.Messages[len(req.Messages)-1].Role
	}

	if lastRole == "tool" {
		// 工具结果已返回 → 给出最终文本
		s := m.finalText
		content = &s
	} else if len(req.Tools) > 0 && m.toolName != "" {
		// 有可用工具且未执行 → 发起 tool_call
		toolCalls = []types.ToolCall{
			{
				ID:   "call_auto",
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      m.toolName,
					Arguments: m.toolArgs,
				},
			},
		}
	} else {
		s := m.defaultText
		content = &s
	}

	msg := map[string]interface{}{"role": "assistant"}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		msg["content"] = nil
	} else {
		msg["content"] = content
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"id": "auto_mock",
		"choices": []map[string]interface{}{
			{"index": 0, "message": msg, "finish_reason": "stop"},
		},
	})
}

// =============================================================================
// sessionFactory —— 持有 LLM + 工具，用于创建测试会话
// =============================================================================

// sessionFactory 从 LLM 客户端 + tool_holder 创建 seelectx.Holder，
// 适配 workplan.AgentFactory 接口。
type sessionFactory struct {
	Llm   *api.ChatClient
	Tools *tool.Holder
}

func newSessionFactory(llmClient *api.ChatClient, tools *tool.Holder) *sessionFactory {
	return &sessionFactory{Llm: llmClient, Tools: tools}
}

func (f *sessionFactory) NewAgent(systemPrompt string) workplan.Agent {
	return seelectx.New(f.Llm, f.Tools, systemPrompt, seelectx.SessionConfig{MaxLoops: 1})
}

// newTestTools 快速创建一个带 LLM 的 tool_holder（用于 Fork 等多 Runtime 场景）。
func newTestTools(baseURL string) (*api.ChatClient, *tool.Holder) {
	llmClient := api.NewChatClient(types.LLMConfig{
		BaseURL: baseURL, APIKey: "x", Model: "x", Timeout: 5,
	})
	return llmClient, tool.New()
}

// strPtr 返回字符串指针，用于构造 Message.Content。
func strPtr(s string) *string { return &s }
