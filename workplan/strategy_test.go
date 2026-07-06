package workplan

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// =============================================================================
// NodeStrategy 接口测试
// =============================================================================

func TestMethodStrategy(t *testing.T) {
	strategy := NewMethodStrategy(func(ctx context.Context, input string) (string, error) {
		return "processed: " + input, nil
	})

	ec := NewExecutionContext()
	ec.PrevOutput = `"test_input"`

	out, err := strategy.Execute(context.Background(), "hello", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "processed: hello") {
		t.Errorf("expected output to contain 'processed: hello', got %q", out)
	}
}

func TestMethodStrategy_Error(t *testing.T) {
	strategy := NewMethodStrategy(func(ctx context.Context, input string) (string, error) {
		return "", fmt.Errorf("method failed")
	})

	ec := NewExecutionContext()
	_, err := strategy.Execute(context.Background(), "input", ec)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "method failed") {
		t.Errorf("expected 'method failed', got %v", err)
	}
}

func TestLLMStrategy(t *testing.T) {
	factory := &mockFactory{prompt: "llm response"}
	strategy := NewLLMStrategy(factory, "test prompt")

	ec := NewExecutionContext()
	ec.PrevOutput = `"prev"`

	out, err := strategy.Execute(context.Background(), "user input", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "llm response") {
		t.Errorf("expected output to contain 'llm response', got %q", out)
	}
}

func TestLLMStrategy_EmptyPrompt(t *testing.T) {
	factory := &mockFactory{prompt: "default prompt response"}
	strategy := NewLLMStrategy(factory, "") // empty prompt → use default

	ec := NewExecutionContext()
	out, err := strategy.Execute(context.Background(), "input", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "default prompt response") {
		t.Errorf("expected output to contain response, got %q", out)
	}
}

func TestAgentStrategy(t *testing.T) {
	factory := &mockFactory{prompt: "agent result"}
	strategy := NewAgentStrategy(factory, "agent system prompt")

	ec := NewExecutionContext()
	out, err := strategy.Execute(context.Background(), "agent task", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "agent result") {
		t.Errorf("expected 'agent result', got %q", out)
	}
}

func TestAgentStrategy_WithToolFilter(t *testing.T) {
	factory := &mockFactory{
		prompt:     "tool result",
		toolFilter: []string{"tool_a"},
	}
	strategy := NewAgentStrategy(factory, "prompt", "tool_a", "tool_b")

	ec := NewExecutionContext()
	out, err := strategy.Execute(context.Background(), "task", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "tool result") {
		t.Errorf("expected 'tool result', got %q", out)
	}
}

func TestAgentStrategy_EmptyPrompt(t *testing.T) {
	factory := &mockFactory{prompt: "default agent response"}
	strategy := NewAgentStrategy(factory, "") // empty → use default

	ec := NewExecutionContext()
	out, err := strategy.Execute(context.Background(), "input", ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "default agent response") {
		t.Errorf("expected response, got %q", out)
	}
}

// =============================================================================
// strategyRunner 测试
// =============================================================================

func TestStrategyRunner_ID(t *testing.T) {
	strategy := NewMethodStrategy(func(ctx context.Context, input string) (string, error) {
		return `"ok"`, nil
	})
	runner := &strategyRunner{id: "my-node", strategy: strategy}

	if runner.ID() != "my-node" {
		t.Errorf("expected 'my-node', got %q", runner.ID())
	}
}

func TestStrategyRunner_Run(t *testing.T) {
	strategy := NewMethodStrategy(func(ctx context.Context, input string) (string, error) {
		return `"runner_output"`, nil
	})
	runner := &strategyRunner{id: "test", strategy: strategy}

	ec := NewExecutionContext()
	out, err := runner.Run(context.Background(), ec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(out, "runner_output") {
		t.Errorf("expected 'runner_output', got %q", out)
	}
}

func TestStrategyRunner_ImplementsNodeRunner(t *testing.T) {
	// 编译期检查：strategyRunner 实现 NodeRunner
	var _ NodeRunner = (*strategyRunner)(nil)
}

// =============================================================================
// WorkPlanTool / ToTool 测试
// =============================================================================

func TestToTool(t *testing.T) {
	// 构造一个简单的 WorkPlan，包装为工具
	factory := &mockFactory{prompt: "tool output"}

	wp := New(factory, nil, "test prompt")
	wp.Auto("node1", "do something")

	tool := wp.ToTool("test_tool", "a test tool", nil)

	if tool.Name != "test_tool" {
		t.Errorf("expected 'test_tool', got %q", tool.Name)
	}
	if tool.Description != "a test tool" {
		t.Errorf("expected 'a test tool', got %q", tool.Description)
	}

	// Run 方法不在这里执行（需要实际 LLM），只验证结构正确
	if tool.Run == nil {
		t.Error("expected tool.Run to be non-nil")
	}
}

// =============================================================================
// 辅助 mock
// =============================================================================

// mockFactory 模拟 AgentFactory，直接返回固定 prompt 的 mockAgent。
type mockFactory struct {
	prompt     string
	toolFilter []string
}

func (f *mockFactory) NewAgent(systemPrompt string) Agent {
	return &mockAgent{
		systemPrompt: systemPrompt,
		prompt:       f.prompt,
		toolFilter:   f.toolFilter,
	}
}

type mockAgent struct {
	systemPrompt string
	prompt       string
	toolFilter   []string
}

func (a *mockAgent) Chat(ctx context.Context, input string) (string, error) {
	return a.prompt, nil
}
