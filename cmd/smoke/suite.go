package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/session"
	"github.com/RedHuang-0622/Seele/tools/builtin"
	toolgateway "github.com/RedHuang-0622/Seele/tools/gateway"
	"github.com/RedHuang-0622/Seele/tools/holder"
)

type smokeCase struct {
	Name         string
	ExpectedTool string
	Prompt       string
	Validate     func(string) error
}

type smokeResult struct {
	Name       string
	Tool       string
	Arguments  string
	ToolResult string
	Reply      string
}

type toolObservation struct {
	mu    sync.Mutex
	calls []session.ToolCallInfo
}

func (o *toolObservation) record(_ context.Context, info session.ToolCallInfo) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, info)
}

func (o *toolObservation) find(name string) (session.ToolCallInfo, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, call := range o.calls {
		if call.Name == name {
			return call, true
		}
	}
	return session.ToolCallInfo{}, false
}

func runSmokeSuite(ctx context.Context, client agent.Completer) ([]smokeResult, error) {
	runtime, err := newSmokeRuntime(client)
	if err != nil {
		return nil, err
	}
	defer runtime.Shutdown()
	cases := builtinSmokeCases()
	results := make([]smokeResult, 0, len(cases))
	for _, test := range cases {
		result, err := runSmokeCase(ctx, runtime, test)
		if err != nil {
			return nil, fmt.Errorf("smoke case %q: %w", test.Name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func newSmokeRuntime(client agent.Completer) (*agent.Agent, error) {
	toolHolder := holder.New()
	toolHolder.Register(builtin.New())
	gateway := toolgateway.NewDefaultGateway(toolHolder)
	components := agent.Components{Completer: client, Tools: gateway}
	components.StreamCompleter, _ = client.(agent.StreamCompleter)
	components.EventCompleter, _ = client.(agent.StreamEventCompleter)
	return agent.NewWithComponents(components)
}

func runSmokeCase(ctx context.Context, runtime *agent.Agent, test smokeCase) (smokeResult, error) {
	observation := &toolObservation{}
	chat := session.New(runtime,
		session.WithSystemPrompt("You are a function-calling smoke-test agent. You must call the tool explicitly requested by the user before answering. Never replace the requested tool call with mental calculation."),
		session.WithHooks(&session.LoopHooks{OnToolComplete: observation.record}),
	)
	reply, err := chat.Chat(ctx, test.Prompt)
	if err != nil {
		return smokeResult{}, err
	}
	call, ok := observation.find(test.ExpectedTool)
	if !ok {
		return smokeResult{}, fmt.Errorf("model did not call required tool %q", test.ExpectedTool)
	}
	if call.Error != nil {
		return smokeResult{}, fmt.Errorf("tool %q failed: %w", call.Name, call.Error)
	}
	if test.Validate != nil {
		if err := test.Validate(call.Result); err != nil {
			return smokeResult{}, fmt.Errorf("tool %q returned invalid result: %w", call.Name, err)
		}
	}
	if strings.TrimSpace(reply) == "" {
		return smokeResult{}, fmt.Errorf("model returned an empty final reply")
	}
	return smokeResult{
		Name: test.Name, Tool: call.Name, Arguments: call.Arguments,
		ToolResult: call.Result, Reply: reply,
	}, nil
}

func builtinSmokeCases() []smokeCase {
	return []smokeCase{
		{
			Name: "calculate", ExpectedTool: "calculate",
			Prompt:   "Call the calculate tool with operation=add, left=19, right=23. After receiving the tool result, report it briefly.",
			Validate: validateJSONNumber("result", 42),
		},
		{
			Name: "get time", ExpectedTool: "get_time",
			Prompt:   "Call the get_time tool with timezone=UTC. After receiving the tool result, report the RFC3339 time briefly.",
			Validate: validateJSONString("timezone", "UTC"),
		},
		{
			Name: "text stats", ExpectedTool: "text_stats",
			Prompt:   "Call the text_stats tool with the exact text 'Seele smoke test'. After receiving the tool result, report the word count briefly.",
			Validate: validateJSONNumber("words", 3),
		},
	}
}

func validateJSONNumber(field string, expected float64) func(string) error {
	return func(raw string) error {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		actual, ok := value[field].(float64)
		if !ok || actual != expected {
			return fmt.Errorf("field %q = %#v, want %v", field, value[field], expected)
		}
		return nil
	}
}

func validateJSONString(field, expected string) func(string) error {
	return func(raw string) error {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		if actual, ok := value[field].(string); !ok || actual != expected {
			return fmt.Errorf("field %q = %#v, want %q", field, value[field], expected)
		}
		return nil
	}
}
