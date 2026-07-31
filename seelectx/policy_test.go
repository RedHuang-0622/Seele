package seelectx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/seelectx/ctx_manager"
	"github.com/RedHuang-0622/Seele/types"
)

func TestStructuralSegmenterKeepsToolExchangeTogether(t *testing.T) {
	call := types.ToolCall{ID: "1", Function: types.ToolCallFunction{Name: "read", Arguments: `{}`}}
	history := []types.Message{
		message("user", "inspect"),
		{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		{Role: "tool", Name: "read", Content: stringPointer("one")},
		{Role: "tool", Name: "read", Content: stringPointer("two")},
		message("assistant", "done"),
	}
	segments := (StructuralSegmenter{MaxChars: 2}).Segment(history)
	var toolSegments []ContextSegment
	for _, segment := range segments {
		if segment.Kind == SegmentToolExchange {
			toolSegments = append(toolSegments, segment)
		}
	}
	if len(toolSegments) != 1 || len(toolSegments[0].Messages) != 3 {
		t.Fatalf("tool segments = %#v", toolSegments)
	}
}

func TestFlattenToolExchanges(t *testing.T) {
	call := types.ToolCall{ID: "1", Function: types.ToolCallFunction{Name: "read", Arguments: `{"path":"a"}`}}
	normalized := (FlattenToolExchanges{}).Normalize([]types.Message{
		message("user", "inspect"),
		{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		{Role: "tool", Name: "read", Content: stringPointer("contents")},
	})
	if len(normalized) != 2 || normalized[1].Content == nil {
		t.Fatalf("normalized = %#v", normalized)
	}
	if !strings.Contains(*normalized[1].Content, "tool_use read") || !strings.Contains(*normalized[1].Content, "tool_result read: contents") {
		t.Fatalf("flattened content = %q", *normalized[1].Content)
	}
}

func TestRelevanceSelectorSortsAndDrops(t *testing.T) {
	segments := []ContextSegment{
		{Kind: SegmentTurn, Messages: []types.Message{message("user", "database migration")}},
		{Kind: SegmentTurn, Messages: []types.Message{message("user", "frontend colors")}},
		{Kind: SegmentSystem, Messages: []types.Message{message("system", "rules")}},
	}
	selected := (RelevanceSelector{MinScore: 0.5, MaxSegments: 2}).Select("database migration", segments)
	if len(selected) != 2 || selected[0].Score < selected[1].Score {
		t.Fatalf("selected = %#v", selected)
	}
	for _, segment := range selected {
		if segment.Messages[0].Content != nil && *segment.Messages[0].Content == "frontend colors" {
			t.Fatal("irrelevant segment was not dropped")
		}
	}
}

func TestPlaceholderRequestAssemblerResolvesDynamically(t *testing.T) {
	assembler := PlaceholderRequestAssembler{
		Resolver: PlaceholderResolverFunc(func(_ context.Context, name string) (string, error) {
			if name != "plan" {
				return "", errors.New("unknown")
			}
			return "inspect -> test", nil
		}),
	}
	assembled, err := assembler.Assemble(context.Background(), AssemblyRequest{Blocks: []PromptBlock{
		{Name: "plan", Messages: []types.Message{message("system", "Plan: {{plan}}")}},
	}})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if got := *assembled.Messages[0].Content; got != "Plan: inspect -> test" {
		t.Fatalf("content = %q", got)
	}
}

type sequenceQuickChat struct {
	responses []string
	calls     int
}

func (s *sequenceQuickChat) Complete(context.Context, QuickChatRequest) (types.Message, error) {
	s.calls++
	if len(s.responses) == 0 {
		return types.Message{}, errors.New("no response")
	}
	content := s.responses[0]
	s.responses = s.responses[1:]
	return types.Message{Role: "assistant", Content: &content}, nil
}

func TestRecursiveCompressorSkipsLLMForShortConversation(t *testing.T) {
	chat := &sequenceQuickChat{responses: []string{"must not run"}}
	long := strings.Repeat("x", 900)
	compressor := RecursiveCompressor{Chat: chat, MinMessages: 6, MinTokens: 10}
	result, err := compressor.Compress(context.Background(), CompressionRequest{
		History:   []types.Message{message("user", long), message("assistant", long)},
		MaxTokens: 20,
	})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if chat.calls != 0 {
		t.Fatalf("QuickChat calls = %d, want 0", chat.calls)
	}
	if ctx_manager.EstimateHistoryTokens(result.Messages) > 20 {
		t.Fatalf("short deterministic result remains over budget")
	}
}

func TestRecursiveCompressorRepeatsUntilBudget(t *testing.T) {
	chat := &sequenceQuickChat{responses: []string{strings.Repeat("a", 300), "compact checkpoint"}}
	var history []types.Message
	for i := 0; i < 6; i++ {
		history = append(history, message("user", strings.Repeat("x", 200)))
	}
	compressor := RecursiveCompressor{Chat: chat, MinMessages: 6, MinTokens: 10, MaxDepth: 3}
	result, err := compressor.Compress(context.Background(), CompressionRequest{History: history, MaxTokens: 30})
	if err != nil {
		t.Fatalf("Compress() error = %v", err)
	}
	if chat.calls != 2 || result.Depth != 2 {
		t.Fatalf("calls = %d, depth = %d", chat.calls, result.Depth)
	}
	if ctx_manager.EstimateHistoryTokens(result.Messages) > 30 {
		t.Fatalf("result tokens = %d", ctx_manager.EstimateHistoryTokens(result.Messages))
	}
}

type fixedCompressor struct {
	calls int
}

func (c *fixedCompressor) Compress(_ context.Context, request CompressionRequest) (CompressionResult, error) {
	c.calls++
	content := "compressed:" + request.Query
	return CompressionResult{Messages: []types.Message{message("system", content)}}, nil
}

func TestPolicyControllerOnlyCompressesWhenPolicyRequests(t *testing.T) {
	compressor := &fixedCompressor{}
	policyCalls := 0
	controller := PolicyController{
		Policy: ContextPolicyFunc(func(_ context.Context, event ContextEvent) (ContextPolicyDecision, error) {
			policyCalls++
			return ContextPolicyDecision{Compress: event.Kind == ContextBeforeModel, MaxTokens: 100}, nil
		}),
		Compressor: compressor,
	}
	decision, err := controller.Handle(context.Background(), ContextEvent{
		Kind: ContextBeforeModel, Query: "database", History: []types.Message{message("user", "work")},
	})
	if err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if policyCalls != 1 || compressor.calls != 1 || !decision.ReplaceHistory {
		t.Fatalf("policy calls = %d, compressor calls = %d, decision = %#v", policyCalls, compressor.calls, decision)
	}
	if got := *decision.History[0].Content; got != "compressed:database" {
		t.Fatalf("history = %q", got)
	}
}

func TestObservedContextControllerReportsDecision(t *testing.T) {
	observed := false
	controller := ObservedContextController{
		Next: ContextControllerFunc(func(context.Context, ContextEvent) (ContextDecision, error) {
			return ContextDecision{ReplaceHistory: true}, nil
		}),
		Observer: ContextEventObserverFunc(func(_ context.Context, event ContextEvent, decision ContextDecision, err error) {
			observed = event.Kind == ContextAfterTool && decision.ReplaceHistory && err == nil
		}),
	}
	if _, err := controller.Handle(context.Background(), ContextEvent{Kind: ContextAfterTool}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if !observed {
		t.Fatal("observer did not receive context decision")
	}
}

func stringPointer(value string) *string { return &value }
