package node

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// LLMNode wraps an LLM provider for pure LLM calls without tool access.
type LLMNode struct {
	BaseNode
	provider LLMProvider
	onChunk  func(string)
}

// NewLLMNode creates an LLM node with the given provider.
func NewLLMNode(id string, provider LLMProvider) *LLMNode {
	return &LLMNode{
		BaseNode: NewBaseNode(id, KindLLM),
		provider: provider,
	}
}

// Run executes the LLM call with PrevOutput as input.
func (n *LLMNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	input := wc.PrevOutput
	if n.onChunk != nil {
		if sa, ok := n.provider.(interface {
			ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
		}); ok {
			return sa.ChatStream(ctx, input, n.onChunk)
		}
	}
	return n.provider.Chat(ctx, input)
}

// WithOnChunk sets a streaming callback.
func (n *LLMNode) WithOnChunk(cb func(string)) *LLMNode {
	n.onChunk = cb
	return n
}
