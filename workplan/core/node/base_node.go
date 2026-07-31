// Package node defines the Node interface and four concrete node types.
// Nodes know what to do but not how to schedule — that is the runtime's responsibility.
package node

import (
	"context"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// NodeKind is optional descriptive metadata for built-in nodes. It is not part
// of the execution contract: custom nodes only need to implement ID and Run.
type NodeKind int

const (
	KindMethod     NodeKind = iota // Go function node
	KindLLM                        // Pure LLM call node
	KindAgent                      // Full Agent ReAct node
	KindAuto                       // Backward compat: equivalent to KindAgent
	KindStrategy                   // Custom strategy node
	KindApprove                    // Human approval node
	KindIf                         // Binary conditional branch
	KindSwitch                     // Multi-way conditional branch
	KindLoop                       // Loop node
	KindFork                       // Concurrent multi-agent branch
	KindJoin                       // Fork result join
	KindCheckpoint                 // Snapshot node
	KindEmit                       // Named variable writer
)

func (k NodeKind) String() string {
	names := map[NodeKind]string{
		KindMethod: "method", KindLLM: "llm", KindAgent: "agent",
		KindAuto: "auto", KindStrategy: "strategy", KindApprove: "approve",
		KindIf: "if", KindSwitch: "switch", KindLoop: "loop",
		KindFork: "fork", KindJoin: "join", KindCheckpoint: "checkpoint",
		KindEmit: "emit",
	}
	if name, ok := names[k]; ok {
		return name
	}
	return "unknown"
}

// Node is the minimal execution interface for graph nodes.
// Implementations must not depend on Graph, Scheduler, or Runner.
type Node interface {
	ID() string
	Run(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

// Kinded is an optional metadata contract implemented by Seele's built-in
// nodes. Runtimes must not require it in order to execute a Node.
type Kinded interface {
	Kind() NodeKind
}

// InputNode is a node whose declarative input can be serialized into a
// workflow definition.
type InputNode interface {
	Node
	DSLInput() string
}

// BaseNode provides common fields for all concrete node types.
type BaseNode struct {
	id   string
	kind NodeKind
}

// NewBaseNode creates a BaseNode with the given ID and kind.
func NewBaseNode(id string, kind NodeKind) BaseNode {
	return BaseNode{id: id, kind: kind}
}

func (n *BaseNode) ID() string     { return n.id }
func (n *BaseNode) Kind() NodeKind { return n.kind }

// LLMProvider is the interface for LLM calls, injected into LLMNode.
type LLMProvider interface {
	Chat(ctx context.Context, input string) (string, error)
	ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
}

// Agent is the minimal interface for an agent that can chat.
type Agent interface {
	Chat(ctx context.Context, input string) (string, error)
}

// AgentFactory is injected by the top layer to create agents on demand.
type AgentFactory interface {
	NewAgent(systemPrompt string) Agent
}

// StreamAgent is an optional extension for agents that support streaming.
type StreamAgent interface {
	Agent
	ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
}

// ForkBranch describes a single concurrent branch configuration.
type ForkBranch struct {
	Label        string
	SystemPrompt string
	Input        string
	EntryNodeID  string // Reserved: nested sub-WorkPlan
}

// SwitchCase describes a single case in a Switch node.
type SwitchCase struct {
	Match  func(string) bool // nil = default case
	NextID string
}
