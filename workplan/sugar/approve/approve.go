// Package approve provides the Approve() sugar — human-in-the-loop approval gate.
package approve

import (
	"context"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ChoiceOption describes a single choice in an approval question.
type ChoiceOption struct {
	Key, Label, Description, Style string
}

// Question represents an approval request to a human.
type Question struct {
	ID      string
	Content string
	Options []ChoiceOption
	KVS     map[string]any
	Timeout time.Duration
}

// DefaultChoice returns the first option's key.
func (q Question) DefaultChoice() string {
	if len(q.Options) > 0 {
		return q.Options[0].Key
	}
	return ""
}

// Resolve looks up the value for a choice key.
func (q Question) Resolve(choice string) (any, bool) {
	if v, ok := q.KVS[choice]; ok {
		return v, true
	}
	if key := q.DefaultChoice(); key != "" {
		if v, ok := q.KVS[key]; ok {
			return v, false
		}
	}
	return nil, false
}

// ApprovalGate is the interface for human approval (CLI/HTTP/Auto).
type ApprovalGate interface {
	Ask(ctx context.Context, q Question) (any, error)
}

// ApproveNode pauses execution and asks for human approval.
type ApproveNode struct {
	node.BaseNode
	Input        string
	SystemPrompt string
	Options      []ChoiceOption
	KVS          map[string]any
	Timeout      time.Duration
	gate         ApprovalGate
	factory      node.AgentFactory
}

// NewNode creates an approval node.
func NewNode(id string, gate ApprovalGate, factory node.AgentFactory) *ApproveNode {
	return &ApproveNode{
		BaseNode: node.NewBaseNode(id, node.KindApprove),
		gate:     gate,
		factory:  factory,
	}
}

// BuildKVS returns the key-value store for the approval question.
func (n *ApproveNode) BuildKVS() map[string]any {
	if n.KVS != nil {
		return n.KVS
	}
	kvs := make(map[string]any, len(n.Options))
	for _, opt := range n.Options {
		kvs[opt.Key] = opt.Key
	}
	return kvs
}

// Run presents the question and waits for an answer.
func (n *ApproveNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	q := Question{
		ID:      n.ID(),
		Content: wc.PrevOutput,
		Options: n.Options,
		KVS:     n.BuildKVS(),
		Timeout: n.Timeout,
	}
	decision, err := n.gate.Ask(ctx, q)
	if err != nil {
		return "", err
	}
	choice, _ := decision.(string)
	switch choice {
	case "skip":
		return wc.PrevOutput, nil
	case "abort":
		return "", fmt.Errorf("aborted at approve node %q", n.ID())
	default:
		// Execute after approval
		input := types.RenderTemplate(n.Input, wc)
		prompt := n.SystemPrompt
		if prompt == "" {
			prompt = "You are a helpful assistant."
		}
		agt := n.factory.NewAgent(prompt)
		out, err := agt.Chat(ctx, input)
		if err != nil {
			return "", err
		}
		return types.ToJSON(out), nil
	}
}

// Add registers an approval node in the graph.
func Add(g *graph.Graph, id, input string, gate ApprovalGate, factory node.AgentFactory, opts ...func(*ApproveNode)) *ApproveNode {
	n := NewNode(id, gate, factory)
	n.Input = input
	n.Options = Choices("execute", "skip", "abort")
	for _, o := range opts {
		o(n)
	}
	g.AddNode(n)
	return n
}

// WithOptions sets custom options for the approval question.
func WithOptions(opts []ChoiceOption) func(*ApproveNode) {
	return func(n *ApproveNode) { n.Options = opts }
}

// WithKVS sets custom key-value store for the approval question.
func WithKVS(kvs map[string]any) func(*ApproveNode) {
	return func(n *ApproveNode) { n.KVS = kvs }
}

// WithTimeout sets the timeout for the approval question.
func WithTimeout(d time.Duration) func(*ApproveNode) {
	return func(n *ApproveNode) { n.Timeout = d }
}

// Choices creates ChoiceOptions from string keys.
func Choices(keys ...string) []ChoiceOption {
	builtin := map[string]ChoiceOption{
		"execute": {Key: "execute", Label: "执行", Description: "按计划执行", Style: "primary"},
		"skip":    {Key: "skip", Label: "跳过", Description: "跳过当前节点", Style: "secondary"},
		"abort":   {Key: "abort", Label: "终止", Description: "终止工作流", Style: "danger"},
		"confirm": {Key: "confirm", Label: "确认", Description: "确认并继续", Style: "primary"},
		"retry":   {Key: "retry", Label: "重试", Description: "重新执行", Style: "warning"},
	}
	r := make([]ChoiceOption, len(keys))
	for i, k := range keys {
		if opt, ok := builtin[k]; ok {
			r[i] = opt
		} else {
			r[i] = ChoiceOption{Key: k, Label: k}
		}
	}
	return r
}

// DefaultOptions returns the default approve/execute choices.
func DefaultOptions() []ChoiceOption { return Choices("execute", "skip", "abort") }
