// Package workplan is the top-level entry point for defining and executing workflows.
//
// WorkPlan provides a chainable DSL for building directed acyclic graphs (DAGs)
// of nodes (Auto/LLM/Method/Fork/Loop/If/Switch/Approve/Emit/Checkpoint) and
// executing them with an injected AgentFactory.
package workplan

import (
	"context"
	"fmt"
	"time"

	frameworkevent "github.com/RedHuang-0622/Seele/event"
	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
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
	plan          *coreplan.Plan
	runner        *runner.Runner
	factory       node.AgentFactory
	defaultPrompt string
	tracer        Tracer

	// Auto-linking state (chainable API support)
	entryID     string
	lastNodeID  string
	pendingGate *approve.Question

	// NodeHook 每节点完成时回调，按需选填。
	// 用于 plan_run 实时回传进度给 TUI（seelex plan visualization）。
	NodeHook           func(nr *types.NodeResult)
	BranchEventHook    func(forkexec.Event)
	BranchRuntimeFor   func(string) forkexec.BranchRuntime
	ForkPolicy         forkexec.Policy
	ForkJoinPolicy     forkexec.JoinPolicy
	MaxForkConcurrency int
	EventConfig        runner.EventConfig
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

// WithBranchEventHook observes queued/started/completed/failed/canceled/panicked branch events.
func WithBranchEventHook(hook func(forkexec.Event)) Option {
	return func(wp *WorkPlan) { wp.BranchEventHook = hook }
}

// WithBranchRuntimeResolver accepts Seelex-injected read-only branch runtime.
func WithBranchRuntimeResolver(resolver func(string) forkexec.BranchRuntime) Option {
	return func(wp *WorkPlan) { wp.BranchRuntimeFor = resolver }
}

// WithForkPolicy explicitly enables the requested fork policy.
func WithForkPolicy(policy forkexec.Policy) Option {
	return func(wp *WorkPlan) { wp.ForkPolicy = policy }
}

// WithForkJoinPolicy configures automatic and explicit fork merge behavior.
func WithForkJoinPolicy(policy forkexec.JoinPolicy) Option {
	return func(wp *WorkPlan) { wp.ForkJoinPolicy = policy }
}

// WithMaxForkConcurrency configures automatic fork parallelism.
func WithMaxForkConcurrency(maxConcurrent int) Option {
	return func(wp *WorkPlan) { wp.MaxForkConcurrency = maxConcurrent }
}

// WithEventSink enables normalized root event delivery for this WorkPlan.
// Plan identity remains caller-owned and must be non-empty when the plan runs.
func WithEventSink(sink frameworkevent.Sink, planID string) Option {
	return func(wp *WorkPlan) {
		wp.EventConfig.Sink = sink
		wp.EventConfig.PlanID = planID
	}
}

// WithEventRunID fixes the execution identity used to correlate events.
func WithEventRunID(runID string) Option {
	return func(wp *WorkPlan) { wp.EventConfig.RunID = runID }
}

// WithEventHeartbeatPolicy enables shared heartbeat events for active nodes.
func WithEventHeartbeatPolicy(policy frameworkevent.HeartbeatPolicy) Option {
	return func(wp *WorkPlan) { wp.EventConfig.HeartbeatPolicy = policy }
}

// WithEventErrorHandler receives event Sink failures without changing plan
// execution behavior.
func WithEventErrorHandler(handler frameworkevent.ErrorHandler) Option {
	return func(wp *WorkPlan) { wp.EventConfig.ErrorHandler = handler }
}

// WithEventLocators appends agent, workplan, or product locators to every
// event emitted for this plan run.
func WithEventLocators(locators ...frameworkevent.Locator) Option {
	return func(wp *WorkPlan) {
		wp.EventConfig.Locators = append(wp.EventConfig.Locators, locators...)
	}
}

// New creates a new WorkPlan with the given AgentFactory.
func New(factory node.AgentFactory, opts ...Option) *WorkPlan {
	return NewFromPlan(coreplan.New(), factory, opts...)
}

// NewFromPlan assembles the WorkPlan facade around an executable Plan kernel.
// The graph field remains an editing view and does not own execution state.
func NewFromPlan(p *coreplan.Plan, factory node.AgentFactory, opts ...Option) *WorkPlan {
	if p == nil {
		p = coreplan.New()
	}
	wp := &WorkPlan{
		plan:    p,
		runner:  runner.New(p),
		factory: factory,
	}
	for _, o := range opts {
		o(wp)
	}
	return wp
}

// Plan returns the executable WorkPlan kernel.
func (wp *WorkPlan) Plan() *coreplan.Plan { return wp.plan }

// ─── Auto / Step ─────────────────────────────────────────────────────

// Auto adds an agent (Auto) strategy node with auto-linking.
func (wp *WorkPlan) Auto(id, input string) *WorkPlan {
	sauto.Add(wp.plan, id, input, wp.factory)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// Step is an alias for Auto with auto-linking.
func (wp *WorkPlan) Step(id, input string) *WorkPlan {
	return wp.Auto(id, input)
}

// Method adds a Go function node with auto-linking.
func (wp *WorkPlan) Method(id string, fn func(ctx context.Context, input string) (string, error)) *WorkPlan {
	sauto.AddMethod(wp.plan, id, fn)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// LLM adds a pure LLM call node with auto-linking.
func (wp *WorkPlan) LLM(id, input string, provider node.LLMProvider) *WorkPlan {
	sauto.AddLLM(wp.plan, id, input, provider)
	wp.autoLink(wp.plan.GetNode(id))
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
	sig := sloop.Add(wp.plan, id, bodyID, wp.factory, opts...)
	wp.autoLink(wp.plan.GetNode(id))
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
	forkNode := fork.Add(wp.plan, id, branches, maxConcurrent, wp.factory)
	forkNode.SetPolicy(wp.ForkPolicy)
	forkNode.SetJoinPolicy(wp.ForkJoinPolicy)
	if wp.BranchRuntimeFor != nil {
		forkNode.SetRuntimeResolver(func(branch node.ForkBranch) forkexec.BranchRuntime {
			return wp.BranchRuntimeFor(branch.Label)
		})
	}
	forkNode.OnEvent = wp.BranchEventHook
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// ─── If / Switch ─────────────────────────────────────────────────────

// If adds a binary conditional branch node with auto-linking.
func (wp *WorkPlan) If(id string, cond func(string) bool, trueID, falseID string) *WorkPlan {
	sw.If(wp.plan, id, cond, trueID, falseID)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// Switch adds a multi-way conditional branch node with auto-linking.
func (wp *WorkPlan) Switch(id string, cases ...node.SwitchCase) *WorkPlan {
	sw.Switch(wp.plan, id, cases...)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// ─── Approve / Gate ──────────────────────────────────────────────────

// Approve adds an approval pause node with auto-linking.
func (wp *WorkPlan) Approve(id, input string, gate approve.ApprovalGate, opts ...func(*approve.ApproveNode)) *WorkPlan {
	approve.Add(wp.plan, id, input, gate, wp.factory, opts...)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// Gate adds a simplified approval node (execute/abort only) with auto-linking.
func (wp *WorkPlan) Gate(id, content string) *WorkPlan {
	g := &AutoApproveGate{}
	approve.Add(wp.plan, id, content, g, wp.factory,
		approve.WithOptions(approve.Choices("execute", "abort")),
	)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// PendingQuestion returns the current approval question (for gate pauses).
func (wp *WorkPlan) PendingQuestion() *approve.Question {
	return wp.pendingGate
}

// ─── Emit ────────────────────────────────────────────────────────────

// Emit adds an emit node that writes PrevOutput to a named variable.
func (wp *WorkPlan) Emit(id, key string) *WorkPlan {
	emit.Add(wp.plan, id, key)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// ─── Checkpoint ──────────────────────────────────────────────────────

// Checkpoint adds a checkpoint/snapshot node with auto-linking.
func (wp *WorkPlan) Checkpoint(id string) *WorkPlan {
	scheckpoint.Add(wp.plan, id)
	wp.autoLink(wp.plan.GetNode(id))
	return wp
}

// ─── Execution ───────────────────────────────────────────────────────

// ExecState returns the current execution state.
func (wp *WorkPlan) ExecState() ExecState { return StateNotStarted }

// Vars returns the plan-level variables.
func (wp *WorkPlan) Vars() map[string]string { return make(map[string]string) }

// Run validates and executes the workflow graph.
func (wp *WorkPlan) Run(ctx context.Context) (*types.WorkPlanResult, error) {
	wp.runner.SetEventConfig(wp.EventConfig)
	if wp.NodeHook != nil {
		wp.runner.SetNodeHook(wp.NodeHook)
	}
	wp.runner.SetBranchEventHook(wp.BranchEventHook)
	wp.runner.SetBranchRuntimeResolver(wp.BranchRuntimeFor)
	wp.runner.SetForkPolicy(wp.ForkPolicy)
	wp.runner.SetForkJoinPolicy(wp.ForkJoinPolicy)
	wp.runner.SetMaxForkConcurrency(wp.MaxForkConcurrency)
	return wp.runner.Run(ctx)
}

// Resume continues execution from a saved checkpoint.
func (wp *WorkPlan) Resume(ctx context.Context, snapshotID string) (*types.WorkPlanResult, error) {
	wp.runner.SetEventConfig(wp.EventConfig)
	return wp.runner.Resume(ctx, snapshotID)
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
		wp.plan.SetEntry(n.ID())
		return
	}
	if wp.lastNodeID == "" {
		wp.lastNodeID = n.ID()
		return
	}
	prev := wp.plan.GetNode(wp.lastNodeID)
	if prev == nil {
		wp.lastNodeID = n.ID()
		return
	}
	kind := node.KindMethod
	if described, ok := prev.(node.Kinded); ok {
		kind = described.Kind()
	}
	switch kind {
	case node.KindIf, node.KindSwitch:
		// Conditional edges are added by If/Switch builders
	case node.KindFork, node.KindLoop:
		// Fork/Loop edges may already be set; only add if no outgoing edges
		edges := wp.plan.GetEdgesFrom(wp.lastNodeID)
		if len(edges) == 0 {
			wp.plan.AddEdge(edge.Edge{From: wp.lastNodeID, To: n.ID()})
		}
	default:
		edges := wp.plan.GetEdgesFrom(wp.lastNodeID)
		if len(edges) == 0 {
			wp.plan.AddEdge(edge.Edge{From: wp.lastNodeID, To: n.ID()})
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
	StateNotStarted ExecState = iota
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
