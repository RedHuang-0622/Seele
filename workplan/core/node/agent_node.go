package node

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// AgentNode wraps a full Agent with its system prompt and optional tool filter.
type AgentNode struct {
	BaseNode
	factory      AgentFactory
	systemPrompt string
	toolFilter   []string
	onChunk      func(string)
}

// NewAgentNode creates an Agent node.
func NewAgentNode(id string, factory AgentFactory, systemPrompt string) *AgentNode {
	return &AgentNode{
		BaseNode:     NewBaseNode(id, KindAgent),
		factory:      factory,
		systemPrompt: systemPrompt,
	}
}

// Run executes the agent with PrevOutput as input.
func (n *AgentNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	prompt := n.systemPrompt
	if prompt == "" {
		prompt = "You are a helpful assistant."
	}
	agt := n.factory.NewAgent(prompt)
	if f, ok := agt.(interface{ SetToolFilter([]string) }); ok && len(n.toolFilter) > 0 {
		f.SetToolFilter(n.toolFilter)
	}
	input := types.RenderTemplate(wc.PrevOutput, wc)
	if sa, ok := agt.(StreamAgent); ok && n.onChunk != nil {
		return sa.ChatStream(ctx, input, n.onChunk)
	}
	return agt.Chat(ctx, input)
}

// WithToolFilter sets which tools the agent can use.
func (n *AgentNode) WithToolFilter(filter []string) *AgentNode {
	n.toolFilter = filter
	return n
}

// WithOnChunk sets a streaming callback.
func (n *AgentNode) WithOnChunk(cb func(string)) *AgentNode {
	n.onChunk = cb
	return n
}
