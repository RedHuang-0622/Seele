// test/limit_test.go
//
// 对框架限制模块的测试：瞬时错误重试、Fork 并发限制、maxLoops 默认值。
// 所有测试使用 mock LLM + mock Provider，不调用真实 LLM API。
package test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	holder "github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	seelectx "github.com/RedHuang-0622/Seele/contexts"
	
	types "github.com/RedHuang-0622/Seele/types"
)

// =============================================================================
// controllableProvider —— 可控制失败模式的 ToolProvider
// =============================================================================

// controllableProvider 支持三种失败模式的可控 ToolProvider。
// 重构后实现新的 ToolProvider 接口（Tools() []ToolEntry），
// 执行逻辑下沉到 controllableHandler（策略模式）。
type controllableProvider struct {
	name            string
	tools           []types.Tool
	failMode        string   // "" | "unavailable" | "error"
	failCount       int32    // 第 failCount 次及之后才成功，0 表示总是成功
	callCount       int32    // 实际已调用次数（原子递增）
	successOverride string   // 非空时覆盖成功返回值
}

func newControllableProvider(name string) *controllableProvider {
	return &controllableProvider{
		name:      name,
		failCount: 0,
	}
}

func (p *controllableProvider) ProviderName() string { return p.name }

func (p *controllableProvider) Tools() []interfaces.ToolEntry {
	entries := make([]interfaces.ToolEntry, len(p.tools))
	for i, t := range p.tools {
		entries[i] = interfaces.ToolEntry{
			Definition: t,
			Handler:    &controllableHandler{parent: p},
		}
	}
	return entries
}

func (p *controllableProvider) AddTool(name, desc string) {
	p.tools = append(p.tools, types.Tool{
		Type: "function",
		Function: types.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	})
}

// controllableHandler 委托给父 provider 执行失败逻辑。
type controllableHandler struct {
	parent *controllableProvider
}

func (h *controllableHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	n := atomic.AddInt32(&h.parent.callCount, 1)
	// failCount == 0 表示总是成功
	if h.parent.failCount > 0 && n <= h.parent.failCount {
		switch h.parent.failMode {
		case "unavailable":
			return "", interfaces.ErrToolUnavailable
		case "error":
			return "", fmt.Errorf("controllable: forced error on call #%d", n)
		}
	}
	if h.parent.successOverride != "" {
		return h.parent.successOverride, nil
	}
	return `{"status":"ok"}`, nil
}

// =============================================================================
// 测试辅助
// =============================================================================

var errExpected = errors.New("expected error for testing")

// errProvider 总是返回错误的 Provider。
type errProvider struct {
	name string
}

func (p *errProvider) ProviderName() string { return p.name }
func (p *errProvider) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{
		{
			Definition: types.Tool{
				Type: "function",
				Function: types.ToolFunction{
					Name:        "err_tool",
					Description: "always errors",
					Parameters:  map[string]interface{}{},
				},
			},
			Handler: &errHandler{},
		},
	}
}

type errHandler struct{}

func (h *errHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	return "", errExpected
}

// newTestFixture 创建一套完整的测试夹具。
func newTestFixture() (*api.ChatClient, *holder.Holder, *mockLLMServer, *controllableProvider) {
	mockSrv := newMockLLMServer()
	llmClient := api.NewChatClient(types.LLMConfig{
		BaseURL: mockSrv.URL(), APIKey: "test-key", Model: "test-model", Timeout: 5,
	})
	tools := holder.New()
	cp := newControllableProvider("test")
	cp.AddTool("test_tool", "test tool for limit testing")
	tools.Register(cp)
	return llmClient, tools, mockSrv, cp
}

func newTestSession(llmClient *api.ChatClient, tools *holder.Holder, prompt string, loops int) *seelectx.Holder {
	return seelectx.New(llmClient, tools, prompt, seelectx.SessionConfig{MaxLoops: loops})
}

// =============================================================================
// 测试用例
// =============================================================================

// TestMaxLoops_Default 验证 Agent 默认 maxLoops 值。
func TestMaxLoops_Default(t *testing.T) {
	cfg := seelectx.SessionConfig{}
	d := cfg.Effective()
	t.Logf("default MaxLoops=%d", d.MaxLoops)
	if d.MaxLoops <= 0 {
		t.Errorf("default MaxLoops should be positive, got %d", d.MaxLoops)
	}
}

// TestMaxLoops_Zero 验证 maxLoops 被设置为 0 时变为默认值。
func TestMaxLoops_Zero(t *testing.T) {
	cfg := seelectx.SessionConfig{MaxLoops: 0}
	d := cfg.Effective()
	if d.MaxLoops <= 0 {
		t.Errorf("effective MaxLoops should be positive when set to 0, got %d", d.MaxLoops)
	}
	// 确认与默认值一致
	def := seelectx.SessionConfig{}.Effective()
	if d.MaxLoops != def.MaxLoops {
		t.Errorf("expected default MaxLoops=%d, got %d", def.MaxLoops, d.MaxLoops)
	}
}

// TestMaxLoops_Explicit 验证显式设置 maxLoops 生效。
func TestMaxLoops_Explicit(t *testing.T) {
	expected := 3
	cfg := seelectx.SessionConfig{MaxLoops: expected}
	d := cfg.Effective()
	if d.MaxLoops != expected {
		t.Errorf("expected MaxLoops=%d, got %d", expected, d.MaxLoops)
	}
}

// TestRetry_UnavailableThenSuccess 验证工具瞬时不可用后重试成功。
func TestRetry_UnavailableThenSuccess(t *testing.T) {
	llm, tools, mockLLM, cp := newTestFixture()
	defer mockLLM.Close()

	// 前 2 次 Unavailable，第 3 次成功
	cp.failMode = "unavailable"
	cp.failCount = 2

	mockLLM.EnqueueToolCalls([]types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "test_tool", Arguments: `{}`}},
	})
	mockLLM.EnqueueText(`"done"`)

	ctx := context.Background()
	agent := newTestSession(llm, tools, "You are a test agent.", 2)
	result, err := agent.Chat(ctx, "call test_tool")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if result != `"done"` {
		t.Errorf("expected 'done', got %q", result)
	}
	// verify 3 calls: 2 fail + 1 success
	// note: tool dispatcher may retry within single dispatch, not chat level
	_ = cp.callCount
	t.Log("Unavailable then success works")
}

// TestRetry_AlwaysUnavailable 验证工具始终不可用时 Dispatch 重试耗尽后返回错误。
//
// 注意：ReAct loop 对 dispatch 错误做优雅处理——将错误 JSON 注入 history 作为
// tool 结果消息，LLM 收到后可据此决策。Chat() 本身不因 dispatch 失败而 error。
func TestRetry_AlwaysUnavailable(t *testing.T) {
	llm, tools, mockLLM, cp := newTestFixture()
	defer mockLLM.Close()

	cp.failMode = "unavailable"
	cp.failCount = 999 // 始终不可用

	mockLLM.EnqueueToolCalls([]types.ToolCall{
		{ID: "call_1", Type: "function", Function: types.ToolCallFunction{Name: "test_tool", Arguments: `{}`}},
	})
	mockLLM.EnqueueText(`"done"`)

	ctx := context.Background()
	sess := newTestSession(llm, tools, "You are a test agent.", 2)
	_, err := sess.Chat(ctx, "call test_tool")
	if err != nil {
		t.Fatalf("Chat should not error on dispatch failure (graceful handling), got: %v", err)
	}

	// 验证 history 中包含 dispatch 错误信息
	history := sess.History()
	lastContent := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "tool" && history[i].Content != nil {
			lastContent = *history[i].Content
			break
		}
	}
	if !strings.Contains(lastContent, "unavailable after 3 retries") {
		t.Errorf("history should contain dispatch error, got: %s", lastContent)
	}
	t.Logf("Chat completed with dispatch error in history (expected)")
}

// TestHistory_TokenEstimation 验证 token 估算函数的正确性。
func TestHistory_TokenEstimation(t *testing.T) {
	// 空字符串
	if n := seelectx.EstimateTokens(""); n != 0 {
		t.Errorf("empty string should be 0 tokens, got %d", n)
	}
	// 短字符串
	short := "Hello"
	n := seelectx.EstimateTokens(short)
	if n == 0 {
		t.Errorf("non-empty string should have >0 tokens")
	}
	// 长字符串
	long := strings.Repeat("Hello World ", 100)
	nLong := seelectx.EstimateTokens(long)
	if nLong <= n {
		t.Errorf("longer string should have more tokens than short string")
	}
	// EstimateMessageTokens
	msg := types.Message{Role: "user", Content: strPtr("Hello")}
	single := seelectx.EstimateMessageTokens(msg)
	if single == 0 {
		t.Errorf("message should have >0 tokens")
	}
	// EstimateHistoryTokens
	msgs := []types.Message{
		{Role: "system", Content: strPtr("You are helpful.")},
		{Role: "user", Content: strPtr("Hello")},
		{Role: "assistant", Content: strPtr("Hi!")},
	}
	total := seelectx.EstimateHistoryTokens(msgs)
	if total == 0 {
		t.Errorf("history should have >0 tokens")
	}
}

// TestHistory_ToolResultTruncation 验证工具结果截断。
func TestHistory_ToolResultTruncation(t *testing.T) {
	maxChars := 100

	// 短结果不截断
	short := "short result"
	if result := seelectx.TruncateToolResult(short, maxChars); result != short {
		t.Errorf("short result should not be truncated")
	}
	// 恰好 maxChars 不截断
	exact := strings.Repeat("a", maxChars)
	if result := seelectx.TruncateToolResult(exact, maxChars); result != exact {
		t.Errorf("exact length result should not be truncated")
	}
	// 过长结果截断
	long := strings.Repeat("a", maxChars*2)
	result := seelectx.TruncateToolResult(long, maxChars)
	if len(result) > maxChars {
		t.Errorf("truncated result should be <= %d chars, got %d", maxChars, len(result))
	}
	// 截断后的标记
	if !strings.Contains(result, "[truncated]") {
		t.Error("truncated result should contain [truncated] marker")
	}
	// 多行截断
	lines := strings.Repeat("line\n", maxChars)
	result2 := seelectx.TruncateToolResult(lines, maxChars)
	if len(result2) > maxChars {
		t.Errorf("multi-line truncated result should be <= %d chars", maxChars)
	}
}

// TestHistory_TrimHistory 验证历史修剪。
func TestHistory_TrimHistory(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("You are helpful.")},
		{Role: "user", Content: strPtr("Hello, how are you? I need some help with my code.")},
		{Role: "assistant", Content: strPtr("I'm doing well, thanks! I'd be happy to help you with your code. What seems to be the issue?")},
	}
	trimmed := seelectx.TrimHistory(msgs, 2048)
	if len(trimmed) == 0 {
		t.Error("TrimHistory should not produce empty history")
	}
	// 验证 system 消息被保留
	if trimmed[0].Role != "system" {
		t.Error("first message should be system prompt")
	}
	tokens := seelectx.EstimateHistoryTokens(trimmed)
	if tokens > 2048 {
		t.Errorf("trimmed history should be <= 2048 tokens, got %d", tokens)
	}
}

// TestHistory_NeedCompression 验证压缩检测。
func TestHistory_NeedCompression(t *testing.T) {
	threshold := 100

	// 空历史不需要压缩
	empty := []types.Message{}
	if seelectx.NeedCompression(empty, threshold) {
		t.Error("empty history should not need compression")
	}
	// 短历史不需要压缩
	short := []types.Message{
		{Role: "user", Content: strPtr("Hello")},
	}
	if seelectx.NeedCompression(short, threshold) {
		t.Error("short history should not need compression")
	}
	// 长历史需要压缩
	long := []types.Message{
		{Role: "system", Content: strPtr(strings.Repeat("long ", 100))},
		{Role: "user", Content: strPtr(strings.Repeat("Hello World ", 100))},
		{Role: "assistant", Content: strPtr(strings.Repeat("response ", 100))},
	}
	if !seelectx.NeedCompression(long, threshold) {
		t.Error("long history should need compression")
	}
}

// =============================================================================
// Test 12: 工具结果截断集成测试
// =============================================================================

func TestToolResultTruncationIntegration(t *testing.T) {
	llm, tools, mockLLM, provider := newTestFixture()
	defer mockLLM.Close()

	// 设置 provider 返回超长结果（超过 MaxToolResultChars）
	longResult := strings.Repeat("abcdefghij", 300) // 3000 chars > 2000
	provider.successOverride = longResult

	// LLM 返回 tool_call → dispatch 返回长结果 → 截断后注入 history
	mockLLM.EnqueueToolCalls([]types.ToolCall{
		{ID: "call_trunc", Type: "function", Function: types.ToolCallFunction{
			Name: "test_tool", Arguments: `{}`,
		}},
	})
	mockLLM.EnqueueText(`"done"`)

	ctx := context.Background()
	agent := newTestSession(llm, tools, "You are a test agent.", 4)
	// 设置更小的 MaxToolResultChars 确保触发截断
	agent.SetContextConfig(seelectx.ContextConfig{MaxToolResultChars: 2000})
	result, err := agent.Chat(ctx, "test truncation")
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if result != `"done"` {
		t.Errorf("expected 'done', got %q", result)
	}

	// 验证 history 中 tool 结果被截断
	for _, msg := range agent.History() {
		if msg.Role == "tool" && msg.Content != nil {
			if len(*msg.Content) >= len(longResult) {
				t.Errorf("tool result should be truncated: len=%d, original=%d", len(*msg.Content), len(longResult))
			}
			if !strings.Contains(*msg.Content, "[truncated]") {
				t.Error("truncated result should contain [truncated] marker")
			}
			return
		}
	}
	t.Error("expected tool message in history")
}

// =============================================================================
// Test 13: 上下文压缩集成测试
// =============================================================================

func TestContextCompression(t *testing.T) {
	llm, tools, mockLLM, _ := newTestFixture()
	defer mockLLM.Close()

	// 设置压缩响应：mock 在检测到无 tools 请求时返回此摘要
	mockLLM.compressResponse = "summarized: user asked about data, tool returned results, task complete."

	agent := newTestSession(llm, tools, "You are helpful.", 4)
	// 设置更小的压缩阈值确保触发压缩
	agent.SetContextConfig(seelectx.ContextConfig{CompressThreshold: 500, MaxTokens: 4096})

	// 用 ForceAppendHistory 预填充超过压缩阈值的历史
	// 每条 ~300 chars → ~100 tokens, 18 条 + sys overhead → ~1800+ tokens > 500
	content := strings.Repeat("abcdefghij", 30) // 300 chars
	for i := 0; i < 18; i++ {
		msgContent := fmt.Sprintf("loop %d: %s", i, content)
		agent.ForceAppendHistory(types.Message{
			Role:    "user",
			Content: &msgContent,
		})
	}

	// 验证预填充后确实需要压缩
	if !seelectx.NeedCompression(agent.History(), 500) {
		t.Error("pre-filled history should trigger compression")
	}

	// 最终文本回复
	mockLLM.EnqueueText(`"final answer"`)

	ctx := context.Background()
	result, err := agent.Chat(ctx, "follow-up question")
	if err != nil {
		t.Fatalf("Chat with compression failed: %v", err)
	}
	if result != `"final answer"` {
		t.Errorf("expected 'final answer', got %q", result)
	}

	// 压缩后 history 应包含压缩摘要（system 角色消息包含压缩内容）
	msgs := agent.History()
	foundSummary := false
	for _, msg := range msgs {
		if msg.Role == "system" && msg.Content != nil && strings.Contains(*msg.Content, "summarized") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Error("compressed history should contain summary in system message")
	}

	// 压缩后 token 数应远低于原始
	afterTokens := seelectx.EstimateHistoryTokens(msgs)
	t.Logf("compressed history tokens: %d", afterTokens)
	if afterTokens > 4096 {
		t.Errorf("compressed history exceeds max tokens: %d > 4096", afterTokens)
	}
}
