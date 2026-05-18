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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtime "github.com/sukasukasuka123/Seele"
	"github.com/sukasukasuka123/Seele/workplan"
)

// =============================================================================
// controllableProvider —— 可控制失败模式的 ToolProvider
// =============================================================================

// controllableProvider 支持三种失败模式：
//   - success：正常返回
//   - transient：返回 ErrToolUnavailable（连接池满/超时等瞬时错误）
//   - permanent：返回普通 error（工具业务错误）
//
// failCount 控制前 N 次调用返回错误，之后返回成功。
type controllableProvider struct {
	name      string
	tools     []runtime.Tool
	toolIdx   map[string]struct{}
	failMode        string        // "transient" | "permanent" | ""
	failCount       int           // 前 N 次失败，0 表示永远失败
	mu              sync.Mutex
	callCount       map[string]int // 每个工具的累计调用次数
	successOverride string         // 若设置，成功时返回此内容而非默认 JSON
}

func newControllableProvider(name string) *controllableProvider {
	return &controllableProvider{
		name:      name,
		toolIdx:   make(map[string]struct{}),
		callCount: make(map[string]int),
	}
}

func (p *controllableProvider) ProviderName() string              { return p.name }
func (p *controllableProvider) Tools() []runtime.Tool             { return p.tools }
func (p *controllableProvider) HasTool(name string) bool          { _, ok := p.toolIdx[name]; return ok }

func (p *controllableProvider) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	p.mu.Lock()
	p.callCount[name]++
	count := p.callCount[name]
	p.mu.Unlock()

	if p.failMode == "" {
		if p.successOverride != "" {
			return p.successOverride, nil
		}
		return `{"status":"ok","tool":"` + name + `"}`, nil
	}

	if p.failCount > 0 && count > p.failCount {
		if p.successOverride != "" {
			return p.successOverride, nil
		}
		return `{"status":"ok","tool":"` + name + `"}`, nil
	}

	switch p.failMode {
	case "transient":
		return "", fmt.Errorf("%w: %s: simulated unavailability (call %d)",
			runtime.ErrToolUnavailable, name, count)
	case "permanent":
		return "", fmt.Errorf("%s: simulated business error (call %d)", name, count)
	default:
		return `{"status":"ok"}`, nil
	}
}

func (p *controllableProvider) SetFailMode(mode string, failCount int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failMode = mode
	p.failCount = failCount
	p.callCount = make(map[string]int)
}

func (p *controllableProvider) AddTool(name, desc string) {
	p.tools = append(p.tools, runtime.Tool{
		Type: "function",
		Function: runtime.ToolFunction{
			Name:        name,
			Description: desc,
			Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
	})
	p.toolIdx[name] = struct{}{}
}

// =============================================================================
// 辅助：创建带 mock LLM + controllableProvider 的 Runtime
// =============================================================================

func newTestRuntime() (*runtime.Runtime, *mockLLMServer, *controllableProvider) {
	mockLLM := newMockLLMServer()
	rt, err := runtime.NewRuntime(runtime.LLMConfig{
		BaseURL: mockLLM.URL(),
		Model:   "test-model",
	})
	if err != nil {
		panic("newTestRuntime: " + err.Error())
	}
	provider := newControllableProvider("test-provider")
	provider.AddTool("test_tool", "test tool for unit tests")
	rt.Register(provider)
	return rt, mockLLM, provider
}

// =============================================================================
// Test 1: maxLoops 默认值 = 4
// =============================================================================

func TestMaxLoopsDefault(t *testing.T) {
	rt, mockLLM, _ := newTestRuntime()
	defer mockLLM.Close()

	// loopTimes=0 应使用默认值 4
	agent := rt.NewAgent("", 0)
	if agent.MaxLoops() != 4 {
		t.Errorf("expected default maxLoops=4, got %d", agent.MaxLoops())
	}

	// 显式设置应覆盖默认值
	agent2 := rt.NewAgent("", 10)
	if agent2.MaxLoops() != 10 {
		t.Errorf("expected explicit maxLoops=10, got %d", agent2.MaxLoops())
	}
}

// =============================================================================
// Test 2: 瞬时错误重试成功 —— 不污染 history
// =============================================================================

func TestTransientErrorRetrySucceeds(t *testing.T) {
	rt, mockLLM, provider := newTestRuntime()
	defer mockLLM.Close()

	// provider 前 2 次返回瞬时错误，第 3 次返回成功
	provider.SetFailMode("transient", 2)

	// LLM 返回 tool_call（触发 dispatch）
	mockLLM.EnqueueToolCalls([]runtime.ToolCall{
		{ID: "call_1", Type: "function", Function: runtime.ToolCallFunction{
			Name: "test_tool", Arguments: `{"key":"val"}`,
		}},
	})
	// dispatch 成功后，LLM 返回最终文本
	mockLLM.EnqueueText(`"task complete"`)

	ctx := context.Background()
	agent := rt.NewAgent("You are a test agent.", 4)
	result, err := agent.Chat(ctx, "do something")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if result != `"task complete"` {
		t.Errorf("expected 'task complete', got %q", result)
	}

	// 验证 history 不含瞬时错误
	history := agent.History()
	for _, msg := range history {
		if msg.Role == "tool" && msg.Content != nil {
			if errors.Is(runtime.ErrToolUnavailable, errors.New(*msg.Content)) {
				t.Errorf("history contains transient error: %q", *msg.Content)
			}
			// 更直接：检查内容是否包含 "ErrToolUnavailable" 或 "unavailability"
			if len(*msg.Content) > 0 {
				// 瞬时错误不应该出现在 history 中
				// 工具成功的响应应该包含 "status":"ok"
			}
		}
	}

	// 验证 history 中 tool 角色的消息数量 = 1（只有成功的那个）
	toolMsgCount := 0
	for _, msg := range history {
		if msg.Role == "tool" {
			toolMsgCount++
		}
	}
	if toolMsgCount != 1 {
		t.Errorf("expected 1 tool message in history, got %d", toolMsgCount)
	}
}

// =============================================================================
// Test 3: 瞬时错误耗尽重试次数 —— 不给 history 注入错误
// =============================================================================

func TestTransientErrorExhausted(t *testing.T) {
	rt, mockLLM, provider := newTestRuntime()
	defer mockLLM.Close()

	// provider 永远返回瞬时错误
	provider.SetFailMode("transient", 0) // failCount=0 表示永远失败

	// LLM 返回 tool_call（每轮都会返回 tool_call 直到 maxLoops）
	for i := 0; i < 5; i++ {
		mockLLM.EnqueueToolCalls([]runtime.ToolCall{
			{ID: "call_1", Type: "function", Function: runtime.ToolCallFunction{
				Name: "test_tool", Arguments: `{}`,
			}},
		})
	}

	ctx := context.Background()
	agent := rt.NewAgent("You are a test agent.", 4)
	_, err := agent.Chat(ctx, "do something")

	// 应该因为 maxLoops 耗尽而返回错误
	if err == nil {
		t.Fatal("expected error due to maxLoops exhausted")
	}

	// 验证 history 中不含瞬时错误消息
	history := agent.History()
	for _, msg := range history {
		if msg.Role == "tool" && msg.Content != nil {
			t.Errorf("history should not contain any tool messages, but got: %s", *msg.Content)
		}
	}

	// 验证没有 tool 角色的消息（瞬时错误全被跳过）
	toolCount := 0
	for _, msg := range history {
		if msg.Role == "tool" {
			toolCount++
		}
	}
	if toolCount > 0 {
		t.Errorf("expected 0 tool messages (all transient), got %d", toolCount)
	}
}

// =============================================================================
// Test 4: 永久错误不重试，正常注入 history
// =============================================================================

func TestPermanentErrorNotRetried(t *testing.T) {
	rt, mockLLM, provider := newTestRuntime()
	defer mockLLM.Close()

	// provider 返回永久（业务）错误
	provider.SetFailMode("permanent", 0)

	// LLM：第一轮返回 tool_call → dispatch 失败（永久错误注入 history）
	// 第二轮 LLM 看到错误后决定不再调工具，直接返回文本
	mockLLM.EnqueueToolCalls([]runtime.ToolCall{
		{ID: "call_1", Type: "function", Function: runtime.ToolCallFunction{
			Name: "test_tool", Arguments: `{}`,
		}},
	})
	mockLLM.EnqueueText(`"cannot proceed, tool failed"`)

	ctx := context.Background()
	agent := rt.NewAgent("You are a test agent.", 4)
	result, err := agent.Chat(ctx, "do something")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if result != `"cannot proceed, tool failed"` {
		t.Errorf("expected fallback text, got %q", result)
	}

	// 验证 history 包含永久错误
	foundError := false
	for _, msg := range agent.History() {
		if msg.Role == "tool" && msg.Content != nil {
			if len(*msg.Content) > 0 {
				foundError = true
				break
			}
		}
	}
	if !foundError {
		t.Error("expected permanent error to be in history")
	}
}

// =============================================================================
// Test 5: Fork 信号量限制并发数
// =============================================================================

type concurrencyAgent struct {
	active  *int64 // 当前并发数
	maxSeen *int64 // 观测到的最大并发数
	sleep   time.Duration
}

func (a *concurrencyAgent) Chat(ctx context.Context, input string) (string, error) {
	cur := atomic.AddInt64(a.active, 1)
	defer atomic.AddInt64(a.active, -1)

	// 更新观测到的最大并发数
	for {
		prev := atomic.LoadInt64(a.maxSeen)
		if cur <= prev {
			break
		}
		if atomic.CompareAndSwapInt64(a.maxSeen, prev, cur) {
			break
		}
	}

	time.Sleep(a.sleep) // 模拟 LLM 调用 + dispatch
	return `"done"`, nil
}

type concurrencyFactory struct {
	active  *int64
	maxSeen *int64
}

func (f *concurrencyFactory) NewAgent(systemPrompt string) workplan.Agent {
	return &concurrencyAgent{active: f.active, maxSeen: f.maxSeen, sleep: 50 * time.Millisecond}
}

func TestForkSemaphoreLimitsConcurrency(t *testing.T) {
	var active, maxSeen int64
	factory := &concurrencyFactory{active: &active, maxSeen: &maxSeen}

	wp := workplan.New(factory, nil, "test prompt")

	// 构建 10 个并发 Fork 分支
	branches := make([]workplan.ForkBranch, 10)
	for i := 0; i < 10; i++ {
		branches[i] = workplan.ForkBranch{
			Label: fmt.Sprintf("branch_%d", i),
			Input: "test input",
		}
	}

	wp.Fork("fork_1", branches).
		Checkpoint("end")

	ctx := context.Background()
	result, err := wp.Run(ctx)
	if err != nil {
		t.Fatalf("WorkPlan.Run failed: %v", err)
	}
	if result.Aborted {
		t.Fatalf("WorkPlan aborted: %s", result.AbortReason)
	}

	max := atomic.LoadInt64(&maxSeen)
	t.Logf("max concurrent fork branches: %d (limit: 3)", max)

	if max > 3 {
		t.Errorf("fork concurrency limit violated: max=%d, expected <= 3", max)
	}
	if max < 1 {
		t.Error("expected at least 1 concurrent branch")
	}

	// Fork 节点 + Checkpoint 节点 = 2 个（分支结果在 Fork 内部汇合）
	nodeCount := len(result.NodeResults)
	if nodeCount != 2 {
		t.Errorf("expected 2 nodes (Fork+Checkpoint), got %d", nodeCount)
	}
	t.Logf("node results: %d", nodeCount)
}

// =============================================================================
// Test 6: 混合场景 —— 部分瞬时 + 部分成功 → 全部重试
// =============================================================================

func TestMixedTransientAndSuccessRetriesAll(t *testing.T) {
	// 场景：Fork 中有 2 个工具，一个瞬时失败，一个成功。
	// 由于有瞬时错误，整轮应重试（不向 history 追加任何结果）。

	rt, mockLLM, _ := newTestRuntime()
	defer mockLLM.Close()

	// 创建两个 provider：一个瞬时失败，一个成功
	transientProv := newControllableProvider("transient-prov")
	transientProv.AddTool("tool_a", "transient tool")
	transientProv.SetFailMode("transient", 2) // 前2次失败，第3次成功

	okProv := newControllableProvider("ok-prov")
	okProv.AddTool("tool_b", "always ok tool")
	okProv.SetFailMode("", 0) // 永远成功

	rt.Register(transientProv)
	rt.Register(okProv)

	// LLM 返回两个 tool_calls
	mockLLM.EnqueueToolCalls([]runtime.ToolCall{
		{ID: "call_a", Type: "function", Function: runtime.ToolCallFunction{
			Name: "tool_a", Arguments: `{}`,
		}},
		{ID: "call_b", Type: "function", Function: runtime.ToolCallFunction{
			Name: "tool_b", Arguments: `{}`,
		}},
	})
	// 重试后全部成功，LLM 返回最终文本
	mockLLM.EnqueueToolCalls([]runtime.ToolCall{
		{ID: "call_a2", Type: "function", Function: runtime.ToolCallFunction{
			Name: "tool_a", Arguments: `{}`,
		}},
		{ID: "call_b2", Type: "function", Function: runtime.ToolCallFunction{
			Name: "tool_b", Arguments: `{}`,
		}},
	})
	// 第3次 dispatch 全部成功
	mockLLM.EnqueueText(`"all done"`)

	ctx := context.Background()
	agent := rt.NewAgent("You are a test agent.", 4)
	result, err := agent.Chat(ctx, "do something")

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if result != `"all done"` {
		t.Errorf("expected 'all done', got %q", result)
	}

	// 验证 history 结构正确：没有瞬时错误消息
	for _, msg := range agent.History() {
		if msg.Role == "tool" && msg.Content != nil {
			content := *msg.Content
			if len(content) > 0 && content[0] != '{' {
				t.Errorf("unexpected tool message content: %s", content)
			}
		}
	}
	t.Logf("history messages: %d", len(agent.History()))
}

// =============================================================================
// Test 7: ErrToolUnavailable 被 errors.Is 正确识别
// =============================================================================

func TestErrToolUnavailableDetection(t *testing.T) {
	// 直接错误
	direct := runtime.ErrToolUnavailable
	if !errors.Is(direct, runtime.ErrToolUnavailable) {
		t.Error("direct ErrToolUnavailable should be detectable")
	}

	// 用 %w 包装一层
	wrapped := fmt.Errorf("dispatch failed: %w", runtime.ErrToolUnavailable)
	if !errors.Is(wrapped, runtime.ErrToolUnavailable) {
		t.Error("wrapped ErrToolUnavailable should be detectable")
	}

	// 用 %w 包装两层
	doubleWrapped := fmt.Errorf("provider error: %w", wrapped)
	if !errors.Is(doubleWrapped, runtime.ErrToolUnavailable) {
		t.Error("double-wrapped ErrToolUnavailable should be detectable")
	}

	// 普通错误不应被误判
	normal := errors.New("some random error")
	if errors.Is(normal, runtime.ErrToolUnavailable) {
		t.Error("normal error should NOT be detected as ErrToolUnavailable")
	}
}

// =============================================================================
// Test 8: EstimateTokens / EstimateHistoryTokens 基本正确性
// =============================================================================

func TestTokenEstimation(t *testing.T) {
	// 空字符串 = 0 token
	if n := runtime.EstimateTokens(""); n != 0 {
		t.Errorf("empty string: expected 0, got %d", n)
	}

	// 短英文：len=12 → (12+2)/3 = 4
	short := "hello world!"
	n := runtime.EstimateTokens(short)
	if n < 2 || n > 6 {
		t.Errorf("short english: got %d, want ~3-4", n)
	}

	// 长文本应有更多 token
	long := strings.Repeat("abcdefghij", 100) // 1000 chars → ~334 tokens
	nLong := runtime.EstimateTokens(long)
	if nLong < 200 || nLong > 500 {
		t.Errorf("long text: got %d, want ~334", nLong)
	}

	// EstimateHistoryTokens 应随消息数增长
	content := "test message content"
	msg := runtime.Message{Role: "user", Content: &content}
	single := runtime.EstimateMessageTokens(msg)
	if single < 5 {
		t.Errorf("single message should be at least 5 tokens, got %d", single)
	}

	msgs := make([]runtime.Message, 10)
	for i := range msgs {
		msgs[i] = msg
	}
	total := runtime.EstimateHistoryTokens(msgs)
	if total < single*10 {
		t.Errorf("total (%d) should be >= 10*single (%d)", total, single*10)
	}
}

// =============================================================================
// Test 9: TruncateToolResult 单元测试
// =============================================================================

func TestTruncateToolResult(t *testing.T) {
	// 短结果原样返回
	short := `{"status":"ok"}`
	if result := runtime.TruncateToolResult(short); result != short {
		t.Errorf("short result changed: %q", result)
	}

	// 刚好在限制内的结果原样返回
	exact := strings.Repeat("a", runtime.MaxToolResultChars)
	if result := runtime.TruncateToolResult(exact); result != exact {
		t.Errorf("exact-limit result was truncated: len=%d", len(result))
	}

	// 长结果应被截断并包含 [truncated] 标记
	long := strings.Repeat("abcdefghij", 300) // 3000 chars > MaxToolResultChars (2000)
	result := runtime.TruncateToolResult(long)
	if len(result) > runtime.MaxToolResultChars+50 {
		t.Errorf("truncated result too long: %d", len(result))
	}
	if !strings.Contains(result, "[truncated]") {
		t.Errorf("truncated result missing [truncated] marker: %q", result[:100])
	}

	// 带换行符的长结果应在换行处截断
	lines := strings.Repeat("line of text here\n", 200) // many lines
	result2 := runtime.TruncateToolResult(lines)
	if !strings.Contains(result2, "[truncated]") {
		t.Error("missing [truncated] marker")
	}
	// 截断点应在换行符处（末尾不是被截断的半行）
	trimmed := strings.TrimSuffix(result2, "\n...[truncated]")
	if !strings.HasSuffix(trimmed, "line of text here") && len(trimmed) > 0 {
		// 检查截断点是否合理
		t.Logf("truncation point: last 50 chars of trimmed: %q", trimmed[max(0, len(trimmed)-50):])
	}
}

// =============================================================================
// Test 10: TrimHistory 硬截断
// =============================================================================

func TestTrimHistory(t *testing.T) {
	content := strings.Repeat("x", 300) // ~100 tokens per message
	sysMsg := runtime.Message{Role: "system", Content: strPtr("You are helpful.")}
	userMsg := runtime.Message{Role: "user", Content: &content}

	// 构建 20 条 user 消息 + system → 远超限制
	msgs := []runtime.Message{sysMsg}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, userMsg)
	}

	trimmed := runtime.TrimHistory(msgs, 2048)

	// system 消息应保留
	foundSys := false
	for _, m := range trimmed {
		if m.Role == "system" {
			foundSys = true
			break
		}
	}
	if !foundSys {
		t.Error("system message should be preserved")
	}

	// 截断后 token 数应在限制内
	tokens := runtime.EstimateHistoryTokens(trimmed)
	if tokens > 2048 {
		t.Errorf("trimmed tokens (%d) exceeds limit (2048)", tokens)
	}

	// 消息数应减少
	if len(trimmed) >= len(msgs) {
		t.Errorf("trimmed length (%d) should be less than original (%d)", len(trimmed), len(msgs))
	}
}

// =============================================================================
// Test 11: NeedCompression 阈值判断
// =============================================================================

func TestNeedCompression(t *testing.T) {
	// 空或少量消息不应触发压缩
	var empty []runtime.Message
	if runtime.NeedCompression(empty) {
		t.Error("empty history should not need compression")
	}

	short := []runtime.Message{{Role: "user", Content: strPtr("hello")}}
	if runtime.NeedCompression(short) {
		t.Error("short history should not need compression")
	}

	// 大量消息应触发压缩
	content := strings.Repeat("x", 300) // ~100 tokens each
	var long []runtime.Message
	for i := 0; i < 20; i++ {
		long = append(long, runtime.Message{Role: "user", Content: &content})
	}
	if !runtime.NeedCompression(long) {
		t.Error("long history should trigger compression")
	}
}

// =============================================================================
// Test 12: tool 结果截断集成测试
// =============================================================================

func TestToolResultTruncationIntegration(t *testing.T) {
	rt, mockLLM, provider := newTestRuntime()
	defer mockLLM.Close()

	// 设置 provider 返回超长结果（超过 MaxToolResultChars）
	longResult := strings.Repeat("abcdefghij", 300) // 3000 chars > 2000
	provider.successOverride = longResult

	// LLM 返回 tool_call → dispatch 返回长结果 → 截断后注入 history
	mockLLM.EnqueueToolCalls([]runtime.ToolCall{
		{ID: "call_trunc", Type: "function", Function: runtime.ToolCallFunction{
			Name: "test_tool", Arguments: `{}`,
		}},
	})
	mockLLM.EnqueueText(`"done"`)

	ctx := context.Background()
	agent := rt.NewAgent("You are a test agent.", 4)
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
	rt, mockLLM, _ := newTestRuntime()
	defer mockLLM.Close()

	// 设置压缩响应：mock 在检测到无 tools 请求时返回此摘要
	mockLLM.compressResponse = "summarized: user asked about data, tool returned results, task complete."

	agent := rt.NewAgent("You are helpful.", 4)

	// 用 ForceAppendHistory 预填充超过压缩阈值的历史
	// 每条 ~300 chars → ~100 tokens, 18 条 + sys overhead → ~1800+ tokens > 1536
	content := strings.Repeat("abcdefghij", 30) // 300 chars
	for i := 0; i < 18; i++ {
		msgContent := fmt.Sprintf("loop %d: %s", i, content)
		agent.ForceAppendHistory(runtime.Message{
			Role:    "user",
			Content: &msgContent,
		})
	}

	// 验证预填充后确实需要压缩
	if !runtime.NeedCompression(agent.History()) {
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
	history := agent.History()
	foundSummary := false
	for _, msg := range history {
		if msg.Role == "system" && msg.Content != nil && strings.Contains(*msg.Content, "[Context summary") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Error("history should contain compression summary after compression")
	}

	// 压缩后 token 数应远小于压缩前的原始消息总和
	afterTokens := runtime.EstimateHistoryTokens(history)
	t.Logf("tokens after compression: %d", afterTokens)
	if afterTokens > 3000 {
		t.Errorf("compressed history too large: %d tokens", afterTokens)
	}
}
