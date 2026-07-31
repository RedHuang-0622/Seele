package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/engine"
	"github.com/RedHuang-0622/Seele/telemetry"
	"github.com/RedHuang-0622/Seele/tools"
	"github.com/RedHuang-0622/Seele/tools/builtin"
	"github.com/RedHuang-0622/Seele/types"
)

type registryRuntime struct {
	registry *tools.Registry
}

func (r registryRuntime) VisibleTools(context.Context) []types.Tool {
	return r.registry.Tools()
}

func (r registryRuntime) Dispatch(ctx context.Context, name, argumentsJSON string) (string, error) {
	return r.registry.Dispatch(ctx, tools.ToolCall{
		Name:          name,
		ArgumentsJSON: argumentsJSON,
	})
}

type scriptedCompleter struct {
	mu    sync.Mutex
	calls int
}

func (c *scriptedCompleter) Complete(_ context.Context, messages []types.Message, visibleTools []types.Tool) (types.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++

	if c.calls == 1 {
		if !hasTool(visibleTools, "calculate") {
			return types.Message{}, fmt.Errorf("scripted completer: calculate tool is not visible")
		}
		return types.Message{
			Role: "assistant",
			ToolCalls: []types.ToolCall{{
				ID:   "call-calculate-1",
				Type: "function",
				Function: types.ToolCallFunction{
					Name:      "calculate",
					Arguments: `{"operation":"multiply","left":6,"right":7}`,
				},
			}},
		}, nil
	}

	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == "tool" && message.Name == "calculate" && message.Content != nil {
			content := "模型已读取工具结果：" + *message.Content
			return types.Message{Role: "assistant", Content: &content}, nil
		}
	}
	return types.Message{}, fmt.Errorf("scripted completer: calculate result is missing")
}

func (c *scriptedCompleter) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type demoResult struct {
	Reply           string
	CompletionCalls int
	VisibleTools    int
	TelemetryEvents int
	ToolEvents      int
}

func runDemo(ctx context.Context) (demoResult, error) {
	registry := tools.NewRegistry(tools.WithCallTimeout(time.Second))
	if err := registry.Register(builtin.New()); err != nil {
		return demoResult{}, fmt.Errorf("register builtin tools: %w", err)
	}

	completer := &scriptedCompleter{}
	runtime := registryRuntime{registry: registry}
	agt, err := agent.NewWithComponents(agent.Components{
		Completer: completer,
		Tools:     runtime,
	})
	if err != nil {
		return demoResult{}, fmt.Errorf("assemble agent: %w", err)
	}
	defer agt.Shutdown()

	tracer := telemetry.NewMemoryTracer()
	hook, err := telemetry.NewLifecycleHook(tracer)
	if err != nil {
		return demoResult{}, fmt.Errorf("create telemetry hook: %w", err)
	}
	eng := engine.New(agt,
		engine.WithSystemPrompt("你是一个离线示例助手；需要计算时调用工具。"),
		engine.WithTelemetryHook(hook),
	)
	reply, err := eng.Chat(ctx, "计算 6 × 7")
	if err != nil {
		return demoResult{}, fmt.Errorf("chat: %w", err)
	}

	view, err := tracer.Query(ctx, telemetry.Query{})
	if err != nil {
		return demoResult{}, fmt.Errorf("query telemetry: %w", err)
	}
	toolEvents := 0
	for _, event := range view.Events {
		if event.Type == telemetry.EventToolBefore || event.Type == telemetry.EventToolAfter {
			toolEvents++
		}
	}
	return demoResult{
		Reply:           reply,
		CompletionCalls: completer.Calls(),
		VisibleTools:    len(registry.Tools()),
		TelemetryEvents: len(view.Events),
		ToolEvents:      toolEvents,
	}, nil
}

func hasTool(available []types.Tool, name string) bool {
	for _, definition := range available {
		if strings.EqualFold(definition.Function.Name, name) {
			return true
		}
	}
	return false
}

func main() {
	result, err := runDemo(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Println(result.Reply)
	fmt.Printf("LLM calls=%d, visible tools=%d, telemetry events=%d (tool=%d)\n",
		result.CompletionCalls, result.VisibleTools, result.TelemetryEvents, result.ToolEvents)
}
