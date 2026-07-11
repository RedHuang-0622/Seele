// Package fork provides the Fork() sugar — concurrent multi-agent execution.
package fork

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

// ForkNode executes multiple branches concurrently.
type ForkNode struct {
	node.BaseNode
	Branches      []node.ForkBranch
	MaxConcurrent int
	factory       node.AgentFactory
	DefaultPrompt string
}

// NewNode creates a fork node.
func NewNode(id string, branches []node.ForkBranch, maxConcurrent int, factory node.AgentFactory) *ForkNode {
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	return &ForkNode{
		BaseNode:      node.NewBaseNode(id, node.KindFork),
		Branches:      branches,
		MaxConcurrent: maxConcurrent,
		factory:       factory,
	}
}

// Run executes all branches concurrently and merges results.
func (n *ForkNode) Run(ctx context.Context, ec *types.WorkflowContext) (string, error) {
	type branchResult struct {
		label string
		out   string
		err   error
	}
	results := make([]branchResult, len(n.Branches))
	var wg sync.WaitGroup
	sem := make(chan struct{}, n.MaxConcurrent)

	for i, branch := range n.Branches {
		wg.Add(1)
		go func(i int, b node.ForkBranch) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results[i] = branchResult{label: b.Label, err: fmt.Errorf("panic: %v", r)}
				}
			}()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				results[i] = branchResult{label: b.Label, err: ctx.Err()}
				return
			}

			input := types.RenderTemplate(b.Input, ec)
			prompt := b.SystemPrompt
			if prompt == "" {
				prompt = n.DefaultPrompt
			}
			if prompt == "" {
				prompt = "You are a helpful assistant."
			}
			agt := n.factory.NewAgent(prompt)
			if agt == nil {
				results[i] = branchResult{label: b.Label, err: fmt.Errorf("nil agent")}
				return
			}
			out, err := agt.Chat(ctx, input)
			if err != nil {
				results[i] = branchResult{label: b.Label, err: err}
				return
			}
			results[i] = branchResult{label: b.Label, out: types.ToJSON(out)}
		}(i, branch)
	}
	wg.Wait()

	merged := make(map[string]any, len(results))
	var errs []string
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, fmt.Sprintf("[%s] %v", r.label, r.err))
			merged[r.label] = nil
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(r.out), &v); err == nil {
			merged[r.label] = v
		} else {
			merged[r.label] = r.out
		}
	}
	if len(errs) > 0 && len(merged) == 0 {
		return "", fmt.Errorf("all fork branches failed: %s", strings.Join(errs, "; "))
	}
	b, _ := json.Marshal(merged)
	return string(b), nil
}

// Add registers a fork node in the graph.
func Add(g *graph.Graph, id string, branches []node.ForkBranch, maxConcurrent int, factory node.AgentFactory) *ForkNode {
	n := NewNode(id, branches, maxConcurrent, factory)
	g.AddNode(n)
	return n
}
