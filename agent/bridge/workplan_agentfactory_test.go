package bridge

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/session"
	"github.com/RedHuang-0622/Seele/types"
)

type fakeRuntime struct {
	llm *fakeCompleter
}

func (r *fakeRuntime) LLM() types.ChatCompleter { return r.llm }

func (r *fakeRuntime) VisibleTools(context.Context) []types.Tool { return nil }

func (r *fakeRuntime) Dispatch(context.Context, string, string) (string, error) {
	return "", nil
}

type fakeCompleter struct {
	mu        sync.Mutex
	responses []string
	messages  [][]types.Message
}

func (c *fakeCompleter) Complete(_ context.Context, messages []types.Message, _ []types.Tool) (types.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, append([]types.Message(nil), messages...))
	response := "done"
	if len(c.responses) > 0 {
		response = c.responses[0]
		c.responses = c.responses[1:]
	}
	return types.Message{Role: "assistant", Content: &response}, nil
}

func (c *fakeCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
	message, err := c.Complete(ctx, messages, tools)
	if err != nil {
		return "", "", nil, err
	}
	content := messageText(message)
	if onChunk != nil {
		onChunk(content)
	}
	return content, "", nil, nil
}

func (c *fakeCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	content, reasoning, toolCalls, err := c.CompleteStream(ctx, messages, tools, nil)
	if err == nil && onEvent != nil {
		onEvent(types.StreamEvent{Type: types.StreamEventText, Content: content})
	}
	return content, reasoning, toolCalls, err
}

var _ session.Agent = (*fakeRuntime)(nil)

func TestNewAgentFactoryRejectsMissingRuntime(t *testing.T) {
	if _, err := NewAgentFactory(nil); err == nil || !strings.Contains(err.Error(), "agent is required") {
		t.Fatalf("NewAgentFactory(nil) error = %v", err)
	}
	var runtime *fakeRuntime
	if _, err := NewAgentFactory(runtime); err == nil || !strings.Contains(err.Error(), "agent is required") {
		t.Fatalf("NewAgentFactory(typed nil) error = %v", err)
	}
	if _, err := NewAgentFactory(&fakeRuntime{}); err == nil || !strings.Contains(err.Error(), "agent LLM is required") {
		t.Fatalf("NewAgentFactory(runtime without LLM) error = %v", err)
	}
}

func TestAgentFactoryCreatesIsolatedSessionsWithNodePrompt(t *testing.T) {
	llm := &fakeCompleter{responses: []string{"first", "second"}}
	runtime := &fakeRuntime{llm: llm}
	factory, err := NewAgentFactory(runtime)
	if err != nil {
		t.Fatalf("NewAgentFactory() error = %v", err)
	}
	first := factory.NewAgent("first system")
	second := factory.NewAgent("second system")
	if first == nil || second == nil {
		t.Fatal("NewAgent returned nil")
	}
	if reflect.ValueOf(first).Pointer() == reflect.ValueOf(second).Pointer() {
		t.Fatal("NewAgent returned the same session instance")
	}
	if got, err := first.Chat(context.Background(), "one"); err != nil || got != "first" {
		t.Fatalf("first.Chat() = %q, %v", got, err)
	}
	if got, err := second.Chat(context.Background(), "two"); err != nil || got != "second" {
		t.Fatalf("second.Chat() = %q, %v", got, err)
	}
	if len(llm.messages) != 2 {
		t.Fatalf("completion count = %d, want 2", len(llm.messages))
	}
	if len(llm.messages[0]) != 2 || messageText(llm.messages[0][0]) != "first system" || messageText(llm.messages[0][1]) != "one" {
		t.Fatalf("first messages = %#v", llm.messages[0])
	}
	if len(llm.messages[1]) != 2 || messageText(llm.messages[1][0]) != "second system" || messageText(llm.messages[1][1]) != "two" {
		t.Fatalf("second messages = %#v", llm.messages[1])
	}
}

func TestAgentFactoryAppliesSessionComponentsAndSessionID(t *testing.T) {
	llm := &fakeCompleter{responses: []string{"ok"}}
	factory, err := NewAgentFactory(&fakeRuntime{llm: llm},
		WithSessionComponents(session.SessionComponents{}),
		WithSessionID(func(string) string { return "workplan-node-1" }),
	)
	if err != nil {
		t.Fatalf("NewAgentFactory() error = %v", err)
	}
	conversation := factory.NewAgent("system")
	identified, ok := conversation.(interface{ SessionID() string })
	if !ok {
		t.Fatal("NewAgent result does not expose SessionID")
	}
	if got := identified.SessionID(); got != "workplan-node-1" {
		t.Fatalf("SessionID() = %q, want workplan-node-1", got)
	}
	if got, err := conversation.Chat(context.Background(), "input"); err != nil || got != "ok" {
		t.Fatalf("Chat() = %q, %v", got, err)
	}
}

func messageText(message types.Message) string {
	if message.Content == nil {
		return ""
	}
	return *message.Content
}
