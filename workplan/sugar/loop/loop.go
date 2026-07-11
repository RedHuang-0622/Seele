// Package loop provides the Loop() sugar — a repeatable execution node with Signal.
package loop

import (
	"context"
	"fmt"
	"sync"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// Signal provides real-time access to loop iteration results.
type Signal struct {
	mu        sync.RWMutex
	value     string
	iter      int
	cbs       []func(string)
	done      chan struct{}
	closeOnce sync.Once
}

// NewSignal creates a signal.
func NewSignal() *Signal {
	return &Signal{done: make(chan struct{}), value: `""`}
}

// Get returns the current value as raw JSON.
func (s *Signal) Get() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.value }

// GetString returns the current value as a plain string.
func (s *Signal) GetString() string { return types.FromJSON(s.Get()) }

// Iter returns the current iteration count.
func (s *Signal) Iter() int { s.mu.RLock(); defer s.mu.RUnlock(); return s.iter }

// Set updates the signal value for a given iteration.
func (s *Signal) Set(raw string, iter int) {
	s.mu.Lock()
	s.value = types.ToJSON(raw)
	s.iter = iter
	cbs := make([]func(string), len(s.cbs))
	copy(cbs, s.cbs)
	s.mu.Unlock()
	for _, cb := range cbs {
		cb(types.ToJSON(raw))
	}
}

// Close signals that the loop has ended.
func (s *Signal) Close() { s.closeOnce.Do(func() { close(s.done) }) }

// Wait blocks until the loop ends and returns the final value.
func (s *Signal) Wait() string { <-s.done; return s.Get() }

// OnUpdate registers a callback for each iteration update.
func (s *Signal) OnUpdate(cb func(string)) {
	s.mu.Lock()
	s.cbs = append(s.cbs, cb)
	s.mu.Unlock()
}

// LoopNode executes its body repeatedly until a condition is met.
type LoopNode struct {
	node.BaseNode
	BodyID      string           // ID of the body node to repeat
	Until       func(string) bool // termination condition, nil = run once
	MaxIter     int              // maximum iterations, 0 = unlimited
	OnExhausted string           // fallback node ID when MaxIter reached
	Signal      *Signal
	factory     node.AgentFactory
	bodyPrompt  string
	bodyInput   string
}

// NewNode creates a loop node.
func NewNode(id, bodyID string, factory node.AgentFactory) *LoopNode {
	return &LoopNode{
		BaseNode: node.NewBaseNode(id, node.KindLoop),
		BodyID:   bodyID,
		factory:  factory,
		Signal:   NewSignal(),
	}
}

// Run executes the loop body repeatedly.
func (n *LoopNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	defer n.Signal.Close()
	current := wc.PrevOutput
	prompt := n.bodyPrompt
	if prompt == "" {
		prompt = "You are a helpful assistant."
	}
	for iter := 0; ; iter++ {
		select {
		case <-ctx.Done():
			return n.Signal.Get(), ctx.Err()
		default:
		}
		input := n.bodyInput
		if input == "" {
			input = current
		} else {
			input = types.RenderTemplate(n.bodyInput, wc)
		}
		agt := n.factory.NewAgent(prompt)
		out, err := agt.Chat(ctx, input)
		if err != nil {
			return n.Signal.Get(), fmt.Errorf("loop iter %d: %w", iter, err)
		}
		n.Signal.Set(out, iter+1)
		current = out

		if n.Until != nil && n.Until(types.FromJSON(out)) {
			break
		}
		if n.MaxIter > 0 && iter+1 >= n.MaxIter {
			break
		}
	}
	return n.Signal.Get(), nil
}

// Add registers a loop node in the graph and returns its Signal.
func Add(g *graph.Graph, id, bodyID string, factory node.AgentFactory, opts ...func(*LoopNode)) *Signal {
	n := NewNode(id, bodyID, factory)
	for _, o := range opts {
		o(n)
	}
	g.AddNode(n)
	return n.Signal
}

// WithUntil sets the loop termination condition.
func WithUntil(cond func(string) bool) func(*LoopNode) {
	return func(n *LoopNode) { n.Until = cond }
}

// WithMaxIter sets the maximum loop iterations.
func WithMaxIter(max int) func(*LoopNode) {
	return func(n *LoopNode) { n.MaxIter = max }
}

// WithOnExhausted sets the fallback node when MaxIter is reached.
func WithOnExhausted(id string) func(*LoopNode) {
	return func(n *LoopNode) { n.OnExhausted = id }
}

// WithBodyConfig sets the body agent's system prompt and input template.
func WithBodyConfig(prompt, input string) func(*LoopNode) {
	return func(n *LoopNode) { n.bodyPrompt = prompt; n.bodyInput = input }
}
