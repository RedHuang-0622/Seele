package main

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

type scriptedClient struct{}

func (scriptedClient) Complete(_ context.Context, messages []types.Message, _ []types.Tool) (types.Message, error) {
	last := messages[len(messages)-1]
	if last.Role == "tool" {
		content := "tool result accepted"
		return types.Message{Role: "assistant", Content: &content}, nil
	}
	prompt := ""
	if last.Content != nil {
		prompt = *last.Content
	}
	call := types.ToolCall{ID: "call_test", Type: "function"}
	switch {
	case strings.Contains(prompt, "operation=add"):
		call.Function = types.ToolCallFunction{Name: "calculate", Arguments: `{"operation":"add","left":19,"right":23}`}
	case strings.Contains(prompt, "timezone=UTC"):
		call.Function = types.ToolCallFunction{Name: "get_time", Arguments: `{"timezone":"UTC"}`}
	default:
		call.Function = types.ToolCallFunction{Name: "text_stats", Arguments: `{"text":"Seele smoke test"}`}
	}
	return types.Message{Role: "assistant", ToolCalls: []types.ToolCall{call}}, nil
}

type directTextClient struct{}

func (directTextClient) Complete(context.Context, []types.Message, []types.Tool) (types.Message, error) {
	content := "answered without a tool"
	return types.Message{Role: "assistant", Content: &content}, nil
}

func TestSmokeHarnessRequiresAndExecutesBuiltinTools(t *testing.T) {
	results, err := runSmokeSuite(context.Background(), scriptedClient{})
	if err != nil {
		t.Fatalf("runSmokeSuite() error = %v", err)
	}
	if len(results) != len(builtinSmokeCases()) {
		t.Fatalf("len(results) = %d", len(results))
	}
}

func TestRunSmokeCaseRejectsMissingToolCall(t *testing.T) {
	runtime, err := newSmokeRuntime(directTextClient{})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	defer runtime.Shutdown()
	_, err = runSmokeCase(context.Background(), runtime, builtinSmokeCases()[0])
	if err == nil || !strings.Contains(err.Error(), "did not call required tool") {
		t.Fatalf("error = %v", err)
	}
}

func TestResultValidatorsRejectInvalidPayloads(t *testing.T) {
	if err := validateJSONNumber("result", 42)("not-json"); err == nil {
		t.Fatal("number validator accepted invalid JSON")
	}
	if err := validateJSONNumber("result", 42)(`{"result":41}`); err == nil {
		t.Fatal("number validator accepted wrong value")
	}
	if err := validateJSONString("timezone", "UTC")(`{"timezone":1}`); err == nil {
		t.Fatal("string validator accepted wrong type")
	}
	if err := validateJSONString("timezone", "UTC")(`{"timezone":"Asia/Shanghai"}`); err == nil {
		t.Fatal("string validator accepted wrong value")
	}
}
