package bridge

import (
	"context"
	"fmt"
	"slices"

	"github.com/RedHuang-0622/Seele/session"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
)

// AgentFactory adapts an assembled session.Agent runtime to workplan's
// product-neutral node.AgentFactory contract.
//
// The adapter is deliberately kept outside workplan/core: the WorkPlan kernel
// only knows how to invoke node.Agent, while this adapter owns the optional
// integration with session.Session. Each node gets a newly assembled Session so
// its working history is isolated from other nodes by default.
type AgentFactory struct {
	agent      session.Agent
	components session.SessionComponents
	sessionID  func(systemPrompt string) string
}

// AgentFactoryOption customizes the Session components created for each
// WorkPlan agent. The bridge always overwrites SessionComponents.Agent with
// the runtime passed to NewAgentFactory.
type AgentFactoryOption func(*AgentFactory)

// WithSessionComponents supplies optional per-session components such as
// prompt blocks, context processors, telemetry, or a caller-owned durable
// history. By default no durable history is supplied, which keeps parallel
// WorkPlan nodes isolated. Reusing a durable history is an explicit caller
// choice and may intentionally couple node sessions.
func WithSessionComponents(components session.SessionComponents) AgentFactoryOption {
	return func(factory *AgentFactory) {
		factory.components = cloneSessionComponents(components)
	}
}

// WithSessionID sets a function used to derive a session ID from the system
// prompt passed by a node. If it returns an empty string, Session generates an
// opaque ID. Supplying a function is useful when a product needs durable
// checkpoints keyed by a WorkPlan/node identity.
func WithSessionID(fn func(systemPrompt string) string) AgentFactoryOption {
	return func(factory *AgentFactory) {
		factory.sessionID = fn
	}
}

var _ node.AgentFactory = (*AgentFactory)(nil)

// NewAgentFactory validates and wraps an already assembled agent runtime. It
// does not create providers, tools, account pools, or sessions eagerly.
func NewAgentFactory(agent session.Agent, opts ...AgentFactoryOption) (*AgentFactory, error) {
	if isNilInterface(agent) {
		return nil, fmt.Errorf("agent.bridge: agent is required")
	}
	if isNilInterface(agent.LLM()) {
		return nil, fmt.Errorf("agent.bridge: agent LLM is required")
	}
	factory := &AgentFactory{agent: agent}
	for _, opt := range opts {
		if opt != nil {
			opt(factory)
		}
	}
	return factory, nil
}

// NewAgent creates an isolated conversation for a WorkPlan node. The node's
// systemPrompt wins over the configured Context.SystemPrompt when non-empty.
// AgentFactory cannot return an error, so a session-construction failure is
// represented by a node.Agent that returns that precise error from Chat.
func (factory *AgentFactory) NewAgent(systemPrompt string) node.Agent {
	if factory == nil || isNilInterface(factory.agent) {
		return failedAgent{err: fmt.Errorf("agent.bridge: agent is unavailable")}
	}
	components := cloneSessionComponents(factory.components)
	components.Agent = factory.agent
	if systemPrompt != "" {
		components.Context.SystemPrompt = systemPrompt
	}
	if factory.sessionID != nil {
		components.SessionID = factory.sessionID(systemPrompt)
	}
	conversation, err := session.NewSession(components)
	if err != nil {
		return failedAgent{err: fmt.Errorf("agent.bridge: create session: %w", err)}
	}
	return conversation
}

type failedAgent struct{ err error }

func (agent failedAgent) Chat(context.Context, string) (string, error) {
	if agent.err == nil {
		return "", fmt.Errorf("agent.bridge: session creation failed")
	}
	return "", agent.err
}

func cloneSessionComponents(components session.SessionComponents) session.SessionComponents {
	clone := components
	clone.Context.PromptBlocks = slices.Clone(components.Context.PromptBlocks)
	return clone
}
