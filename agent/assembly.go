package agent

import (
	"context"
	"fmt"

	"github.com/RedHuang-0622/Seele/types"
)

// Completer is the minimum non-streaming LLM capability accepted by Agent.
type Completer interface {
	Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
}

// StreamCompleter is optional. When omitted, ChatStream-compatible callers
// receive a single chunk produced by Completer.
type StreamCompleter interface {
	CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error)
}

// StreamEventCompleter is the optional structured streaming capability.
type StreamEventCompleter interface {
	CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error)
}

// ToolRuntime is the provider-neutral tool surface needed by Agent and the
// ReAct loop. MCP, microHub, skills, and function-calling registries can all be
// adapted to this interface outside Agent.
type ToolRuntime interface {
	VisibleTools(ctx context.Context) []types.Tool
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)
}

// Components describes the explicit, side-effect-free Agent construction
// path. Account pools should expose a Completer adapter and be injected here;
// Agent does not construct or configure an account pool itself.
type Components struct {
	Completer       Completer
	StreamCompleter StreamCompleter
	EventCompleter  StreamEventCompleter
	Tools           ToolRuntime
	Logger          Logger
}

// NewWithComponents creates an Agent without starting microHub, loading
// registry files, constructing account pools, or registering any tools.
func NewWithComponents(components Components) (*Agent, error) {
	if components.Completer == nil {
		return nil, fmt.Errorf("agent: completer is required")
	}
	logger := components.Logger
	if logger == nil {
		logger = &stdLogger{}
	}
	client := &composedClient{
		complete: components.Completer,
		stream:   components.StreamCompleter,
		events:   components.EventCompleter,
	}
	if client.stream == nil {
		client.stream, _ = components.Completer.(StreamCompleter)
	}
	if client.events == nil {
		client.events, _ = components.Completer.(StreamEventCompleter)
	}
	return &Agent{
		llmClient:   client,
		toolRuntime: components.Tools,
		opts:        Options{Logger: logger},
		shutdown:    make(chan struct{}),
		done:        make(chan struct{}),
	}, nil
}

type composedClient struct {
	complete Completer
	stream   StreamCompleter
	events   StreamEventCompleter
}

func (c *composedClient) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return c.complete.Complete(ctx, messages, tools)
}

func (c *composedClient) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(string)) (string, string, []types.ToolCall, error) {
	if c.stream != nil {
		return c.stream.CompleteStream(ctx, messages, tools, onChunk)
	}
	message, err := c.complete.Complete(ctx, messages, tools)
	if err != nil {
		return "", "", nil, err
	}
	content := ""
	if message.Content != nil {
		content = *message.Content
		if onChunk != nil && content != "" {
			onChunk(content)
		}
	}
	return content, message.ReasoningContent, message.ToolCalls, nil
}

func (c *composedClient) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (string, string, []types.ToolCall, error) {
	if c.events != nil {
		return c.events.CompleteStreamEvents(ctx, messages, tools, onEvent)
	}
	return c.CompleteStream(ctx, messages, tools, func(delta string) {
		if onEvent != nil {
			onEvent(types.StreamEvent{Type: types.StreamEventText, Content: delta})
		}
	})
}
