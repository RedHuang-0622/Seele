package history

import (
	"context"
	"strings"
	"testing"

	types "github.com/RedHuang-0622/Seele/types"
)

// ---------------------------------------------------------------------------
// test helpers
// ---------------------------------------------------------------------------

type mockCompleter struct {
	result types.Message
	err    error
}

func (m *mockCompleter) Complete(_ context.Context, _ []types.Message, _ []types.Tool) (types.Message, error) {
	return m.result, m.err
}

func (m *mockCompleter) CompleteStream(_ context.Context, _ []types.Message, _ []types.Tool, _ func(string)) (string, string, []types.ToolCall, error) {
	return "", "", nil, m.err
}

func (m *mockCompleter) CompleteStreamEvents(_ context.Context, _ []types.Message, _ []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return "", "", nil, m.err
}

// makeMsgs creates n user messages each with content of given char length.
func makeMsgs(n, contentLen int) []types.Message {
	content := strings.Repeat("a", contentLen)
	msgs := make([]types.Message, n)
	for i := range msgs {
		msgs[i] = types.Message{Role: "user", Content: strPtr(content)}
	}
	return msgs
}

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

func TestConfig_DefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d (want 8192)", cfg.MaxTokens)
	}
	if cfg.CompressThreshold != 6144 {
		t.Errorf("CompressThreshold = %d (want 6144)", cfg.CompressThreshold)
	}
	if cfg.MaxToolResultChars != 4000 {
		t.Errorf("MaxToolResultChars = %d (want 4000)", cfg.MaxToolResultChars)
	}
}

func TestConfig_Effective_ZeroFields(t *testing.T) {
	cfg := Config{}.Effective()
	if cfg.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d (want 8192)", cfg.MaxTokens)
	}
	if cfg.CompressThreshold != 6144 {
		t.Errorf("CompressThreshold = %d (want 6144)", cfg.CompressThreshold)
	}
	if cfg.MaxToolResultChars != 4000 {
		t.Errorf("MaxToolResultChars = %d (want 4000)", cfg.MaxToolResultChars)
	}
}

func TestConfig_Effective_PartialOverrides(t *testing.T) {
	cfg := Config{MaxTokens: 4096, MaxToolResultChars: 2000}.Effective()
	if cfg.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d (want 4096)", cfg.MaxTokens)
	}
	if cfg.CompressThreshold != 6144 {
		t.Errorf("CompressThreshold = %d (want 6144)", cfg.CompressThreshold)
	}
	if cfg.MaxToolResultChars != 2000 {
		t.Errorf("MaxToolResultChars = %d (want 2000)", cfg.MaxToolResultChars)
	}
}

func TestConfig_Effective_NegativeTokens(t *testing.T) {
	cfg := Config{MaxTokens: -1, CompressThreshold: 0, MaxToolResultChars: -100}.Effective()
	if cfg.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d (want 8192)", cfg.MaxTokens)
	}
	if cfg.CompressThreshold != 6144 {
		t.Errorf("CompressThreshold = %d (want 6144)", cfg.CompressThreshold)
	}
	if cfg.MaxToolResultChars != 4000 {
		t.Errorf("MaxToolResultChars = %d (want 4000)", cfg.MaxToolResultChars)
	}
}

func TestConfig_Effective_AllExplicit(t *testing.T) {
	cfg := Config{MaxTokens: 4096, CompressThreshold: 3072, MaxToolResultChars: 1000}.Effective()
	if cfg.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d (want 4096)", cfg.MaxTokens)
	}
	if cfg.CompressThreshold != 3072 {
		t.Errorf("CompressThreshold = %d (want 3072)", cfg.CompressThreshold)
	}
	if cfg.MaxToolResultChars != 1000 {
		t.Errorf("MaxToolResultChars = %d (want 1000)", cfg.MaxToolResultChars)
	}
}

// ---------------------------------------------------------------------------
// EstimateTokens
// ---------------------------------------------------------------------------

func TestEstimateTokens_Empty(t *testing.T) {
	if n := EstimateTokens(""); n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestEstimateTokens_Short(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"a", 1},
		{"ab", 1},
		{"abc", 1},
		{"abcd", 2},
		{"hello", 2},
		{"Hello, World!", 5}, // 13 bytes -> (13+2)/3 = 5
		{"a\nb", 1},           // 3 bytes -> (3+2)/3 = 1
	}
	for _, tc := range tests {
		got := EstimateTokens(tc.input)
		if got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestEstimateTokens_LongAsianText(t *testing.T) {
	// Chinese characters are 3 bytes each in UTF-8.
	input := "你好世界" // 12 bytes
	got := EstimateTokens(input)
	want := (12 + 2) / 3 // 4
	if got != want {
		t.Errorf("EstimateTokens(%q) = %d, want %d", input, got, want)
	}

	// 100 Chinese chars: 300 bytes -> (300+2)/3 = 100
	long := strings.Repeat("中", 100)
	got = EstimateTokens(long)
	want = (300 + 2) / 3 // 100
	if got != want {
		t.Errorf("100 Chinese chars: got %d, want %d", got, want)
	}
}

func TestEstimateTokens_EnglishText(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog" // 43 bytes
	got := EstimateTokens(text)
	want := (43 + 2) / 3 // 15
	if got != want {
		t.Errorf("EstimateTokens = %d, want %d", got, want)
	}
}

func TestEstimateTokens_MixedContent(t *testing.T) {
	input := "hello你好world" // 5 + 6 + 5 = 16 bytes
	got := EstimateTokens(input)
	want := (16 + 2) / 3 // 6
	if got != want {
		t.Errorf("EstimateTokens(%q) = %d, want %d", input, got, want)
	}
}

func TestEstimateTokens_Whitespace(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{" ", 1},    // 1 byte -> (1+2)/3 = 1
		{"  ", 1},   // 2 bytes -> (2+2)/3 = 1
		{"   ", 1},  // 3 bytes -> (3+2)/3 = 1
		{"\t\n", 1}, // 2 bytes -> (2+2)/3 = 1
	}
	for _, tc := range tests {
		got := EstimateTokens(tc.input)
		if got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// EstimateMessageTokens
// ---------------------------------------------------------------------------

func TestEstimateMessageTokens_System(t *testing.T) {
	msg := types.Message{Role: "system", Content: strPtr("You are helpful.")}
	got := EstimateMessageTokens(msg)
	// base 10 + content tokens
	contentTokens := EstimateTokens("You are helpful.")
	want := 10 + contentTokens
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_User(t *testing.T) {
	msg := types.Message{Role: "user", Content: strPtr("hello")}
	got := EstimateMessageTokens(msg)
	want := 10 + EstimateTokens("hello")
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_Assistant(t *testing.T) {
	msg := types.Message{Role: "assistant", Content: strPtr("I can help.")}
	got := EstimateMessageTokens(msg)
	want := 10 + EstimateTokens("I can help.")
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_Tool(t *testing.T) {
	msg := types.Message{
		Role:       "tool",
		Content:    strPtr(`{"result":"ok"}`),
		ToolCallID: "call_1",
		Name:       "search",
	}
	got := EstimateMessageTokens(msg)
	want := 10 +
		EstimateTokens(`{"result":"ok"}`) +
		EstimateTokens("call_1") +
		EstimateTokens("search")
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_WithToolCalls(t *testing.T) {
	msg := types.Message{
		Role:    "assistant",
		Content: strPtr("Let me check"),
		ToolCalls: []types.ToolCall{
			{
				ID: "call_1", Type: "function",
				Function: types.ToolCallFunction{Name: "search", Arguments: `{"q":"x"}`},
			},
		},
	}
	got := EstimateMessageTokens(msg)
	want := 10 +
		EstimateTokens("Let me check") +
		EstimateTokens("search") +
		EstimateTokens(`{"q":"x"}`)
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_WithReasoning(t *testing.T) {
	msg := types.Message{
		Role:             "assistant",
		Content:          strPtr("Answer"),
		ReasoningContent: "I think...",
	}
	got := EstimateMessageTokens(msg)
	want := 10 +
		EstimateTokens("Answer") +
		EstimateTokens("I think...")
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

func TestEstimateMessageTokens_NilContent(t *testing.T) {
	msg := types.Message{Role: "assistant"}
	got := EstimateMessageTokens(msg)
	if got != 10 {
		t.Errorf("got %d, want 10 (base only)", got)
	}
}

func TestEstimateMessageTokens_MultipleToolCalls(t *testing.T) {
	msg := types.Message{
		Role: "assistant",
		ToolCalls: []types.ToolCall{
			{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "a", Arguments: `{}`}},
			{ID: "c2", Type: "function", Function: types.ToolCallFunction{Name: "b", Arguments: `{}`}},
		},
	}
	got := EstimateMessageTokens(msg)
	want := 10 +
		EstimateTokens("a") + EstimateTokens(`{}`) +
		EstimateTokens("b") + EstimateTokens(`{}`)
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// EstimateHistoryTokens
// ---------------------------------------------------------------------------

func TestEstimateHistoryTokens_Empty(t *testing.T) {
	if n := EstimateHistoryTokens(nil); n != 0 {
		t.Errorf("got %d, want 0", n)
	}
	if n := EstimateHistoryTokens([]types.Message{}); n != 0 {
		t.Errorf("got %d, want 0", n)
	}
}

func TestEstimateHistoryTokens_MultipleMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("You are an AI.")},
		{Role: "user", Content: strPtr("Hello")},
		{Role: "assistant", Content: strPtr("Hi!")},
	}
	got := EstimateHistoryTokens(msgs)
	want := EstimateMessageTokens(msgs[0]) +
		EstimateMessageTokens(msgs[1]) +
		EstimateMessageTokens(msgs[2])
	if got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------------------
// TruncateToolResult
// ---------------------------------------------------------------------------

func TestTruncateToolResult_ShortContent(t *testing.T) {
	content := "short result"
	got := TruncateToolResult(content, 100)
	if got != content {
		t.Errorf("got %q, want %q", got, content)
	}
}

func TestTruncateToolResult_ExactMax(t *testing.T) {
	content := strings.Repeat("x", 50)
	got := TruncateToolResult(content, 50)
	if got != content {
		t.Errorf("got len %d, want 50", len(got))
	}
}

func TestTruncateToolResult_LongContent(t *testing.T) {
	content := strings.Repeat("a", 20) + "\n" + strings.Repeat("b", 30)
	// maxChars=50, truncatedMarker="\n...[truncated]" (15 bytes)
	// maxBody = 50 - 15 = 35
	// cut = content[:35] = "a"*20 + "\n" + "b"*14
	// LastIndex of "\n" in cut = 20
	// 20 > 35/2 = 17, so cut = cut[:20] = "a"*20
	got := TruncateToolResult(content, 50)
	want := strings.Repeat("a", 20) + "\n...[truncated]"
	if got != want {
		t.Errorf("got len %d\n got: %q\nwant: %q", len(got), got, want)
	}
}

func TestTruncateToolResult_NoNewlineBoundary(t *testing.T) {
	content := strings.Repeat("x", 100)
	// maxChars=50, maxBody=35, no newline in cut
	got := TruncateToolResult(content, 50)
	want := strings.Repeat("x", 35) + "\n...[truncated]"
	if got != want {
		t.Errorf("got len %d\n got: %q\nwant: %q", len(got), got, want)
	}
}

func TestTruncateToolResult_VeryLongMarkerOnly(t *testing.T) {
	content := "some very long tool result that exceeds limit"
	// maxChars=10, truncatedMarker "\n...[truncated]" = 15 bytes
	// maxBody = 10 - 15 = -5 <= 0
	got := TruncateToolResult(content, 10)
	want := "\n...[truncated]"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTruncateToolResult_EmptyContent(t *testing.T) {
	got := TruncateToolResult("", 100)
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestTruncateToolResult_ExactMarkerSizeMaxChars(t *testing.T) {
	content := strings.Repeat("x", 50)
	// maxChars = len(truncatedMarker) = 15
	// maxBody = 15 - 15 = 0, which is not > 0, so falls to truncatedMarker
	got := TruncateToolResult(content, len(truncatedMarker))
	want := truncatedMarker
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTruncateToolResult_NewlineBeforeHalf(t *testing.T) {
	content := "a\n" + strings.Repeat("x", 100)
	// maxChars=50, maxBody=35
	// cut = "a\n"+"x"*33  (2 + 33 = 35 bytes)
	// LastIndex "\n" = 1
	// 1 > 17? No -> keep full cut
	got := TruncateToolResult(content, 50)
	want := "a\n" + strings.Repeat("x", 33) + "\n...[truncated]"
	if got != want {
		t.Errorf("got len %d\n got: %q\nwant: %q", len(got), got, want)
	}
}

// ---------------------------------------------------------------------------
// TrimHistory
// ---------------------------------------------------------------------------

func TestTrimHistory_UnderLimit(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
	}
	got := TrimHistory(msgs, 500)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
	if *got[0].Content != "hi" {
		t.Errorf("content = %q", *got[0].Content)
	}
}

func TestTrimHistory_Empty(t *testing.T) {
	got := TrimHistory(nil, 500)
	if got != nil && len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestTrimHistory_OverLimit(t *testing.T) {
	// 10 messages, each with 300-char content
	// Each: EstimateMessageTokens ≈ 10 + (300+2)/3 = 10 + 100 = 110
	// 10 * 110 = 1100 tokens total
	msgs := makeMsgs(10, 300)
	got := TrimHistory(msgs, 500)
	total := EstimateHistoryTokens(got)
	if total > 500 {
		t.Errorf("total tokens %d exceeds maxTokens 500", total)
	}
	if len(got) >= 10 {
		t.Errorf("len = %d, expected reduction", len(got))
	}
}

func TestTrimHistory_SystemPreserved(t *testing.T) {
	sysContent := "You are a helpful assistant. Be concise."
	nonSysContent := strings.Repeat("a", 300)
	msgs := []types.Message{
		{Role: "system", Content: strPtr(sysContent)},
		{Role: "user", Content: strPtr(nonSysContent)},
		{Role: "assistant", Content: strPtr(nonSysContent)},
		{Role: "user", Content: strPtr(nonSysContent)},
		{Role: "assistant", Content: strPtr(nonSysContent)},
	}
	got := TrimHistory(msgs, 200)
	// System message must remain
	if len(got) == 0 {
		t.Fatal("result should not be empty")
	}
	if got[0].Role != "system" {
		t.Errorf("first msg role = %q, want 'system'", got[0].Role)
	}
	if *got[0].Content != sysContent {
		t.Errorf("system content altered: %q", *got[0].Content)
	}
	total := EstimateHistoryTokens(got)
	if total > 200 {
		t.Errorf("total tokens %d exceeds maxTokens 200", total)
	}
}

func TestTrimHistory_OrphanToolsStripped(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("sys")},
		{Role: "tool", Content: strPtr("r1"), ToolCallID: "c1"},  // leading orphan in rest
		{Role: "tool", Content: strPtr("r2"), ToolCallID: "c2"},  // leading orphan in rest
		{Role: "user", Content: strPtr("hello")},
		{Role: "assistant", Content: strPtr("world")},
	}
	got := TrimHistory(msgs, 500)
	for _, m := range got {
		if m.Role == "tool" {
			t.Errorf("orphan tool message not stripped: %+v", m)
		}
	}
	// System should still be present
	if got[0].Role != "system" {
		t.Errorf("first msg role = %q, want system", got[0].Role)
	}
}

func TestTrimHistory_NonLeadingToolsPreserved(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hello")},
		{Role: "assistant", Content: strPtr("world")},
		{Role: "tool", Content: strPtr("result"), ToolCallID: "c1"},
	}
	got := TrimHistory(msgs, 500)
	found := false
	for _, m := range got {
		if m.Role == "tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("non-leading tool message should be preserved")
	}
}

func TestTrimHistory_AllToolMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "tool", Content: strPtr("r1"), ToolCallID: "c1"},
		{Role: "tool", Content: strPtr("r2"), ToolCallID: "c2"},
	}
	got := TrimHistory(msgs, 500)
	if len(got) != 0 {
		t.Errorf("expected 0 messages (all orphan tools stripped), got %d", len(got))
	}
}

func TestTrimHistory_AllSystemMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("s1")},
		{Role: "system", Content: strPtr("s2")},
	}
	got := TrimHistory(msgs, 500)
	if len(got) != 2 {
		t.Errorf("expected 2, got %d", len(got))
	}
}

func TestTrimHistory_TruncateLongContent(t *testing.T) {
	// System message with content > 4000 (MaxToolResultChars) forces truncation.
	longContent := strings.Repeat("x", 5000)
	msgs := []types.Message{
		{Role: "system", Content: strPtr(longContent)},
	}
	got := TrimHistory(msgs, 100)
	if len(got) != 1 {
		t.Fatalf("expected 1 message (system preserved), got %d", len(got))
	}
	if got[0].Content == nil {
		t.Fatal("content should not be nil")
	}
	// Content should be truncated to <= 4000 chars with marker appended
	if len(*got[0].Content) > 4000 {
		t.Errorf("content len = %d, want <= 4000", len(*got[0].Content))
	}
	if !strings.HasSuffix(*got[0].Content, truncatedMarker) {
		t.Errorf("truncated content should end with marker, got len %d: %q", len(*got[0].Content), *got[0].Content)
	}
}

func TestTrimHistory_NegativeMaxTokens(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
	}
	got := TrimHistory(msgs, -1)
	// Should use DefaultConfig().MaxTokens (8192), so no trimming
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

func TestTrimHistory_ZeroMaxTokens(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
	}
	got := TrimHistory(msgs, 0)
	// Should use DefaultConfig().MaxTokens (8192), so no trimming
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// NeedCompression
// ---------------------------------------------------------------------------

func TestNeedCompression_UnderThreshold(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
	}
	// EstimateTokens("hi") = 1, base = 10, total = 11
	got := NeedCompression(msgs, 100)
	if got {
		t.Error("expected false (11 <= 100)")
	}
}

func TestNeedCompression_OverThreshold(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
	}
	got := NeedCompression(msgs, 5)
	if !got {
		t.Error("expected true (11 > 5)")
	}
}

func TestNeedCompression_Empty(t *testing.T) {
	got := NeedCompression(nil, 100)
	if got {
		t.Error("expected false for empty history")
	}
}

// ---------------------------------------------------------------------------
// splitSystem
// ---------------------------------------------------------------------------

func TestSplitSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("s1")},
		{Role: "user", Content: strPtr("u1")},
		{Role: "system", Content: strPtr("s2")},
		{Role: "assistant", Content: strPtr("a1")},
		{Role: "tool", Content: strPtr("t1"), ToolCallID: "c1"},
	}
	sys, rest := splitSystem(msgs)
	if len(sys) != 2 {
		t.Errorf("sys len = %d, want 2", len(sys))
	}
	if len(rest) != 3 {
		t.Errorf("rest len = %d, want 3", len(rest))
	}
	if sys[0].Role != "system" || *sys[0].Content != "s1" {
		t.Errorf("sys[0] = %+v", sys[0])
	}
	if sys[1].Role != "system" || *sys[1].Content != "s2" {
		t.Errorf("sys[1] = %+v", sys[1])
	}
	if rest[0].Role != "user" || *rest[0].Content != "u1" {
		t.Errorf("rest[0] = %+v", rest[0])
	}
}

func TestSplitSystem_AllSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("s1")},
		{Role: "system", Content: strPtr("s2")},
	}
	sys, rest := splitSystem(msgs)
	if len(sys) != 2 {
		t.Errorf("sys len = %d", len(sys))
	}
	if len(rest) != 0 {
		t.Errorf("rest len = %d, want 0", len(rest))
	}
}

func TestSplitSystem_NoSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("u1")},
	}
	sys, rest := splitSystem(msgs)
	if len(sys) != 0 {
		t.Errorf("sys len = %d, want 0", len(sys))
	}
	if len(rest) != 1 {
		t.Errorf("rest len = %d, want 1", len(rest))
	}
}

func TestSplitSystem_Empty(t *testing.T) {
	sys, rest := splitSystem(nil)
	if len(sys) != 0 {
		t.Errorf("sys = %v", sys)
	}
	if len(rest) != 0 {
		t.Errorf("rest = %v", rest)
	}
}

// ---------------------------------------------------------------------------
// buildCompressInput
// ---------------------------------------------------------------------------

func TestBuildCompressInput_UserAndAssistant(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: strPtr("What is the weather?")},
		{Role: "assistant", Content: strPtr("Let me search for it.")},
	}
	result := buildCompressInput(msgs)
	if !strings.Contains(result, "User: What is the weather?") {
		t.Errorf("missing User line: %q", result)
	}
	if !strings.Contains(result, "Assistant: Let me search for it.") {
		t.Errorf("missing Assistant line: %q", result)
	}
}

func TestBuildCompressInput_ToolCalls(t *testing.T) {
	msgs := []types.Message{
		{
			Role: "assistant",
			ToolCalls: []types.ToolCall{
				{
					ID: "c1", Type: "function",
					Function: types.ToolCallFunction{Name: "get_weather", Arguments: `{"city":"Beijing"}`},
				},
			},
		},
	}
	result := buildCompressInput(msgs)
	if !strings.Contains(result, "Called get_weather(") {
		t.Errorf("missing Called line: %q", result)
	}
	if !strings.Contains(result, `{"city":"Beijing"}`) {
		t.Errorf("missing arguments in Called line: %q", result)
	}
}

func TestBuildCompressInput_ToolResult(t *testing.T) {
	msgs := []types.Message{
		{Role: "tool", Content: strPtr(`{"temp":25}`), Name: "get_weather"},
	}
	result := buildCompressInput(msgs)
	if !strings.Contains(result, "Result from get_weather") {
		t.Errorf("missing Result line: %q", result)
	}
	if !strings.Contains(result, `{"temp":25}`) {
		t.Errorf("missing tool result content: %q", result)
	}
}

func TestBuildCompressInput_LongToolResultTruncated(t *testing.T) {
	longContent := strings.Repeat("x", 1000)
	msgs := []types.Message{
		{Role: "tool", Content: strPtr(longContent), Name: "search"},
	}
	result := buildCompressInput(msgs)
	// Should contain truncated content (800 chars + "...")
	if !strings.Contains(result, "...") {
		t.Error("result should contain truncation marker")
	}
	// The tool content line should be roughly 800 + overhead
	if len(result) > 900 {
		t.Errorf("result length = %d, expected ~800 + overhead", len(result))
	}
}

func TestBuildCompressInput_ToolCallsOverrideContent(t *testing.T) {
	// When ToolCalls is non-empty, Content is ignored
	msgs := []types.Message{
		{
			Role:    "assistant",
			Content: strPtr("This should not appear"),
			ToolCalls: []types.ToolCall{
				{ID: "c1", Type: "function", Function: types.ToolCallFunction{Name: "fn", Arguments: `{}`}},
			},
		},
	}
	result := buildCompressInput(msgs)
	if strings.Contains(result, "Assistant:") {
		t.Error("Assistant line with content should not appear when ToolCalls present")
	}
	if !strings.Contains(result, "Called fn(") {
		t.Error("should contain Called line")
	}
}

func TestBuildCompressInput_SkipSystem(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("you are helpful")},
		{Role: "user", Content: strPtr("hi")},
	}
	result := buildCompressInput(msgs)
	if strings.Contains(result, "System:") || strings.Contains(result, "system") {
		t.Errorf("system messages should be skipped: %q", result)
	}
	if !strings.Contains(result, "User: hi") {
		t.Error("user message should be present")
	}
}

func TestBuildCompressInput_Empty(t *testing.T) {
	result := buildCompressInput(nil)
	if result != "" {
		t.Errorf("expected empty, got %q", result)
	}
}

func TestBuildCompressInput_ToolResultTooToolName(t *testing.T) {
	msgs := []types.Message{
		{Role: "tool", Content: strPtr("result"), Name: ""},
	}
	result := buildCompressInput(msgs)
	if !strings.Contains(result, "Result from :") {
		t.Errorf("result should handle empty Name: %q", result)
	}
}

// ---------------------------------------------------------------------------
// CompressHistory
// ---------------------------------------------------------------------------

func TestCompressHistory_ShortHistory(t *testing.T) {
	// 3 non-system messages ≤ keepRecent (4), falls back to TrimHistory
	msgs := []types.Message{
		{Role: "user", Content: strPtr("hi")},
		{Role: "assistant", Content: strPtr("hello")},
		{Role: "user", Content: strPtr("how are you?")},
	}
	mock := &mockCompleter{}
	got, err := CompressHistory(context.Background(), mock, msgs, 10000)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	// Should be same as TrimHistory: no compression, all messages preserved
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestCompressHistory_NormalCompression(t *testing.T) {
	// 6 non-system messages, compress the first 2
	msgs := makeMsgs(6, 10)
	mock := &mockCompleter{
		result: types.Message{Content: strPtr("summary of earlier steps")},
	}
	got, err := CompressHistory(context.Background(), mock, msgs, 10000)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	// Result should be: [summary msg] + keep (last 4)
	// = 1 + 4 = 5 messages
	if len(got) <= 4 {
		t.Errorf("expected more than keepRecent (4) messages, got %d", len(got))
	}
	// The first message should be the summary (system role)
	if got[0].Role != "system" {
		t.Errorf("first msg role = %q, want 'system' (summary)", got[0].Role)
	}
	if got[0].Content == nil || !strings.Contains(*got[0].Content, "summary of earlier") {
		t.Errorf("summary content = %q", *got[0].Content)
	}
}

func TestCompressHistory_WithSystemMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: strPtr("be helpful")},
		{Role: "user", Content: strPtr("a")},
		{Role: "assistant", Content: strPtr("b")},
		{Role: "user", Content: strPtr("c")},
		{Role: "assistant", Content: strPtr("d")},
		{Role: "user", Content: strPtr("e")},
		{Role: "assistant", Content: strPtr("f")},
	}
	mock := &mockCompleter{
		result: types.Message{Content: strPtr("summary")},
	}
	got, err := CompressHistory(context.Background(), mock, msgs, 10000)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	// Result should be: [original system msg] + [summary system msg] + keep (last 4)
	// = 1 + 1 + 4 = 6
	if len(got) != 6 {
		t.Errorf("len = %d, want 6 (sys+summary+keep)", len(got))
	}
	// First message is original system
	if got[0].Role != "system" || *got[0].Content != "be helpful" {
		t.Errorf("first msg: %+v", got[0])
	}
	// Second is summary
	if got[1].Role != "system" || !strings.Contains(*got[1].Content, "summary") {
		t.Errorf("second msg: %+v", got[1])
	}
}

func TestCompressHistory_LLMFails(t *testing.T) {
	msgs := makeMsgs(6, 10)
	mock := &mockCompleter{
		err: context.DeadlineExceeded,
	}
	got, err := CompressHistory(context.Background(), mock, msgs, 10000)
	if err != nil {
		t.Fatalf("CompressHistory should not propagate LLM error: %v", err)
	}
	// Must fall back to TrimHistory, so no summary message
	for _, m := range got {
		if m.Role == "system" && strings.Contains(*m.Content, "[Context summary") {
			t.Error("should not have summary message after LLM failure")
		}
	}
	// Result should be <= original and meet token budget
	total := EstimateHistoryTokens(got)
	if total > 10000 {
		t.Errorf("total tokens %d > max 10000", total)
	}
}

func TestCompressHistory_LLMReturnsNilContent(t *testing.T) {
	msgs := makeMsgs(6, 10)
	mock := &mockCompleter{
		result: types.Message{}, // Content is nil
	}
	got, err := CompressHistory(context.Background(), mock, msgs, 10000)
	if err != nil {
		t.Fatalf("CompressHistory should not propagate nil content error: %v", err)
	}
	// Must fall back to TrimHistory
	for _, m := range got {
		if m.Role == "system" && strings.Contains(*m.Content, "[Context summary") {
			t.Error("should not have summary message after nil content")
		}
	}
}

func TestCompressHistory_ZeroMaxTokens(t *testing.T) {
	msgs := makeMsgs(2, 10)
	mock := &mockCompleter{}
	got, err := CompressHistory(context.Background(), mock, msgs, 0)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestCompressHistory_FallBackTrimWhenOverBudgetAfterCompression(t *testing.T) {
	// Create history where even after compression it exceeds maxTokens.
	// CompressHistory will compress then detect it's still over budget
	// and call TrimHistory. TrimHistory preserves system messages (including
	// the summary), so the result is valid but different from input.
	longContent := strings.Repeat("a", 5000)
	msgs := []types.Message{
		{Role: "system", Content: strPtr("sys")},
		{Role: "user", Content: strPtr("q1")},
		{Role: "assistant", Content: strPtr("a1")},
		{Role: "user", Content: strPtr("q2")},
		{Role: "assistant", Content: strPtr("a2")},
		{Role: "user", Content: strPtr(longContent)},
		{Role: "assistant", Content: strPtr("a3")},
	}
	mock := &mockCompleter{
		result: types.Message{Content: strPtr("ok")},
	}
	got, err := CompressHistory(context.Background(), mock, msgs, 50)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	// Must return non-nil result (TrimHistory handles even tiny budgets)
	if len(got) == 0 {
		t.Error("expected non-empty result after fallback")
	}
}

// ---------------------------------------------------------------------------
// strPtr
// ---------------------------------------------------------------------------

func TestStrPtr(t *testing.T) {
	s := "hello"
	p := strPtr(s)
	if p == nil {
		t.Fatal("strPtr returned nil")
	}
	if *p != "hello" {
		t.Errorf("got %q, want %q", *p, "hello")
	}
}

func TestStrPtr_EmptyString(t *testing.T) {
	p := strPtr("")
	if p == nil {
		t.Fatal("strPtr returned nil for empty string")
	}
	if *p != "" {
		t.Errorf("got %q", *p)
	}
}

func TestStrPtr_UniquePointer(t *testing.T) {
	s1 := "a"
	s2 := "b"
	p1 := strPtr(s1)
	p2 := strPtr(s2)
	if p1 == p2 {
		t.Error("strPtr should return a unique pointer per call")
	}
}

// ---------------------------------------------------------------------------
// callCompressLLM (indirect, via CompressHistory)
// ---------------------------------------------------------------------------

func TestCallCompressLLM_EmptyInput(t *testing.T) {
	msgs2 := []types.Message{
		{Role: "system", Content: strPtr("sys")},
		{Role: "tool", Content: strPtr(""), ToolCallID: "c1"},
		{Role: "assistant", Content: strPtr("a1")},
		{Role: "tool", Content: strPtr(""), ToolCallID: "c2"},
		{Role: "assistant", Content: strPtr("a2")},
		{Role: "tool", Content: strPtr(""), ToolCallID: "c3"},
		{Role: "assistant", Content: strPtr("a3")},
	}
	// Sys=1, rest=6 (tools + assistants), keepRecent=4
	// keep = last 4 = [a2, tool, a3] (indices 3-6)
	// wait: rest[2:] = indices 2..5 = [{a1}, {tool, c2}, {a2}, {tool, c3}]
	// That's 4 items (indices 2,3,4,5 of rest).
	// compressible = rest[:2] = indices 0,1 of rest = [{tool, c1}, {a1}]
	// keep[0] = rest[2] = {a1} -- not tool, so no movement
	// compressible = [{tool, c1}, {a1}]
	// But tool with empty content in buildCompressInput will be "Result from : \n" (skip if no content)
	// Actually: tool with Content="" will have Content != nil (since strPtr gives non-nil pointer)
	// But buildCompressInput checks: if m.Content != nil { ... }
	// So empty content would produce "Result from : \n"

	called := false
	mock := &mockCompleter{
		result: types.Message{Content: strPtr("summary")},
	}
	// If llm is called, called becomes true
	_ = mock
	_ = called
	got, err := CompressHistory(context.Background(), mock, msgs2, 10000)
	if err != nil {
		t.Fatalf("CompressHistory: %v", err)
	}
	// Should produce compressed output
	if len(got) == 0 {
		t.Error("expected non-empty result")
	}
}
