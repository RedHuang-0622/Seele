package node

import (
	"context"
	"errors"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// --- Mocks ---

type mockLLMProvider struct {
	chatResult string
	chatErr    error
	lastInput  string
}

func (m *mockLLMProvider) Chat(_ context.Context, input string) (string, error) {
	m.lastInput = input
	return m.chatResult, m.chatErr
}

func (m *mockLLMProvider) ChatStream(_ context.Context, input string, onChunk func(string)) (string, error) {
	m.lastInput = input
	onChunk("chunk1")
	onChunk("chunk2")
	return m.chatResult, m.chatErr
}

type mockAgent struct {
	lastInput   string
	lastSystemP string
	toolFilter  []string
	chatResult  string
	chatErr     error
}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	m.lastInput = input
	return m.chatResult, m.chatErr
}

func (m *mockAgent) ChatStream(_ context.Context, input string, onChunk func(string)) (string, error) {
	m.lastInput = input
	onChunk("agent_chunk")
	return m.chatResult, m.chatErr
}

func (m *mockAgent) SetToolFilter(filter []string) {
	m.toolFilter = filter
}

type mockAgentFactory struct {
	agent *mockAgent
}

func (f *mockAgentFactory) NewAgent(systemPrompt string) Agent {
	f.agent.lastSystemP = systemPrompt
	return f.agent
}

// --- NewBaseNode ---

func TestNewBaseNode(t *testing.T) {
	n := NewBaseNode("node1", KindMethod)
	if n.ID() != "node1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "node1")
	}
	if n.Kind() != KindMethod {
		t.Errorf("Kind() = %v, want KindMethod", n.Kind())
	}

	// Verify exported methods work on zero value
	var empty BaseNode
	if empty.ID() != "" {
		t.Errorf("zero value ID() = %q, want empty", empty.ID())
	}
	if empty.Kind() != 0 {
		t.Errorf("zero value Kind() = %d, want 0", empty.Kind())
	}
}

// --- NodeKind String ---

func TestNodeKindString(t *testing.T) {
	tests := []struct {
		kind NodeKind
		want string
	}{
		{KindMethod, "method"},
		{KindLLM, "llm"},
		{KindAgent, "agent"},
		{KindAuto, "auto"},
		{KindStrategy, "strategy"},
		{KindApprove, "approve"},
		{KindIf, "if"},
		{KindSwitch, "switch"},
		{KindLoop, "loop"},
		{KindFork, "fork"},
		{KindJoin, "join"},
		{KindCheckpoint, "checkpoint"},
		{KindEmit, "emit"},
		{NodeKind(999), "unknown"},
		{NodeKind(-1), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.kind.String()
			if got != tt.want {
				t.Errorf("NodeKind(%d).String() = %q, want %q", int(tt.kind), got, tt.want)
			}
		})
	}
}

// --- Node interface compliance ---

func testNodeInterface(t *testing.T, n Node, expectedID string, expectedKind NodeKind) {
	t.Helper()
	if n.ID() != expectedID {
		t.Errorf("ID() = %q, want %q", n.ID(), expectedID)
	}
	if n.Kind() != expectedKind {
		t.Errorf("Kind() = %v, want %v", n.Kind(), expectedKind)
	}
}

// --- NewLLMNode ---

func TestNewLLMNode(t *testing.T) {
	provider := &mockLLMProvider{chatResult: "llm response"}
	n := NewLLMNode("llm1", provider)

	testNodeInterface(t, n, "llm1", KindLLM)
	if n.provider != provider {
		t.Error("provider not set correctly")
	}
	if n.onChunk != nil {
		t.Error("onChunk should be nil initially")
	}
}

func TestLLMNodeRun(t *testing.T) {
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "hello from llm"

	provider := &mockLLMProvider{chatResult: "llm response"}
	n := NewLLMNode("llm1", provider)

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "llm response" {
		t.Errorf("Run() = %q, want %q", result, "llm response")
	}
	if provider.lastInput != "hello from llm" {
		t.Errorf("provider lastInput = %q, want %q", provider.lastInput, "hello from llm")
	}
}

func TestLLMNodeRunWithStream(t *testing.T) {
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "stream input"

	provider := &mockLLMProvider{chatResult: "stream result"}
	n := NewLLMNode("llm1", provider)

	var chunks []string
	n.WithOnChunk(func(s string) {
		chunks = append(chunks, s)
	})

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "stream result" {
		t.Errorf("Run() = %q, want %q", result, "stream result")
	}
	if len(chunks) != 2 || chunks[0] != "chunk1" || chunks[1] != "chunk2" {
		t.Errorf("chunks = %v, want [chunk1 chunk2]", chunks)
	}
}

func TestLLMNodeRunError(t *testing.T) {
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	provider := &mockLLMProvider{chatResult: "", chatErr: errors.New("llm error")}
	n := NewLLMNode("llm1", provider)

	_, err := n.Run(context.Background(), wc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "llm error" {
		t.Errorf("error = %q, want %q", err.Error(), "llm error")
	}
}

func TestLLMNodeWithOnChunk(t *testing.T) {
	provider := &mockLLMProvider{chatResult: "ok"}
	n := NewLLMNode("llm1", provider)

	called := false
	cb := func(s string) { called = true }

	n2 := n.WithOnChunk(cb)
	if n2 != n {
		t.Error("WithOnChunk should return the same node (fluent builder)")
	}
	if n.onChunk == nil {
		t.Error("onChunk should be set after WithOnChunk")
	}

	// Invoke the callback to verify it works
	n.onChunk("test")
	if !called {
		t.Error("onChunk callback was never invoked")
	}
}

// --- NewFunctionNode ---

func TestNewFunctionNode(t *testing.T) {
	fn := func(_ context.Context, input string) (string, error) {
		return "fn_result", nil
	}
	n := NewFunctionNode("fn1", fn)

	testNodeInterface(t, n, "fn1", KindMethod)
}

func TestFunctionNodeRun(t *testing.T) {
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input data"

	n := NewFunctionNode("fn1", func(_ context.Context, input string) (string, error) {
		return "processed: " + input, nil
	})

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// FunctionNode.Run wraps the result with types.ToJSON
	expected := types.ToJSON("processed: input data")
	if result != expected {
		t.Errorf("Run() = %q, want %q", result, expected)
	}
}

func TestFunctionNodeRunError(t *testing.T) {
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	n := NewFunctionNode("fn_err", func(_ context.Context, _ string) (string, error) {
		return "", errors.New("function error")
	})

	_, err := n.Run(context.Background(), wc)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "function error" {
		t.Errorf("error = %q, want %q", err.Error(), "function error")
	}
}

func TestFunctionNodeRunToJSON(t *testing.T) {
	// Verify that the output is always valid JSON via types.ToJSON.
	wc := types.NewWorkflowContext()
	wc.PrevOutput = "raw"

	n := NewFunctionNode("fn_json", func(_ context.Context, input string) (string, error) {
		return `{"custom": "` + input + `"}`, nil
	})

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The function returns a JSON string, ToJSON should pass it through.
	if result != `{"custom": "raw"}` {
		t.Errorf("Run() = %q, want %q", result, `{"custom": "raw"}`)
	}
}

// --- NewAgentNode ---

func TestNewAgentNode(t *testing.T) {
	agent := &mockAgent{chatResult: "agent response"}
	factory := &mockAgentFactory{agent: agent}

	n := NewAgentNode("agent1", factory, "custom system prompt")

	testNodeInterface(t, n, "agent1", KindAgent)
	if n.factory != factory {
		t.Error("factory not set correctly")
	}
	if n.systemPrompt != "custom system prompt" {
		t.Errorf("systemPrompt = %q, want %q", n.systemPrompt, "custom system prompt")
	}
	if n.toolFilter != nil {
		t.Error("toolFilter should be nil initially")
	}
	if n.onChunk != nil {
		t.Error("onChunk should be nil initially")
	}
}

func TestAgentNodeRun(t *testing.T) {
	agent := &mockAgent{chatResult: "agent output"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("agent1", factory, "custom prompt")

	wc := types.NewWorkflowContext()
	wc.PrevOutput = "user input"

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "agent output" {
		t.Errorf("Run() = %q, want %q", result, "agent output")
	}
	if agent.lastSystemP != "custom prompt" {
		t.Errorf("agent system prompt = %q, want %q", agent.lastSystemP, "custom prompt")
	}
	if agent.lastInput != "user input" {
		t.Errorf("agent last input = %q, want %q", agent.lastInput, "user input")
	}
}

func TestAgentNodeRunDefaultPrompt(t *testing.T) {
	agent := &mockAgent{chatResult: "output"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("agent2", factory, "")

	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	_, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.lastSystemP != "You are a helpful assistant." {
		t.Errorf("default system prompt = %q, want %q", agent.lastSystemP, "You are a helpful assistant.")
	}
}

func TestAgentNodeRunWithToolFilter(t *testing.T) {
	agent := &mockAgent{chatResult: "output"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("agent3", factory, "prompt")
	n.WithToolFilter([]string{"tool_a", "tool_b"})

	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	_, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(agent.toolFilter) != 2 || agent.toolFilter[0] != "tool_a" || agent.toolFilter[1] != "tool_b" {
		t.Errorf("agent toolFilter = %v, want [tool_a tool_b]", agent.toolFilter)
	}
}

func TestAgentNodeRunEmptyToolFilter(t *testing.T) {
	// If toolFilter is empty, SetToolFilter should not be called.
	agent := &mockAgent{chatResult: "output"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("agent4", factory, "prompt")

	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	_, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.toolFilter != nil {
		t.Errorf("toolFilter should remain nil when not set, got %v", agent.toolFilter)
	}
}

func TestAgentNodeRunWithStream(t *testing.T) {
	agent := &mockAgent{chatResult: "stream output"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("agent5", factory, "prompt")

	var chunks []string
	n.WithOnChunk(func(s string) {
		chunks = append(chunks, s)
	})

	wc := types.NewWorkflowContext()
	wc.PrevOutput = "input"

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "stream output" {
		t.Errorf("Run() = %q, want %q", result, "stream output")
	}
	if len(chunks) != 1 || chunks[0] != "agent_chunk" {
		t.Errorf("chunks = %v, want [agent_chunk]", chunks)
	}
}

// --- Builder methods ---

func TestWithToolFilter(t *testing.T) {
	agent := &mockAgent{chatResult: "r"}
	factory := &mockAgentFactory{agent: agent}
	n := NewAgentNode("n", factory, "p")

	filter := []string{"tool1", "tool2"}
	n2 := n.WithToolFilter(filter)
	if n2 != n {
		t.Error("WithToolFilter should return the same node (fluent builder)")
	}
	if len(n.toolFilter) != 2 || n.toolFilter[0] != "tool1" || n.toolFilter[1] != "tool2" {
		t.Errorf("toolFilter = %v, want [tool1 tool2]", n.toolFilter)
	}

	// Overwrite with nil
	n.WithToolFilter(nil)
	if n.toolFilter != nil {
		t.Error("toolFilter should be nil after WithToolFilter(nil)")
	}
}

func TestWithOnChunk(t *testing.T) {
	t.Run("LLMNode", func(t *testing.T) {
		provider := &mockLLMProvider{chatResult: "r"}
		n := NewLLMNode("llm", provider)

		called := false
		cb := func(s string) { called = true }
		n2 := n.WithOnChunk(cb)
		if n2 != n {
			t.Error("WithOnChunk should return the same node (fluent builder)")
		}
		n.onChunk("test")
		if !called {
			t.Error("callback was not invoked")
		}
	})

	t.Run("AgentNode", func(t *testing.T) {
		agent := &mockAgent{chatResult: "r"}
		factory := &mockAgentFactory{agent: agent}
		n := NewAgentNode("agent", factory, "p")

		called := false
		cb := func(s string) { called = true }
		n2 := n.WithOnChunk(cb)
		if n2 != n {
			t.Error("WithOnChunk should return the same node (fluent builder)")
		}
		n.onChunk("test")
		if !called {
			t.Error("callback was not invoked")
		}
	})
}

// --- ForkBranch / SwitchCase ---

func TestForkBranch(t *testing.T) {
	fb := ForkBranch{
		Label:        "researcher",
		SystemPrompt: "You are a research agent",
		Input:        "Research topic: AI",
		EntryNodeID:  "sub-plan-1",
	}
	if fb.Label != "researcher" {
		t.Errorf("Label = %q, want %q", fb.Label, "researcher")
	}
	if fb.SystemPrompt != "You are a research agent" {
		t.Errorf("SystemPrompt = %q, want %q", fb.SystemPrompt, "You are a research agent")
	}
	if fb.Input != "Research topic: AI" {
		t.Errorf("Input = %q, want %q", fb.Input, "Research topic: AI")
	}
	if fb.EntryNodeID != "sub-plan-1" {
		t.Errorf("EntryNodeID = %q, want %q", fb.EntryNodeID, "sub-plan-1")
	}
}

func TestForkBranchZeroValues(t *testing.T) {
	fb := ForkBranch{}
	if fb.Label != "" {
		t.Errorf("Label should be empty, got %q", fb.Label)
	}
	if fb.SystemPrompt != "" {
		t.Errorf("SystemPrompt should be empty, got %q", fb.SystemPrompt)
	}
	if fb.Input != "" {
		t.Errorf("Input should be empty, got %q", fb.Input)
	}
	if fb.EntryNodeID != "" {
		t.Errorf("EntryNodeID should be empty, got %q", fb.EntryNodeID)
	}
}

func TestSwitchCaseMatch(t *testing.T) {
	matched := false
	sc := SwitchCase{
		Match: func(s string) bool {
			if s == "target" {
				matched = true
				return true
			}
			return false
		},
		NextID: "node_b",
	}

	if !sc.Match("target") {
		t.Error("expected Match('target') to return true")
	}
	if !matched {
		t.Error("Match function was not called")
	}

	if sc.Match("other") {
		t.Error("expected Match('other') to return false")
	}
	if sc.NextID != "node_b" {
		t.Errorf("NextID = %q, want %q", sc.NextID, "node_b")
	}
}

func TestSwitchCaseNilMatch(t *testing.T) {
	sc := SwitchCase{Match: nil, NextID: "default_node"}
	if sc.Match != nil {
		t.Error("Match should be nil for default case")
	}
	if sc.NextID != "default_node" {
		t.Errorf("NextID = %q, want %q", sc.NextID, "default_node")
	}
}

func TestSwitchCaseDefaultBehaviour(t *testing.T) {
	// A nil Match represents a default case that always matches.
	// The Switch node logic is responsible for handling nil.
	sc := SwitchCase{Match: nil, NextID: "default"}
	if sc.Match != nil {
		t.Error("expected nil Match (default case)")
	}
}

// --- Node interface (compile-time check) ---

func TestNodeInterfaceCompileCheck(t *testing.T) {
	// Ensure concrete types implement Node interface at compile time.
	var _ Node = (*LLMNode)(nil)
	var _ Node = (*FunctionNode)(nil)
	var _ Node = (*AgentNode)(nil)

	// BaseNode does NOT implement Node (no Run method), but is embedded.
}
