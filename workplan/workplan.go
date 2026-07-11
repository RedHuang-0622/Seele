// Package workplan is the top-level entry point for defining and executing workflows.
//
// WorkPlan provides a chainable DSL for building directed acyclic graphs (DAGs)
// of nodes (Auto/LLM/Method/Fork/Loop/If/Switch/Approve/Emit/Checkpoint) and
// executing them with an injected AgentFactory.
package workplan

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
	"github.com/RedHuang-0622/Seele/workplan/runtime/runner"
	"github.com/RedHuang-0622/Seele/workplan/sugar/approve"
	sauto "github.com/RedHuang-0622/Seele/workplan/sugar/auto"
	scheckpoint "github.com/RedHuang-0622/Seele/workplan/sugar/checkpoint"
	"github.com/RedHuang-0622/Seele/workplan/sugar/emit"
	"github.com/RedHuang-0622/Seele/workplan/sugar/fork"
	sloop "github.com/RedHuang-0622/Seele/workplan/sugar/loop"
	sw "github.com/RedHuang-0622/Seele/workplan/sugar/switch"
)

// WorkPlan is the workflow definition and execution engine.
type WorkPlan struct {
	graph         *graph.Graph
	runner        *runner.Runner
	factory       node.AgentFactory
	defaultPrompt string
	tracer        Tracer

	// Auto-linking state (chainable API support)
	entryID       string
	lastNodeID    string
	pendingGate   *approve.Question
}

// Option configures a WorkPlan instance.
type Option func(*WorkPlan)

// WithDefaultPrompt sets the default system prompt for auto/agent nodes.
func WithDefaultPrompt(prompt string) Option {
	return func(wp *WorkPlan) { wp.defaultPrompt = prompt }
}

// WithTracer sets a tracer for execution observability.
func WithTracer(t Tracer) Option {
	return func(wp *WorkPlan) { wp.tracer = t }
}

// New creates a new WorkPlan with the given AgentFactory.
func New(factory node.AgentFactory, opts ...Option) *WorkPlan {
	g := graph.New()
	wp := &WorkPlan{
		graph:   g,
		runner:  runner.New(g, factory),
		factory: factory,
	}
	for _, o := range opts {
		o(wp)
	}
	return wp
}

// Graph returns the underlying graph structure.
func (wp *WorkPlan) Graph() *graph.Graph { return wp.graph }

// ─── Auto / Step ─────────────────────────────────────────────────────

// Auto adds an agent (Auto) strategy node with auto-linking.
func (wp *WorkPlan) Auto(id, input string) *WorkPlan {
	sauto.Add(wp.graph, id, input, wp.factory)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// Step is an alias for Auto with auto-linking.
func (wp *WorkPlan) Step(id, input string) *WorkPlan {
	return wp.Auto(id, input)
}

// Method adds a Go function node with auto-linking.
func (wp *WorkPlan) Method(id string, fn func(ctx context.Context, input string) (string, error)) *WorkPlan {
	sauto.AddMethod(wp.graph, id, fn)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// LLM adds a pure LLM call node with auto-linking.
func (wp *WorkPlan) LLM(id, input string, provider node.LLMProvider) *WorkPlan {
	sauto.AddLLM(wp.graph, id, input, provider)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// ─── Pipeline ────────────────────────────────────────────────────────

// Pipeline executes multiple Auto steps sequentially.
func (wp *WorkPlan) Pipeline(steps ...StepDef) *WorkPlan {
	for _, step := range steps {
		wp.Auto(step.ID, step.Input)
	}
	return wp
}

// StepDef defines a single pipeline step.
type StepDef struct {
	ID    string
	Input string
}

// PipelineStep is an alias for StepDef (backward compatibility).
type PipelineStep = StepDef

// Step creates a StepDef for use with Pipeline.
func Step(id, input string) StepDef {
	return StepDef{ID: id, Input: input}
}

// ─── Loop ────────────────────────────────────────────────────────────

// Loop adds a loop node with auto-linking and returns a Signal for real-time access.
func (wp *WorkPlan) Loop(id, bodyID string, opts ...func(*sloop.LoopNode)) *sloop.Signal {
	sig := sloop.Add(wp.graph, id, bodyID, wp.factory, opts...)
	wp.autoLink(wp.graph.GetNode(id))
	return sig
}

// Retry is a convenience wrapper for Loop with retry semantics.
func (wp *WorkPlan) Retry(id, bodyID string, maxIter int, successCond func(string) bool, exhaustedID string) *sloop.Signal {
	return wp.Loop(id, bodyID,
		sloop.WithUntil(successCond),
		sloop.WithMaxIter(maxIter),
		sloop.WithOnExhausted(exhaustedID),
	)
}

// ─── Fork ────────────────────────────────────────────────────────────

// Fork adds a concurrent fork node with auto-linking.
func (wp *WorkPlan) Fork(id string, branches []node.ForkBranch, maxConcurrent int) *WorkPlan {
	fork.Add(wp.graph, id, branches, maxConcurrent, wp.factory)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// ─── If / Switch ─────────────────────────────────────────────────────

// If adds a binary conditional branch node with auto-linking.
func (wp *WorkPlan) If(id string, cond func(string) bool, trueID, falseID string) *WorkPlan {
	sw.If(wp.graph, id, cond, trueID, falseID)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// Switch adds a multi-way conditional branch node with auto-linking.
func (wp *WorkPlan) Switch(id string, cases ...node.SwitchCase) *WorkPlan {
	sw.Switch(wp.graph, id, cases...)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// ─── Approve / Gate ──────────────────────────────────────────────────

// Approve adds an approval pause node with auto-linking.
func (wp *WorkPlan) Approve(id, input string, gate approve.ApprovalGate, opts ...func(*approve.ApproveNode)) *WorkPlan {
	approve.Add(wp.graph, id, input, gate, wp.factory, opts...)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// Gate adds a simplified approval node (execute/abort only) with auto-linking.
func (wp *WorkPlan) Gate(id, content string) *WorkPlan {
	g := &AutoApproveGate{}
	approve.Add(wp.graph, id, content, g, wp.factory,
		approve.WithOptions(approve.Choices("execute", "abort")),
	)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// PendingQuestion returns the current approval question (for gate pauses).
func (wp *WorkPlan) PendingQuestion() *approve.Question {
	return wp.pendingGate
}

// ─── Emit ────────────────────────────────────────────────────────────

// Emit adds an emit node that writes PrevOutput to a named variable.
func (wp *WorkPlan) Emit(id, key string) *WorkPlan {
	emit.Add(wp.graph, id, key)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// ─── Checkpoint ──────────────────────────────────────────────────────

// Checkpoint adds a checkpoint/snapshot node with auto-linking.
func (wp *WorkPlan) Checkpoint(id string) *WorkPlan {
	scheckpoint.Add(wp.graph, id)
	wp.autoLink(wp.graph.GetNode(id))
	return wp
}

// ─── Execution ───────────────────────────────────────────────────────

// ExecState returns the current execution state.
func (wp *WorkPlan) ExecState() ExecState { return StateNotStarted }

// Vars returns the plan-level variables.
func (wp *WorkPlan) Vars() map[string]string { return make(map[string]string) }

// Run validates and executes the workflow graph.
func (wp *WorkPlan) Run(ctx context.Context) (*types.WorkPlanResult, error) {
	return wp.runner.Run(ctx)
}

// Resume continues execution from a saved checkpoint.
func (wp *WorkPlan) Resume(ctx context.Context, snapshotID string) (*types.WorkPlanResult, error) {
	return wp.runner.Resume(ctx, snapshotID)
}

// ─── Serialization ───────────────────────────────────────────────────

// ExportJSON exports the current graph as a JSON plan.
func (wp *WorkPlan) ExportJSON() (string, error) {
	type ps struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	type es struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	type plan struct {
		EntryNodeID string `json:"entry_node_id"`
		Nodes       []ps   `json:"nodes"`
		Edges       []es   `json:"edges"`
	}
	p := &plan{EntryNodeID: wp.graph.Entry()}
	for _, id := range wp.graph.AllNodes() {
		n := wp.graph.GetNode(id)
		kind := "unknown"
		if n != nil {
			kind = n.Kind().String()
		}
		p.Nodes = append(p.Nodes, ps{ID: id, Kind: kind})
	}
	for _, e := range wp.graph.AllEdges() {
		p.Edges = append(p.Edges, es{From: e.From, To: e.To})
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ─── Internal Helpers ───────────────────────────────────────────────

// autoLink automatically connects the last added node to the new one.
func (wp *WorkPlan) autoLink(n node.Node) {
	if n == nil {
		return
	}
	if wp.entryID == "" {
		wp.entryID = n.ID()
		wp.lastNodeID = n.ID()
		wp.graph.SetEntry(n.ID())
		return
	}
	if wp.lastNodeID == "" {
		wp.lastNodeID = n.ID()
		return
	}
	prev := wp.graph.GetNode(wp.lastNodeID)
	if prev == nil {
		wp.lastNodeID = n.ID()
		return
	}
	switch prev.Kind() {
	case node.KindIf, node.KindSwitch:
		// Conditional edges are added by If/Switch builders
	case node.KindFork, node.KindLoop:
		// Fork/Loop edges may already be set; only add if no outgoing edges
		edges := wp.graph.GetEdgesFrom(wp.lastNodeID)
		if len(edges) == 0 {
			wp.graph.AddEdge(edge.Edge{From: wp.lastNodeID, To: n.ID()})
		}
	default:
		edges := wp.graph.GetEdgesFrom(wp.lastNodeID)
		if len(edges) == 0 {
			wp.graph.AddEdge(edge.Edge{From: wp.lastNodeID, To: n.ID()})
		}
	}
	wp.lastNodeID = n.ID()
}

// WithNext sets the next node ID after the current node (used as a node option).
func WithNext(nextID string) func(*sauto.StrategyNode) {
	return func(n *sauto.StrategyNode) {}
}

// SetTracer sets a tracer on the workplan (for backward compatibility).
func (wp *WorkPlan) SetTracer(t Tracer) { wp.tracer = t }

// ─── ExecState backward compatibility ───────────────────────────────

// ExecState represents the execution state of a WorkPlan.
type ExecState int

const (
	StateNotStarted       ExecState = iota
	StateExecuting
	StateAwaitingApproval
	StateCompleted
	StateAborted
	StateFailed
)

func (s ExecState) String() string {
	switch s {
	case StateNotStarted:
		return "not_started"
	case StateExecuting:
		return "executing"
	case StateAwaitingApproval:
		return "awaiting_approval"
	case StateCompleted:
		return "completed"
	case StateAborted:
		return "aborted"
	case StateFailed:
		return "failed"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// ─── Built-in conditions (shorthand helpers) ─────────────────────────

// Contains returns a condition function that checks for substring match.
func Contains(substr string) func(string) bool { return sw.Contains(substr) }

// NotContains returns the inverse of Contains.
func NotContains(substr string) func(string) bool { return sw.NotContains(substr) }

// Case creates a SwitchCase for use with Switch.
func Case(match func(string) bool, nextID string) node.SwitchCase {
	return node.SwitchCase{Match: match, NextID: nextID}
}

// Default creates a default SwitchCase (always matches).
func Default(nextID string) node.SwitchCase {
	return node.SwitchCase{Match: nil, NextID: nextID}
}

// ─── Loop option helpers ─────────────────────────────────────────────

// Until sets the loop termination condition.
func Until(cond func(string) bool) func(*sloop.LoopNode) { return sloop.WithUntil(cond) }

// MaxIter sets the maximum loop iterations.
func MaxIter(max int) func(*sloop.LoopNode) { return sloop.WithMaxIter(max) }

// OnExhausted sets the fallback node when MaxIter is reached.
func OnExhausted(id string) func(*sloop.LoopNode) { return sloop.WithOnExhausted(id) }

// ─── Type re-exports for API compatibility ───────────────────────────

// Agent is the minimal agent interface required by workplan.
type Agent = node.Agent

// AgentFactory creates agents for workplan nodes.
type AgentFactory = node.AgentFactory

// StreamAgent is an agent with streaming support.
type StreamAgent = node.StreamAgent

// ForkBranch describes a concurrent fork branch.
type ForkBranch = node.ForkBranch

// SwitchCase describes a switch case.
type SwitchCase = node.SwitchCase

// Compile-time interface checks
var _ = time.Now
