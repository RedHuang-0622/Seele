package seelectx

import (
	"context"

	"github.com/RedHuang-0622/Seele/types"
)

// Completer is the minimal synchronous client needed by context services.
type Completer interface {
	Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error)
}

// QuickChatRequest is an explicit one-shot completion request. Callers choose
// the messages and tools; no session history is inherited implicitly.
type QuickChatRequest struct {
	Messages []types.Message
	Tools    []types.Tool
}

// QuickChat is the capability used by Seelex for ephemeral completions.
type QuickChat interface {
	Complete(ctx context.Context, request QuickChatRequest) (types.Message, error)
}

// QuickChatClient adapts a normal LLM client to QuickChat. It intentionally
// has no history, cache, tool registry, or session fields.
type QuickChatClient struct{ client Completer }

func NewQuickChat(client Completer) (*QuickChatClient, error) {
	if client == nil {
		return nil, ErrNilCompleter
	}
	return &QuickChatClient{client: client}, nil
}

func (c *QuickChatClient) Complete(ctx context.Context, request QuickChatRequest) (types.Message, error) {
	if c == nil || c.client == nil {
		return types.Message{}, ErrNilCompleter
	}
	return c.client.Complete(ctx, cloneMessages(request.Messages), append([]types.Tool(nil), request.Tools...))
}
