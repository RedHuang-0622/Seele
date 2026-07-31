package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/RedHuang-0622/Seele/seelectx"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/types"
)

type recordingRuntime struct {
	result string
	err    error
}

func (r recordingRuntime) VisibleTools(context.Context) []types.Tool { return nil }
func (r recordingRuntime) Dispatch(context.Context, string, string) (string, error) {
	return r.result, r.err
}

type recordingLLM struct {
	responses []types.Message
	seen      [][]types.Message
}

func (l *recordingLLM) Complete(_ context.Context, messages []types.Message, _ []types.Tool) (types.Message, error) {
	l.seen = append(l.seen, append([]types.Message(nil), messages...))
	if len(l.responses) == 0 {
		return types.Message{}, errors.New("no response")
	}
	response := l.responses[0]
	l.responses = l.responses[1:]
	return response, nil
}

func (l *recordingLLM) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
	message, err := l.Complete(ctx, messages, tools)
	if err != nil {
		return "", "", nil, err
	}
	content := ""
	if message.Content != nil {
		content = *message.Content
		if onChunk != nil {
			onChunk(content)
		}
	}
	return content, message.ReasoningContent, message.ToolCalls, nil
}

func (l *recordingLLM) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, _ func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	return l.CompleteStream(ctx, messages, tools, nil)
}

func engineMessage(role, content string) types.Message {
	return types.Message{Role: role, Content: &content}
}

func TestReActLoopSeparatesWorkingAndDurableHistory(t *testing.T) {
	durable := seelectx.NewMemoryHistory(engineMessage("assistant", "checkpoint"))
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	loop := NewReActLoop(recordingRuntime{}, llm, WithHistoryOwner(durable))

	if got := loop.History(); len(got) != 0 {
		t.Fatalf("working history before Run = %#v", got)
	}
	if _, err := loop.Run(context.Background(), "continue", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(llm.seen) != 1 || len(llm.seen[0]) != 2 {
		t.Fatalf("model input = %#v", llm.seen)
	}
	saved, err := durable.Load(context.Background())
	if err != nil {
		t.Fatalf("durable.Load() error = %v", err)
	}
	if !reflect.DeepEqual(saved, loop.History()) || len(saved) != 3 {
		t.Fatalf("durable = %#v, working = %#v", saved, loop.History())
	}
}

func TestReActLoopUsesRequestAssemblerBlocks(t *testing.T) {
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	block := seelectx.PromptBlock{Name: "skill", Messages: []types.Message{engineMessage("system", "skill prompt")}}
	loop := NewReActLoop(recordingRuntime{}, llm, WithPromptBlocks(block))
	if _, err := loop.Run(context.Background(), "work", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(llm.seen) != 1 || len(llm.seen[0]) != 2 {
		t.Fatalf("assembled messages = %#v", llm.seen)
	}
	if llm.seen[0][0].Role != "system" || *llm.seen[0][0].Content != "skill prompt" {
		t.Fatalf("first assembled message = %#v", llm.seen[0][0])
	}
	if got := loop.History(); len(got) != 2 {
		t.Fatalf("working history contains prompt block: %#v", got)
	}
}

func TestReActLoopToolResultProcessorControlsModelView(t *testing.T) {
	call := types.ToolCall{ID: "call-1", Type: "function", Function: types.ToolCallFunction{Name: "inspect", Arguments: `{}`}}
	llm := &recordingLLM{responses: []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		engineMessage("assistant", "done"),
	}}
	processor := seelectx.ToolResultProcessorFunc(func(_ context.Context, result seelectx.ToolResult) (seelectx.ToolResultView, error) {
		if result.Raw != "sensitive raw" {
			t.Fatalf("processor raw = %q", result.Raw)
		}
		return seelectx.ToolResultView{Content: "result-ref:123"}, nil
	})
	loop := NewReActLoop(recordingRuntime{result: "sensitive raw"}, llm, WithToolResultProcessor(processor))
	if _, err := loop.Run(context.Background(), "work", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(llm.seen) != 2 {
		t.Fatalf("model calls = %d", len(llm.seen))
	}
	last := llm.seen[1][len(llm.seen[1])-1]
	if last.Role != "tool" || last.Content == nil || *last.Content != "result-ref:123" {
		t.Fatalf("processed tool message = %#v", last)
	}
}

func TestReActLoopContextControllerIsExplicit(t *testing.T) {
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	var events []seelectx.ContextEventKind
	controller := seelectx.ContextControllerFunc(func(_ context.Context, event seelectx.ContextEvent) (seelectx.ContextDecision, error) {
		events = append(events, event.Kind)
		if event.Query != "work" {
			t.Fatalf("event = %#v", event)
		}
		if event.Kind != seelectx.ContextBeforeModel {
			return seelectx.ContextDecision{}, nil
		}
		return seelectx.ContextDecision{
			ReplaceHistory: true,
			History:        []types.Message{engineMessage("system", "policy checkpoint")},
		}, nil
	})
	loop := NewReActLoop(recordingRuntime{}, llm, WithContextController(controller))
	if _, err := loop.Run(context.Background(), "work", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := []seelectx.ContextEventKind{seelectx.ContextBeforeModel, seelectx.ContextAfterAssistant}; !reflect.DeepEqual(events, want) {
		t.Fatalf("controller events = %#v, want %#v", events, want)
	}
	if len(llm.seen) != 1 || len(llm.seen[0]) != 1 {
		t.Fatalf("model input = %#v", llm.seen)
	}
	if got := *llm.seen[0][0].Content; got != "policy checkpoint" {
		t.Fatalf("model context = %q", got)
	}
}

func TestReActLoopAllowsNoToolRuntimeForPlainChat(t *testing.T) {
	llm := &recordingLLM{responses: []types.Message{engineMessage("assistant", "done")}}
	loop := NewReActLoop(nil, llm)
	got, err := loop.Run(context.Background(), "hello", nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("Run() = %q, want done", got)
	}
}

func TestReActLoopReportsMissingToolRuntime(t *testing.T) {
	call := types.ToolCall{ID: "call-1", Type: "function", Function: types.ToolCallFunction{Name: "inspect", Arguments: `{}`}}
	llm := &recordingLLM{responses: []types.Message{{Role: "assistant", ToolCalls: []types.ToolCall{call}}}}
	loop := NewReActLoop(nil, llm)
	_, err := loop.Run(context.Background(), "hello", nil)
	if err == nil || err.Error() != `session: model requested tool "inspect" but no tool runtime is configured` {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestReActLoopEmitsCorrelatedTelemetry(t *testing.T) {
	call := types.ToolCall{ID: "call-1", Type: "function", Function: types.ToolCallFunction{Name: "inspect", Arguments: `{}`}}
	llm := &recordingLLM{responses: []types.Message{
		{Role: "assistant", ToolCalls: []types.ToolCall{call}},
		engineMessage("assistant", "done"),
	}}
	tracer := telemetry.NewMemoryTracer()
	hook, err := telemetry.NewLifecycleHook(tracer, telemetry.WithStrictHookErrors())
	if err != nil {
		t.Fatalf("NewLifecycleHook() error = %v", err)
	}
	loop := NewReActLoop(recordingRuntime{result: "ok"}, llm, WithReActTelemetryHook(hook))
	if _, err := loop.Run(context.Background(), "work", nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	view, err := tracer.Query(context.Background(), telemetry.Query{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	counts := map[telemetry.EventType]int{}
	for _, event := range view.Events {
		counts[event.Type]++
	}
	for _, eventType := range []telemetry.EventType{
		telemetry.EventAgentStart, telemetry.EventAgentEnd,
		telemetry.EventLLMBefore, telemetry.EventLLMAfter,
		telemetry.EventToolBefore, telemetry.EventToolAfter,
	} {
		if counts[eventType] == 0 {
			t.Fatalf("missing telemetry event %q in %#v", eventType, counts)
		}
	}
	if len(view.Traces) != 1 || len(view.Traces[0].Operations) != 4 {
		t.Fatalf("trace operations = %#v", view.Traces)
	}
}
