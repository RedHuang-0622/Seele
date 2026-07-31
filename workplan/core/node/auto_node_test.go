package node

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type autoTestFactory struct{ prompt string }

func (f *autoTestFactory) NewAgent(prompt string) Agent {
	f.prompt = prompt
	return autoTestAgent{}
}

type autoTestAgent struct{}

func (autoTestAgent) Chat(_ context.Context, input string) (string, error) { return input, nil }

func TestAutoNodeRendersTaskInputAndSupportsFactoryOverride(t *testing.T) {
	baseFactory := &autoTestFactory{}
	overrideFactory := &autoTestFactory{}
	n := NewAutoNode("inspect", baseFactory, "inspect {{.PrevResult}}")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"scope"`

	output, err := n.RunWithAgentFactory(context.Background(), wc, overrideFactory)
	if err != nil {
		t.Fatalf("RunWithAgentFactory error = %v", err)
	}
	if output != "inspect scope" {
		t.Fatalf("output = %q", output)
	}
	if overrideFactory.prompt == "" || baseFactory.prompt != "" {
		t.Fatalf("factory override was not used: base=%q override=%q", baseFactory.prompt, overrideFactory.prompt)
	}
	if n.DSLInput() != "inspect {{.PrevResult}}" || n.Kind() != KindAuto {
		t.Fatalf("node metadata was not preserved")
	}
}

func TestAutoNodeRunUsesConstructionTimeFactory(t *testing.T) {
	factory := &autoTestFactory{}
	n := NewAutoNode("inspect", factory, "plain task")

	output, err := n.Run(context.Background(), types.NewWorkflowContext())
	if err != nil {
		t.Fatalf("Run error = %v", err)
	}
	if output != "plain task" {
		t.Fatalf("output = %q, want %q", output, "plain task")
	}
	if factory.prompt != defaultAutoSystemPrompt {
		t.Fatalf("system prompt = %q, want %q", factory.prompt, defaultAutoSystemPrompt)
	}
}

// A nil factory must fail loudly rather than panic inside the scheduler.
func TestAutoNodeReportsNilFactory(t *testing.T) {
	n := NewAutoNode("inspect", nil, "task")

	_, err := n.Run(context.Background(), types.NewWorkflowContext())
	if err == nil {
		t.Fatal("Run with a nil factory must return an error")
	}
	if !strings.Contains(err.Error(), `auto node "inspect"`) || !strings.Contains(err.Error(), "factory is nil") {
		t.Fatalf("error = %v", err)
	}

	// A nil override falls back to the construction-time factory.
	if _, err := n.RunWithAgentFactory(context.Background(), types.NewWorkflowContext(), nil); err == nil {
		t.Fatal("nil override with a nil base factory must return an error")
	}
}

type nilAgentFactory struct{}

func (nilAgentFactory) NewAgent(string) Agent { return nil }

func TestAutoNodeReportsNilAgent(t *testing.T) {
	n := NewAutoNode("inspect", nilAgentFactory{}, "task")

	_, err := n.Run(context.Background(), types.NewWorkflowContext())
	if err == nil {
		t.Fatal("a factory returning a nil agent must return an error")
	}
	if !strings.Contains(err.Error(), "returned nil") {
		t.Fatalf("error = %v", err)
	}
}
