package workplan

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/sugar/loop"
)

// ─── Mock Agent for testing ──────────────────────────────────────────

type mockAgent struct {
	responses []string
	idx       int
}

func (a *mockAgent) Chat(ctx context.Context, input string) (string, error) {
	if a.idx < len(a.responses) {
		r := a.responses[a.idx]
		a.idx++
		return types.ToJSON(r), nil
	}
	return types.ToJSON("mock response"), nil
}

type mockFactory struct {
	responses []string
	mu        sync.Mutex
	idx       int
}

func (f *mockFactory) NewAgent(systemPrompt string) node.Agent {
	return &sharedAgent{responses: f.responses, idx: &f.idx, mu: &f.mu}
}

// sharedAgent shares a response index across all instances created by the same factory
type sharedAgent struct {
	responses []string
	idx       *int
	mu        *sync.Mutex
}

func (a *sharedAgent) Chat(ctx context.Context, input string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if *a.idx < len(a.responses) {
		r := a.responses[*a.idx]
		*a.idx++
		return types.ToJSON(r), nil
	}
	return types.ToJSON("mock response"), nil
}

// ─── Tests ───────────────────────────────────────────────────────────

func TestWorkPlan_Auto_Chain(t *testing.T) {
	factory := &mockFactory{responses: []string{"output1", "output2"}}
	wp := New(factory, WithDefaultPrompt("test assistant"))

	wp.Auto("step1", "first step")
	wp.Auto("step2", "second step")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Aborted {
		t.Fatal("unexpected abort")
	}
	final := result.FinalOutputString()
	t.Logf("final output: %s", final)
	if final == "" {
		t.Error("expected non-empty output")
	}
	if len(result.NodeResults) != 2 {
		t.Errorf("expected 2 node results, got %d", len(result.NodeResults))
	}
}

func TestWorkPlan_EmptyGraph(t *testing.T) {
	factory := &mockFactory{}
	wp := New(factory)
	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run empty graph: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestWorkPlan_Loop(t *testing.T) {
	factory := &mockFactory{
		responses: []string{"仍在运行", "仍在运行", "仍在运行", "已恢复正常"},
	}
	wp := New(factory)

	wp.Auto("body", "修复: {{.PrevResult}}")

	sig := wp.Loop("loop", "body",
		Until(func(s string) bool { return strings.Contains(s, "已恢复正常") }),
		MaxIter(5),
	)

	wp.Auto("done", "完成")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	t.Run("signal_iterations", func(t *testing.T) {
		if sig.Iter() != 3 {
			t.Errorf("expected 3 iterations, got %d", sig.Iter())
		}
	})

	t.Run("signal_final_value", func(t *testing.T) {
		final := sig.GetString()
		if !strings.Contains(final, "已恢复正常") {
			t.Errorf("expected '已恢复正常' in signal, got: %s", final)
		}
	})

	t.Run("result_completed", func(t *testing.T) {
		if result.Aborted {
			t.Error("unexpected abort")
		}
	})
}

func TestWorkPlan_Fork(t *testing.T) {
	factory := &mockFactory{
		responses: []string{"branch_a_result", "branch_b_result"},
	}
	wp := New(factory)

	wp.Fork("fork", []node.ForkBranch{
		{Label: "A", SystemPrompt: "agent A", Input: "task A"},
		{Label: "B", SystemPrompt: "agent B", Input: "task B"},
	}, 2)

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("fork result: %s", result.FinalOutput())
	if len(result.NodeResults) != 1 {
		t.Errorf("expected 1 node result (fork), got %d", len(result.NodeResults))
	}
}

func TestWorkPlan_Emit(t *testing.T) {
	factory := &mockFactory{
		responses: []string{`"分析结果: 系统存在内存泄漏"`},
	}
	wp := New(factory)

	wp.Auto("analyze", "分析系统")
	wp.Emit("save", "root_cause")
	wp.Auto("fix", "修复: {{.Vars.root_cause}}")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Vars == nil {
		t.Fatal("expected vars map")
	}
	val, ok := result.Vars["root_cause"]
	if !ok || val == "" {
		t.Errorf("emit var 'root_cause' not found or empty: ok=%v val=%q", ok, val)
	}
	t.Logf("emit var root_cause = %s", val)
}

func TestWorkPlan_If(t *testing.T) {
	factory := &mockFactory{
		responses: []string{`"系统异常: CPU过高"`},
	}
	wp := New(factory)

	wp.Auto("detect", "检测系统")
	wp.If("check", func(s string) bool { return strings.Contains(s, "异常") }, "alert", "normal")
	wp.Auto("alert", "发出告警")
	wp.Auto("normal", "一切正常")

	result, err := wp.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("node results: %d", len(result.NodeResults))
	for _, nr := range result.NodeResults {
		t.Logf("  %s", nr.NodeID)
	}
	foundAlert := false
	for _, nr := range result.NodeResults {
		if nr.NodeID == "alert" {
			foundAlert = true
		}
	}
	if !foundAlert {
		t.Error("expected 'alert' path to be taken (output contains '异常')")
	}
}

func TestWorkPlan_ExportJSON(t *testing.T) {
	factory := &mockFactory{}
	wp := New(factory)

	wp.Auto("step1", "first")
	wp.Auto("step2", "second")

	json, err := wp.ExportJSON()
	if err != nil {
		t.Fatalf("ExportJSON: %v", err)
	}
	if json == "" {
		t.Fatal("expected non-empty JSON")
	}
	t.Logf("exported plan: %s", json)
}

// ─── Compile-time interface checks ──────────────────────────────────

var _ types.EdgeCondition = nil
var _ *loop.Signal = nil
