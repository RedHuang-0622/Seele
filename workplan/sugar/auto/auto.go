// Package auto provides the Auto() sugar — an agent node builder.
package auto

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// StrategyNode wraps a NodeStrategy as a graph node.
type StrategyNode struct {
	node.BaseNode
	Strategy     NodeStrategy
	SystemPrompt string
	Input        string
	ToolFilter   []string
	onChunk      func(string)
}

// NodeStrategy is the execution strategy interface (Strategy pattern).
type NodeStrategy interface {
	Execute(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

// Run implements node.Node.
func (n *StrategyNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if n.Input != "" {
		wc.PrevOutput = types.RenderTemplate(n.Input, wc)
	}
	if n.Strategy == nil {
		return wc.PrevOutput, nil
	}
	return n.Strategy.Execute(ctx, wc)
}

// MethodStrategy executes a plain Go function.
type MethodStrategy struct {
	Fn func(ctx context.Context, input string) (string, error)
}

func NewMethodStrategy(fn func(ctx context.Context, input string) (string, error)) *MethodStrategy {
	return &MethodStrategy{Fn: fn}
}

func (s *MethodStrategy) Execute(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	out, err := s.Fn(ctx, wc.PrevOutput)
	if err != nil {
		return "", err
	}
	return types.ToJSON(out), nil
}

// LLMStrategy executes a pure LLM call.
type LLMStrategy struct {
	Provider node.LLMProvider
	OnChunk  func(string)
}

func NewLLMStrategy(provider node.LLMProvider) *LLMStrategy {
	return &LLMStrategy{Provider: provider}
}

func (s *LLMStrategy) Execute(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if s.OnChunk != nil {
		if sa, ok := s.Provider.(interface {
			ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
		}); ok {
			out, err := sa.ChatStream(ctx, wc.PrevOutput, s.OnChunk)
			if err != nil {
				return "", err
			}
			return types.ToJSON(out), nil
		}
	}
	out, err := s.Provider.Chat(ctx, wc.PrevOutput)
	if err != nil {
		return "", err
	}
	return types.ToJSON(out), nil
}

// AgentStrategy executes a full Agent ReAct loop.
type AgentStrategy struct {
	Factory      node.AgentFactory
	SystemPrompt string
	ToolFilter   []string
	OnChunk      func(string)
}

func NewAgentStrategy(factory node.AgentFactory, systemPrompt string, toolFilter ...string) *AgentStrategy {
	return &AgentStrategy{Factory: factory, SystemPrompt: systemPrompt, ToolFilter: toolFilter}
}

func (s *AgentStrategy) Execute(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	prompt := s.SystemPrompt
	if prompt == "" {
		prompt = "You are a helpful assistant."
	}
	agt := s.Factory.NewAgent(prompt)
	if f, ok := agt.(interface{ SetToolFilter([]string) }); ok && len(s.ToolFilter) > 0 {
		f.SetToolFilter(s.ToolFilter)
	}
	if sa, ok := agt.(node.StreamAgent); ok && s.OnChunk != nil {
		out, err := sa.ChatStream(ctx, wc.PrevOutput, s.OnChunk)
		if err != nil {
			return "", err
		}
		return types.ToJSON(out), nil
	}
	out, err := agt.Chat(ctx, wc.PrevOutput)
	if err != nil {
		return "", err
	}
	return types.ToJSON(out), nil
}

// Add registers an auto (Agent) strategy node in the graph.
func Add(g *graph.Graph, id, input string, factory node.AgentFactory) *StrategyNode {
	n := &StrategyNode{
		BaseNode: node.NewBaseNode(id, node.KindAuto),
		Strategy: NewAgentStrategy(factory, ""),
		Input:    input,
	}
	g.AddNode(n)
	return n
}

// AddMethod registers a method (Go function) strategy node.
func AddMethod(g *graph.Graph, id string, fn func(ctx context.Context, input string) (string, error)) *StrategyNode {
	n := &StrategyNode{
		BaseNode: node.NewBaseNode(id, node.KindMethod),
		Strategy: NewMethodStrategy(fn),
	}
	g.AddNode(n)
	return n
}

// AddLLM registers an LLM strategy node.
func AddLLM(g *graph.Graph, id, input string, provider node.LLMProvider) *StrategyNode {
	n := &StrategyNode{
		BaseNode: node.NewBaseNode(id, node.KindLLM),
		Strategy: NewLLMStrategy(provider),
		Input:    input,
	}
	g.AddNode(n)
	return n
}

// WithToolFilter sets the tool filter on a strategy node.
func WithToolFilter(filter []string) func(*StrategyNode) {
	return func(n *StrategyNode) { n.ToolFilter = filter }
}

// WithSystemPrompt sets the system prompt on a strategy node.
func WithSystemPrompt(prompt string) func(*StrategyNode) {
	return func(n *StrategyNode) { n.SystemPrompt = prompt }
}

// WithOnChunk sets a streaming callback.
func WithOnChunk(cb func(string)) func(*StrategyNode) {
	return func(n *StrategyNode) { n.onChunk = cb }
}
