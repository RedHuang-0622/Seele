package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
	"github.com/RedHuang-0622/Seele/workplan/sugar/auto"
)

type testNode struct {
	node.BaseNode
	runFn func(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

type runtimeFactory struct{ output string }

func (f runtimeFactory) NewAgent(string) node.Agent { return runtimeAgent{output: f.output} }

type runtimeAgent struct{ output string }

func (a runtimeAgent) Chat(context.Context, string) (string, error) { return a.output, nil }

func newTestNode(id string, kind node.NodeKind) *testNode {
	return &testNode{BaseNode: node.NewBaseNode(id, kind)}
}

func (n *testNode) Run(ctx context.Context, wc *types.WorkflowContext) (string, error) {
	if n.runFn != nil {
		return n.runFn(ctx, wc)
	}
	return "", nil
}

func TestNew(t *testing.T) {
	g := graph.New()
	exec := executor.New()
	s := New(g.Plan(), exec)
	if s == nil {
		t.Fatal("New() returned nil")
	}
	if s.plan != g.Plan() {
		t.Error("plan not set")
	}
	if s.executor != exec {
		t.Error("executor not set")
	}
	if s.MaxForkConcurrency != 3 {
		t.Errorf("MaxForkConcurrency = %d, want 3", s.MaxForkConcurrency)
	}
	s.SetMaxForkConcurrency(2)
	if s.MaxForkConcurrency != 2 {
		t.Errorf("MaxForkConcurrency = %d, want 2", s.MaxForkConcurrency)
	}
	s.SetMaxForkConcurrency(0)
	if s.MaxForkConcurrency != 3 {
		t.Errorf("MaxForkConcurrency = %d after reset, want 3", s.MaxForkConcurrency)
	}
}

func TestRunBasicLinearExecution(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("mid", node.KindMethod))
	g.AddNode(newTestNode("end", node.KindMethod))
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "mid"})
	g.AddEdge(edge.Edge{From: "mid", To: "end"})

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 3 {
		t.Fatalf("expected 3 node results, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
	if result.NodeResults[1].NodeID != "mid" {
		t.Errorf("expected second node 'mid', got %q", result.NodeResults[1].NodeID)
	}
	if result.NodeResults[2].NodeID != "end" {
		t.Errorf("expected third node 'end', got %q", result.NodeResults[2].NodeID)
	}
	if result.Aborted {
		t.Error("expected Aborted to be false")
	}
}

func TestRunWithSingleNode(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("only", node.KindMethod))
	g.SetEntry("only")

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(result.NodeResults) != 1 {
		t.Fatalf("expected 1 node result, got %d", len(result.NodeResults))
	}
	if result.NodeResults[0].NodeID != "only" {
		t.Errorf("expected node 'only', got %q", result.NodeResults[0].NodeID)
	}
}

func TestRunWithBranchConditionalEdge(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("branchA", node.KindMethod))
	g.AddNode(newTestNode("branchB", node.KindMethod))
	g.SetEntry("start")

	trueCond := func(wc *types.WorkflowContext) bool { return true }
	falseCond := func(wc *types.WorkflowContext) bool { return false }
	g.AddEdge(edge.Edge{From: "start", To: "branchA", Condition: trueCond})
	g.AddEdge(edge.Edge{From: "start", To: "branchB", Condition: falseCond})

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Only branchA should execute (true condition), branchB should be skipped
	if len(result.NodeResults) != 2 {
		t.Fatalf("expected 2 node results (start + branchA), got %d", len(result.NodeResults))
	}
	if result.NodeResults[1].NodeID != "branchA" {
		t.Errorf("expected second node 'branchA', got %q", result.NodeResults[1].NodeID)
	}
}

func TestRunWithFork(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("b1", node.KindMethod))
	g.AddNode(newTestNode("b2", node.KindMethod))
	g.AddNode(newTestNode("merge", node.KindMethod))
	g.SetEntry("start")

	// Unconditional fork: start -> {b1, b2}
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	// Convergence: b1 -> merge, b2 -> merge
	g.AddEdge(edge.Edge{From: "b1", To: "merge"})
	g.AddEdge(edge.Edge{From: "b2", To: "merge"})

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	// Should execute: start, b1, b2, merge (4 nodes)
	if len(result.NodeResults) != 4 {
		t.Fatalf("expected 4 node results (start, b1, b2, merge), got %d", len(result.NodeResults))
	}
	// First node should always be "start"
	if result.NodeResults[0].NodeID != "start" {
		t.Errorf("expected first node 'start', got %q", result.NodeResults[0].NodeID)
	}
}

func TestForkJoinContextInheritance(t *testing.T) {
	type branchSnapshot struct {
		prevOutput  string
		startResult string
		variable    string
		metadata    string
	}

	g := graph.New()
	branchSnapshots := make(chan branchSnapshot, 2)
	var joinOutput string
	var joinResults map[string]string
	var joinVars map[string]string
	var joinMetadata string

	start := newTestNode("start", node.KindMethod)
	start.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
		wc.Vars["scope"] = `"parent"`
		wc.Metadata["scope"] = "parent"
		return "parent-output", nil
	}

	newBranch := func(id, output string) *testNode {
		branch := newTestNode(id, node.KindMethod)
		branch.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
			branchSnapshots <- branchSnapshot{
				prevOutput:  wc.PrevOutput,
				startResult: wc.PrevResults["start"],
				variable:    wc.Vars["scope"],
				metadata:    wc.Metadata["scope"].(string),
			}
			return output, nil
		}
		return branch
	}

	merge := newTestNode("merge", node.KindMethod)
	merge.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
		joinOutput = wc.PrevOutput
		joinResults = make(map[string]string, len(wc.PrevResults))
		for key, value := range wc.PrevResults {
			joinResults[key] = value
		}
		joinVars = make(map[string]string, len(wc.Vars))
		for key, value := range wc.Vars {
			joinVars[key] = value
		}
		joinMetadata = wc.Metadata["scope"].(string)
		return "joined", nil
	}

	g.AddNode(start)
	g.AddNode(newBranch("b1", "branch-one"))
	g.AddNode(newBranch("b2", "branch-two"))
	g.AddNode(merge)
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	g.AddEdge(edge.Edge{From: "b1", To: "merge"})
	g.AddEdge(edge.Edge{From: "b2", To: "merge"})

	result, err := New(g.Plan(), executor.New()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	for range 2 {
		snapshot := <-branchSnapshots
		if snapshot.prevOutput != types.ToJSON("parent-output") {
			t.Errorf("branch PrevOutput = %q, want parent output", snapshot.prevOutput)
		}
		if snapshot.startResult != types.ToJSON("parent-output") {
			t.Errorf("branch PrevResults[start] = %q, want parent output", snapshot.startResult)
		}
		if snapshot.variable != `"parent"` {
			t.Errorf("branch Vars[scope] = %q, want parent value", snapshot.variable)
		}
		if snapshot.metadata != "parent" {
			t.Errorf("branch Metadata[scope] = %q, want parent value", snapshot.metadata)
		}
	}

	var aggregate map[string]string
	if err := json.Unmarshal([]byte(joinOutput), &aggregate); err != nil {
		t.Fatalf("join PrevOutput is not aggregate JSON: %v", err)
	}
	if aggregate["b1"] != "branch-one" || aggregate["b2"] != "branch-two" {
		t.Errorf("join aggregate = %#v, want both branch outputs", aggregate)
	}
	if joinResults["start"] != types.ToJSON("parent-output") {
		t.Errorf("join PrevResults[start] = %q, want parent output", joinResults["start"])
	}
	if joinResults["b1"] != types.ToJSON("branch-one") || joinResults["b2"] != types.ToJSON("branch-two") {
		t.Errorf("join PrevResults = %#v, want both branch outputs", joinResults)
	}
	if joinVars["scope"] != `"parent"` || joinMetadata != "parent" {
		t.Errorf("join did not preserve parent context: Vars=%#v Metadata=%q", joinVars, joinMetadata)
	}
	if result.FinalOutputString() != "joined" {
		t.Errorf("final output = %q, want joined", result.FinalOutputString())
	}
}

func TestSchedulerForkRespectsMaxForkConcurrency(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})

	g := graph.New()
	start := newTestNode("start", node.KindMethod)
	newBranch := func(id string) *testNode {
		branch := newTestNode(id, node.KindMethod)
		branch.runFn = func(_ context.Context, _ *types.WorkflowContext) (string, error) {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				maximumSeen := maximum.Load()
				if current <= maximumSeen || maximum.CompareAndSwap(maximumSeen, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			return id, nil
		}
		return branch
	}

	g.AddNode(start)
	for _, id := range []string{"b1", "b2", "b3", "b4"} {
		g.AddNode(newBranch(id))
		g.AddEdge(edge.Edge{From: "start", To: id})
	}
	g.SetEntry("start")

	scheduler := New(g.Plan(), executor.New())
	scheduler.SetMaxForkConcurrency(2)
	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Run(context.Background())
		done <- err
	}()

	for range 2 {
		<-started
	}
	if current := active.Load(); current != 2 {
		t.Fatalf("active automatic fork branches = %d, want 2", current)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if maximumSeen := maximum.Load(); maximumSeen != 2 {
		t.Errorf("maximum automatic fork branches = %d, want 2", maximumSeen)
	}
	if current := active.Load(); current != 0 {
		t.Errorf("active automatic fork branches after Run = %d, want 0", current)
	}
}

func TestSchedulerForkFailFastCancelsSiblings(t *testing.T) {
	g := graph.New()
	failureReady := make(chan struct{}, 1)
	siblingReady := make(chan struct{}, 1)
	releaseFailure := make(chan struct{})
	var siblingCanceled atomic.Bool
	var mergeRan atomic.Bool

	start := newTestNode("start", node.KindMethod)
	failingBranch := newTestNode("b1", node.KindMethod)
	failingBranch.runFn = func(_ context.Context, _ *types.WorkflowContext) (string, error) {
		failureReady <- struct{}{}
		<-releaseFailure
		return "", errors.New("branch failed")
	}
	siblingBranch := newTestNode("b2", node.KindMethod)
	siblingBranch.runFn = func(ctx context.Context, _ *types.WorkflowContext) (string, error) {
		siblingReady <- struct{}{}
		<-ctx.Done()
		siblingCanceled.Store(true)
		return "", ctx.Err()
	}
	merge := newTestNode("merge", node.KindMethod)
	merge.runFn = func(_ context.Context, _ *types.WorkflowContext) (string, error) {
		mergeRan.Store(true)
		return "merged", nil
	}

	g.AddNode(start)
	g.AddNode(failingBranch)
	g.AddNode(siblingBranch)
	g.AddNode(merge)
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	g.AddEdge(edge.Edge{From: "b1", To: "merge"})
	g.AddEdge(edge.Edge{From: "b2", To: "merge"})

	done := make(chan error, 1)
	go func() {
		_, err := New(g.Plan(), executor.New()).Run(context.Background())
		done <- err
	}()

	<-failureReady
	<-siblingReady
	close(releaseFailure)
	if err := <-done; err == nil {
		t.Fatal("Run() error = nil, want fork failure")
	}
	if !siblingCanceled.Load() {
		t.Error("failing branch did not cancel its sibling")
	}
	if mergeRan.Load() {
		t.Error("join node ran after a fork branch failed")
	}
}

func TestForkCreatesIndependentBranchContexts(t *testing.T) {
	g := graph.New()
	branchContexts := make(chan *types.WorkflowContext, 2)
	var mergeContext *types.WorkflowContext

	start := newTestNode("start", node.KindMethod)
	start.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
		wc.PrevResults["parent"] = `"result"`
		wc.Vars["scope"] = `"parent"`
		wc.Metadata["nested"] = map[string]any{"value": "parent"}
		wc.Result.Checkpoints["parent"] = `"checkpoint"`
		return "parent", nil
	}

	newBranch := func(id string) *testNode {
		branch := newTestNode(id, node.KindMethod)
		branch.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
			branchContexts <- wc
			return id, nil
		}
		return branch
	}

	merge := newTestNode("merge", node.KindMethod)
	merge.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
		mergeContext = wc
		return "merged", nil
	}

	g.AddNode(start)
	g.AddNode(newBranch("b1"))
	g.AddNode(newBranch("b2"))
	g.AddNode(merge)
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	g.AddEdge(edge.Edge{From: "b1", To: "merge"})
	g.AddEdge(edge.Edge{From: "b2", To: "merge"})

	if _, err := New(g.Plan(), executor.New()).Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	firstBranch := <-branchContexts
	secondBranch := <-branchContexts
	if firstBranch == secondBranch {
		t.Fatal("fork branches received the same WorkflowContext pointer")
	}
	if mergeContext == nil {
		t.Fatal("merge node did not receive a workflow context")
	}
	if firstBranch == mergeContext || secondBranch == mergeContext {
		t.Fatal("fork branch received the parent WorkflowContext pointer")
	}

	firstBranch.Vars["branch"] = `"mutated"`
	firstBranch.Metadata["nested"].(map[string]any)["value"] = "mutated"
	firstBranch.Result.Checkpoints["branch"] = `"mutated"`

	if _, ok := secondBranch.Vars["branch"]; ok {
		t.Error("mutating one branch Vars polluted its sibling")
	}
	if secondBranch.Metadata["nested"].(map[string]any)["value"] != "parent" {
		t.Error("mutating one branch Metadata polluted its sibling")
	}
	if _, ok := secondBranch.Result.Checkpoints["branch"]; ok {
		t.Error("mutating one branch Result polluted its sibling")
	}
	if _, ok := mergeContext.Vars["branch"]; ok {
		t.Error("mutating a branch Vars polluted the parent context")
	}
	if mergeContext.Metadata["nested"].(map[string]any)["value"] != "parent" {
		t.Error("mutating a branch Metadata polluted the parent context")
	}
	if _, ok := mergeContext.Result.Checkpoints["branch"]; ok {
		t.Error("mutating a branch Result polluted the parent context")
	}
}

func TestRunWithForkDivergent(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.AddNode(newTestNode("b1", node.KindMethod))
	g.AddNode(newTestNode("b2", node.KindMethod))
	g.AddNode(newTestNode("end1", node.KindMethod))
	g.AddNode(newTestNode("end2", node.KindMethod))
	g.SetEntry("start")

	// Unconditional fork: start -> {b1, b2}
	g.AddEdge(edge.Edge{From: "start", To: "b1"})
	g.AddEdge(edge.Edge{From: "start", To: "b2"})
	// b1 -> end1, b2 -> end2 (different targets 鈥?divergent)
	g.AddEdge(edge.Edge{From: "b1", To: "end1"})
	g.AddEdge(edge.Edge{From: "b2", To: "end2"})

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(result.NodeResults) != 5 {
		t.Errorf("expected all divergent branch nodes to run, got %d results", len(result.NodeResults))
	}
}

func TestNestedForkDependencyJoin(t *testing.T) {
	g := graph.New()
	var reviewContext *types.WorkflowContext
	start := newTestNode("start", node.KindMethod)
	backend := newTestNode("backend", node.KindMethod)
	tests := newTestNode("tests", node.KindMethod)
	backendCheck := newTestNode("backend-check", node.KindMethod)
	testsCheck := newTestNode("tests-check", node.KindMethod)
	review := newTestNode("review", node.KindMethod)
	review.runFn = func(_ context.Context, wc *types.WorkflowContext) (string, error) {
		reviewContext = wc
		return "reviewed", nil
	}
	for _, n := range []*testNode{start, backend, tests, backendCheck, testsCheck, review} {
		g.AddNode(n)
	}
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "backend"})
	g.AddEdge(edge.Edge{From: "start", To: "tests"})
	g.AddEdge(edge.Edge{From: "backend", To: "backend-check"})
	g.AddEdge(edge.Edge{From: "tests", To: "tests-check"})
	g.AddEdge(edge.Edge{From: "backend-check", To: "review"})
	g.AddEdge(edge.Edge{From: "tests-check", To: "review"})

	result, err := New(g.Plan(), executor.New()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if len(result.NodeResults) != 6 {
		t.Fatalf("node results = %d, want 6", len(result.NodeResults))
	}
	if reviewContext == nil {
		t.Fatal("review did not run after both branch paths completed")
	}
	if _, ok := reviewContext.PrevResults["backend-check"]; !ok {
		t.Error("review context is missing backend-check output")
	}
	if _, ok := reviewContext.PrevResults["tests-check"]; !ok {
		t.Error("review context is missing tests-check output")
	}
}

func TestAutomaticForkUsesBranchBoundAgentFactory(t *testing.T) {
	g := graph.New()
	start := newTestNode("start", node.KindMethod)
	start.runFn = func(context.Context, *types.WorkflowContext) (string, error) { return "start", nil }
	g.AddNode(start)
	auto.Add(g, "left", "left input", runtimeFactory{output: "shared-left"})
	auto.Add(g, "right", "right input", runtimeFactory{output: "shared-right"})
	g.SetEntry("start")
	g.AddEdge(edge.Edge{From: "start", To: "left"})
	g.AddEdge(edge.Edge{From: "start", To: "right"})

	s := New(g.Plan(), executor.New())
	s.SetBranchRuntimeResolver(func(branchID string) forkexec.BranchRuntime {
		return forkexec.BranchRuntime{AgentFactory: runtimeFactory{output: branchID + "-runtime"}}
	})
	result, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	outputs := make(map[string]string)
	for _, nodeResult := range result.NodeResults {
		outputs[nodeResult.NodeID] = types.FromJSON(nodeResult.Output)
	}
	if outputs["left"] != "left-runtime" || outputs["right"] != "right-runtime" {
		t.Errorf("automatic fork outputs = %#v, want branch-bound factories", outputs)
	}
}

func TestRunNodeNotFound(t *testing.T) {
	g := graph.New()
	g.SetEntry("nonexistent")

	exec := executor.New()
	s := New(g.Plan(), exec)

	_, err := s.Run(context.Background())
	if err == nil {
		t.Fatal("expected error for missing entry node, got nil")
	}
}

func TestRunContextCancellation(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("start", node.KindMethod))
	g.SetEntry("start")

	exec := executor.New()
	s := New(g.Plan(), exec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediately cancel

	result, err := s.Run(ctx)
	if err != nil {
		t.Fatalf("Run should handle cancellation gracefully: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Aborted {
		t.Error("expected Aborted to be true after cancellation")
	}
}

func TestRunWithCheckpointCreatesSnapshots(t *testing.T) {
	g := graph.New()
	g.AddNode(newTestNode("a", node.KindMethod))
	g.AddNode(newTestNode("b", node.KindMethod))
	g.SetEntry("a")
	g.AddEdge(edge.Edge{From: "a", To: "b"})

	exec := executor.New()
	s := New(g.Plan(), exec)

	result, checkpoints, err := s.RunWithCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("RunWithCheckpoint failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.NodeResults) != 2 {
		t.Errorf("expected 2 node results, got %d", len(result.NodeResults))
	}
	if len(checkpoints) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(checkpoints))
	}
	if _, ok := checkpoints["a"]; !ok {
		t.Error("expected checkpoint for node 'a'")
	}
	if _, ok := checkpoints["b"]; !ok {
		t.Error("expected checkpoint for node 'b'")
	}
	if checkpoints["a"].Status != types.StatusRunning {
		t.Errorf("expected StatusRunning, got %v", checkpoints["a"].Status)
	}
	if checkpoints["a"].NodeID != "a" {
		t.Errorf("expected NodeID 'a', got %q", checkpoints["a"].NodeID)
	}
	if checkpoints["a"].Context == nil {
		t.Error("expected non-nil Context in checkpoint")
	}
}
