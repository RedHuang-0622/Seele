// test/tracer_smoke_test.go
//
// 可观测性 Trace Tree 冒烟测试。
//
// 测试内容：
//   1. mock LLM 模式：验证 trace tree 结构（root/child spans、duration、attrs）
//   2. 真实 API 模式（可选）：验证完整链路（需 real 配置文件）
//
// 运行：
//   go test -race -v -run TestTraceMock ./test/          # mock 模式
//   go test -race -v -run TestTraceReal ./test/           # 真实 API（需配置）
//   go test -race -v -run TestTrace ./test/               # 全部
package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/seelectx/tracer"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/types"
)

// =============================================================================
// 内联 mockLLM — 只有 tracer 集成测试用到
// =============================================================================

type mockLLMResp struct {
	content   string
	toolCalls []types.ToolCall
}

type mockLLMServerInline struct {
	server *httptest.Server
	mu     sync.Mutex
	queue  []mockLLMResp
}

func newMockLLMInline() *mockLLMServerInline {
	m := &mockLLMServerInline{}
	m.server = httptest.NewServer(http.HandlerFunc(m.handler))
	return m
}

func (m *mockLLMServerInline) URL() string { return m.server.URL }
func (m *mockLLMServerInline) Close()      { m.server.Close() }

func (m *mockLLMServerInline) enqueueText(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResp{content: s})
}

func (m *mockLLMServerInline) enqueueTool(tcs []types.ToolCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queue = append(m.queue, mockLLMResp{toolCalls: tcs})
}

func (m *mockLLMServerInline) handler(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	var resp mockLLMResp
	if len(m.queue) > 0 {
		resp = m.queue[0]
		m.queue = m.queue[1:]
	} else {
		resp = mockLLMResp{content: "default"}
	}
	m.mu.Unlock()

	msg := map[string]interface{}{"role": "assistant"}
	if len(resp.toolCalls) > 0 {
		msg["content"] = nil
		msg["tool_calls"] = resp.toolCalls
	} else {
		msg["content"] = resp.content
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id": "mock",
		"choices": []map[string]interface{}{
			{"index": 0, "message": msg, "finish_reason": "stop"},
		},
	})
}

// =============================================================================
// TestTraceMock — mock LLM 验证 Trace Tree 结构
// =============================================================================

func TestTraceMock(t *testing.T) {
	t.Run("simple text response", func(t *testing.T) {
		mock := newMockLLMInline()
		defer mock.Close()
		mock.enqueueText("Hello world")

		a := newTestAgentInline(t, mock.URL())
		defer a.Shutdown()

		tr := tracer.NewSimpleTracer()
		eng := engine.New(a, engine.WithTracer(tr),
			engine.WithSystemPrompt("You are helpful."))

		reply, err := eng.Chat(context.Background(), "Say hello")
		if err != nil {
			t.Fatalf("Chat() failed: %v", err)
		}
		if reply == "" {
			t.Fatal("Chat() returned empty reply")
		}

		tree := eng.ExportTrace()
		if tree == nil || tree.Root == nil {
			t.Fatal("ExportTrace() returned nil tree")
		}
		if tree.TraceID == "" {
			t.Error("TraceID should be non-empty")
		}
		if tree.Root.Kind != tracer.SpanReActLoop {
			t.Errorf("Root kind should be %s, got %s", tracer.SpanReActLoop, tree.Root.Kind)
		}
		if len(tree.Root.Children) < 1 {
			t.Errorf("Root should have at least 1 child, got %d", len(tree.Root.Children))
		}
		if tree.Root.Attrs["user_input"] != "Say hello" {
			t.Errorf("Expected user_input='Say hello', got %q", tree.Root.Attrs["user_input"])
		}
		// Check LLM call span exists
		hasLLM := false
		for _, c := range tree.Root.Children {
			if c.Kind == tracer.SpanLLMCall {
				hasLLM = true
				if c.Duration <= 0 {
					t.Errorf("LLM call span duration should be > 0, got %v", c.Duration)
				}
				break
			}
		}
		if !hasLLM {
			t.Error("No LLM call span found in children")
		}
		t.Logf("Trace tree:\n%s", tree.String())
	})

	t.Run("with tool call", func(t *testing.T) {
		mock := newMockLLMInline()
		defer mock.Close()

		a := newTestAgentInline(t, mock.URL())
		defer a.Shutdown()
		a.RegisterTool(
			"echo", "echo tool",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args string) (string, error) {
				return `{"ok":true}`, nil
			},
		)

		mock.enqueueTool([]types.ToolCall{
			{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "echo", Arguments: "{}"}},
		})
		mock.enqueueText("Done")

		tr := tracer.NewSimpleTracer()
		eng := engine.New(a, engine.WithTracer(tr),
			engine.WithSystemPrompt("You are helpful."))

		reply, err := eng.Chat(context.Background(), "Run echo")
		if err != nil {
			t.Fatalf("Chat() failed: %v", err)
		}
		if reply == "" {
			t.Fatal("empty reply")
		}

		tree := eng.ExportTrace()
		if tree.Root == nil {
			t.Fatal("nil root")
		}
		if len(tree.Root.Children) < 2 {
			t.Fatalf("expected >=2 children, got %d", len(tree.Root.Children))
		}
		// Verify at least one tool_dispatch span
		hasTool := false
		for _, c := range tree.Root.Children {
			if c.Kind == tracer.SpanToolDispatch {
				hasTool = true
				if c.Attrs["tool"] != "echo" {
					t.Errorf("tool span attr[tool]=%q, expected 'echo'", c.Attrs["tool"])
				}
				break
			}
		}
		if !hasTool {
			t.Error("No tool_dispatch span found")
		}
		t.Logf("Trace tree:\n%s", tree.String())
	})

	t.Run("error in loop", func(t *testing.T) {
		mock := newMockLLMInline()
		defer mock.Close()
		// Enqueue multiple tool calls — the first one cancels the context,
		// causing the next LLM call to fail and verifying the trace error path.
		for i := 0; i < 5; i++ {
			mock.enqueueTool([]types.ToolCall{
				{ID: fmt.Sprintf("c%d", i), Type: "function",
					Function: types.ToolCallFunction{Name: "echo", Arguments: "{}"}},
			})
		}

		ctx, cancel := context.WithCancel(context.Background())

		a := newTestAgentInline(t, mock.URL())
		defer a.Shutdown()
		a.RegisterTool(
			"echo", "echo tool",
			map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			func(ctx context.Context, args string) (string, error) {
				cancel() // cancel context on first tool call → next LLM call fails
				return `{"ok":true}`, nil
			},
		)

		tr := tracer.NewSimpleTracer()
		eng := engine.New(a, engine.WithTracer(tr),
			engine.WithSystemPrompt("You are helpful."))

		_, err := eng.Chat(ctx, "Loop forever")
		if err == nil {
			t.Fatal("expected error from cancelled context")
		}
		t.Logf("Chat error: %v", err)

		tree := eng.ExportTrace()
		if tree.Root == nil {
			t.Fatal("nil root after error")
		}
		if tree.Root.Status != tracer.StatusError {
			t.Errorf("Expected root status %s after error, got %s", tracer.StatusError, tree.Root.Status)
		}
		t.Logf("Error trace:\n%s", tree.String())
	})
}

// =============================================================================
// TestTraceReal — 真实 API 冒烟测试
// =============================================================================

func TestTraceReal(t *testing.T) {
	result, err := api.LoadFullAccountsConfig("../config/account-openai.yaml")
	if err != nil {
		t.Skipf("config/account-openai.yaml 不存在，跳过真实 API 测试: %v", err)
	}

	ls := result.LLMDefaults
	pool := result.Pool
	acct := pool.All()[0]

	llmCfg := types.LLMConfig{
		BaseURL:   acct.BaseURL,
		APIKey:    acct.APIKey,
		Model:     acct.Model,
		MaxTokens: ls.MaxTokens,
	}

	agt, err := agent.New(agent.Options{
		LLMConfig:       llmCfg,
		HubStartupDelay: 10,
	})
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer agt.Shutdown()

	chatClient := agt.LLM().(*api.ChatClient)
	chatClient.WithAccountPool(pool)
	chatClient.SetProvider(ls.Provider)

	agt.RegisterTool(
		"ping", "responds with pong",
		map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		func(ctx context.Context, args string) (string, error) {
			return `{"reply":"pong"}`, nil
		},
	)

	tr := tracer.NewSimpleTracer()
	eng := engine.New(agt, engine.WithTracer(tr),
		engine.WithSystemPrompt("You are a helpful assistant."))

	t.Logf("Calling real API (model=%s)...", acct.Model)

	reply, err := eng.Chat(context.Background(), "Say hello and nothing else.")
	if err != nil {
		t.Fatalf("Chat() with real API failed: %v", err)
	}
	if reply == "" {
		t.Fatal("Chat() returned empty reply")
	}
	t.Logf("Reply: %s", reply)

	tree := eng.ExportTrace()
	if tree == nil || tree.Root == nil {
		t.Fatal("ExportTrace() returned nil tree")
	}

	// Verify structure
	if tree.TraceID == "" {
		t.Error("TraceID should be non-empty")
	}
	if tree.Root.Kind != tracer.SpanReActLoop {
		t.Errorf("Root kind should be %s, got %s", tracer.SpanReActLoop, tree.Root.Kind)
	}
	if tree.Root.Duration <= 0 {
		t.Error("Root Duration should be positive")
	}

	// Check LLM spans for token counts
	llmSpans := findSpans(tree.Root, tracer.SpanLLMCall)
	if len(llmSpans) == 0 {
		t.Fatal("No LLM call spans found")
	}
	hasTokens := false
	for _, s := range llmSpans {
		if s.Attrs["total_tokens"] != "" {
			hasTokens = true
			break
		}
	}
	if !hasTokens {
		t.Log("No token counts on LLM spans (expected with real API)")
		// This is informational, not a failure
	}

	t.Logf("Real API Trace Tree:\n%s", tree.String())
}

// =============================================================================
// 辅助函数
// =============================================================================

func newTestAgentInline(t *testing.T, mockURL string) *agent.Agent {
	t.Helper()
	a, err := agent.New(agent.Options{
		LLMConfig:       types.LLMConfig{BaseURL: mockURL, APIKey: "k", Model: "m"},
		HubStartupDelay: 10,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return a
}

// findSpans 递归查找指定 Kind 的所有 span。
func findSpans(node *tracer.Node, kind tracer.SpanKind) []*tracer.Node {
	var res []*tracer.Node
	if node.Kind == kind {
		res = append(res, node)
	}
	for _, c := range node.Children {
		res = append(res, findSpans(c, kind)...)
	}
	return res
}
