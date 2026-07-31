package fork

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
	"go.uber.org/goleak"
)

// ── Mock types ─────────────────────────────────────────────────────────────

type mockAgent struct{}

func (m *mockAgent) Chat(_ context.Context, input string) (string, error) {
	return `"result:` + input + `"`, nil
}

type mockFactory struct{}

func (m *mockFactory) NewAgent(_ string) node.Agent {
	return &mockAgent{}
}

type taggedFactory struct{ tag string }

func (f taggedFactory) NewAgent(_ string) node.Agent {
	return taggedAgent{tag: f.tag}
}

type taggedAgent struct{ tag string }

func (a taggedAgent) Chat(context.Context, string) (string, error) {
	return a.tag, nil
}

type selectiveFactory struct{}

func (selectiveFactory) NewAgent(_ string) node.Agent { return selectiveAgent{} }

type selectiveAgent struct{}

func (selectiveAgent) Chat(_ context.Context, input string) (string, error) {
	if input == "fail" {
		return "", context.Canceled
	}
	return input, nil
}

type blockingFactory struct {
	started chan<- struct{}
	release <-chan struct{}
	current atomic.Int32
	maximum atomic.Int32
}

func (f *blockingFactory) NewAgent(_ string) node.Agent {
	return &blockingAgent{factory: f}
}

type blockingAgent struct {
	factory *blockingFactory
}

func (a *blockingAgent) Chat(ctx context.Context, _ string) (string, error) {
	current := a.factory.current.Add(1)
	defer a.factory.current.Add(-1)
	for {
		maximum := a.factory.maximum.Load()
		if current <= maximum || a.factory.maximum.CompareAndSwap(maximum, current) {
			break
		}
	}

	a.factory.started <- struct{}{}
	select {
	case <-a.factory.release:
		return `"done"`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ── Tests ──────────────────────────────────────────────────────────────────

func TestNewNode(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "branch-a", SystemPrompt: "You are A", Input: "input-a"},
	}
	n := NewNode("fork-1", branches, 2, &mockFactory{})
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "fork-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "fork-1")
	}
	if n.Kind() != node.KindFork {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindFork)
	}
	if len(n.Branches) != 1 {
		t.Errorf("Branches length = %d, want 1", len(n.Branches))
	}
	if n.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", n.MaxConcurrent)
	}
}

func TestNewNode_DefaultMaxConcurrent(t *testing.T) {
	n := NewNode("fork-default", nil, 0, &mockFactory{})
	if n.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (default)", n.MaxConcurrent)
	}
}

func TestNewNode_NegativeMaxConcurrent(t *testing.T) {
	n := NewNode("fork-neg", nil, -5, &mockFactory{})
	if n.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (default)", n.MaxConcurrent)
	}
}

func TestAdd(t *testing.T) {
	g := coreplan.New()
	branches := []node.ForkBranch{
		{Label: "research", SystemPrompt: "Research agent", Input: "data"},
		{Label: "write", SystemPrompt: "Write agent", Input: "data"},
	}
	n := Add(g, "fork-node", branches, 2, &mockFactory{})
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if n.ID() != "fork-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "fork-node")
	}
	if got := g.GetNode("fork-node"); got == nil {
		t.Error("fork-node not found in graph")
	}
}

func TestAdd_EmptyBranches(t *testing.T) {
	g := coreplan.New()
	n := Add(g, "empty-fork", []node.ForkBranch{}, 1, &mockFactory{})
	if n == nil {
		t.Fatal("Add() with empty branches returned nil")
	}
	if len(n.Branches) != 0 {
		t.Errorf("Branches length = %d, want 0", len(n.Branches))
	}
}

func TestRun_SingleBranch(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "single", Input: "hello"},
	}
	n := NewNode("single-fork", branches, 1, &mockFactory{})
	wc := types.NewWorkflowContext()

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "single") {
		t.Errorf("result = %q, should contain branch label 'single'", result)
	}
}

func TestRun_MultipleBranches(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "branch1", Input: "input1"},
		{Label: "branch2", Input: "input2"},
	}
	n := NewNode("multi-fork", branches, 2, &mockFactory{})
	wc := types.NewWorkflowContext()

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "branch1") || !strings.Contains(result, "branch2") {
		t.Errorf("result = %q, should contain both branch labels", result)
	}
}

func TestRun_ContextCancelled(t *testing.T) {
	branches := []node.ForkBranch{
		{Label: "slow1", Input: "data"},
	}
	n := NewNode("cancel-fork", branches, 1, &mockFactory{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := n.Run(ctx, types.NewWorkflowContext())
	if err != nil {
		t.Logf("Run with cancelled context returned: %v", err)
	}
}

func TestRun_AllBranchesSucceed(t *testing.T) {
	failFactory := &mockFactory{}
	n := NewNode("succeed-fork", []node.ForkBranch{
		{Label: "succeed1", Input: "x"},
	}, 1, failFactory)

	wc := types.NewWorkflowContext()
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() should not error when mock succeeds: %v", err)
	}
	if !strings.Contains(result, "succeed1") {
		t.Errorf("result = %q, should contain 'succeed1'", result)
	}
}

func TestRun_RespectsMaxConcurrentAndReleasesGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t)

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	factory := &blockingFactory{started: started, release: release}
	n := NewNode("limited-fork", []node.ForkBranch{
		{Label: "one"},
		{Label: "two"},
		{Label: "three"},
		{Label: "four"},
	}, 2, factory)

	done := make(chan error, 1)
	go func() {
		_, err := n.Run(context.Background(), types.NewWorkflowContext())
		done <- err
	}()

	for range 2 {
		<-started
	}
	if current := factory.current.Load(); current != 2 {
		t.Fatalf("active branches = %d, want 2 while semaphore is saturated", current)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if maximum := factory.maximum.Load(); maximum != 2 {
		t.Errorf("maximum active branches = %d, want 2", maximum)
	}
	if current := factory.current.Load(); current != 0 {
		t.Errorf("active branches after Run() = %d, want 0", current)
	}
}

func TestRun_UsesInjectedBranchRuntimeAndEmitsBranchEvents(t *testing.T) {
	events := make(chan forkexec.Event, 12)
	n := NewNode("runtime-fork", []node.ForkBranch{
		{Label: "left"},
		{Label: "right"},
	}, 2, taggedFactory{tag: "node-owned"})
	n.SetRuntimeResolver(func(branch node.ForkBranch) forkexec.BranchRuntime {
		return forkexec.BranchRuntime{Metadata: map[string]string{"branch": branch.Label}}
	})
	n.OnEvent = func(event forkexec.Event) { events <- event }

	output, err := n.Run(context.Background(), types.NewWorkflowContext())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "node-owned") {
		t.Errorf("output = %q, want node-owned agent outputs", output)
	}

	seen := make(map[string]map[forkexec.BranchState]bool)
	for len(events) > 0 {
		event := <-events
		if event.BranchID == "" {
			t.Error("event missing branch ID")
		}
		if seen[event.BranchID] == nil {
			seen[event.BranchID] = make(map[forkexec.BranchState]bool)
		}
		seen[event.BranchID][event.Type] = true
	}
	for _, branchID := range []string{"left", "right"} {
		for _, eventType := range []forkexec.BranchState{forkexec.StateQueued, forkexec.StateStarted, forkexec.StateCompleted} {
			if !seen[branchID][eventType] {
				t.Errorf("branch %q missing %q event", branchID, eventType)
			}
		}
	}
}

func TestRun_BestEffortJoinSuccessful(t *testing.T) {
	n := NewNode("best-effort", []node.ForkBranch{
		{Label: "good", Input: "good"},
		{Label: "failed", Input: "fail"},
	}, 2, selectiveFactory{})
	n.SetPolicy(forkexec.ForkPolicyBestEffort)
	n.SetJoinPolicy(forkexec.JoinSuccessful)

	workflow := types.NewWorkflowContext()
	output, err := n.Run(context.Background(), workflow)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output, "good") || !strings.Contains(output, "failed") {
		t.Errorf("aggregate output = %q, want both branch IDs", output)
	}
	if got := workflow.PrevResults["good"]; got == "" {
		t.Error("successful explicit fork result was not joined")
	}
	if _, found := workflow.PrevResults["failed"]; found {
		t.Error("failed best-effort branch was joined as successful")
	}
}

func TestGraphContainsForkNode(t *testing.T) {
	g := coreplan.New()
	branches := []node.ForkBranch{
		{Label: "a", Input: "in-a"},
		{Label: "b", Input: "in-b"},
	}
	Add(g, "graph-fork", branches, 3, &mockFactory{})

	nodes := g.AllNodes()
	found := false
	for _, id := range nodes {
		if id == "graph-fork" {
			found = true
			break
		}
	}
	if !found {
		t.Error("graph-fork not found via AllNodes")
	}
}
