package emit

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

func TestNewNode(t *testing.T) {
	n := NewNode("emit-1", "result-key")
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "emit-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "emit-1")
	}
	if n.Kind() != node.KindEmit {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindEmit)
	}
	if n.Key != "result-key" {
		t.Errorf("Key = %q, want %q", n.Key, "result-key")
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	n := Add(g, "emit-node", "myvar")
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if n.ID() != "emit-node" {
		t.Errorf("ID() = %q, want %q", n.ID(), "emit-node")
	}
	if n.Key != "myvar" {
		t.Errorf("Key = %q, want %q", n.Key, "myvar")
	}
	if got := g.GetNode("emit-node"); got == nil {
		t.Error("emit-node not found in graph")
	}
}

func TestAdd_ReturnsCorrectType(t *testing.T) {
	g := graph.New()
	n := Add(g, "my-emit", "key")
	if _, ok := interface{}(n).(*EmitNode); !ok {
		t.Error("Add() should return *EmitNode")
	}
}

func TestRun_WritesVar(t *testing.T) {
	n := NewNode("emit-write", "output")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"emitted-value"`

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Should pass through value
	if result != `"emitted-value"` {
		t.Errorf("Run() = %q, want %q", result, `"emitted-value"`)
	}

	// Should write to Vars
	if v, ok := wc.Vars["output"]; !ok {
		t.Error("Vars['output'] not set")
	} else if v != `"emitted-value"` {
		t.Errorf("Vars['output'] = %q, want %q", v, `"emitted-value"`)
	}
}

func TestRun_Passthrough(t *testing.T) {
	n := NewNode("emit-pass", "any")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"passthrough-data"`

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != `"passthrough-data"` {
		t.Errorf("Run() = %q, want %q", result, `"passthrough-data"`)
	}
}

func TestRun_NilVars(t *testing.T) {
	n := NewNode("emit-nil", "key")
	wc := types.NewWorkflowContext()
	wc.Vars = nil
	wc.Result = &types.WorkPlanResult{}

	// Should not panic when Vars is nil
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "" {
		t.Errorf("Run() = %q, want empty", result)
	}
}

func TestRun_NilResult(t *testing.T) {
	n := NewNode("emit-noresult", "key")
	wc := types.NewWorkflowContext()
	wc.Result = nil

	// Should not panic when Result is nil
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "" {
		t.Errorf("Run() = %q, want empty", result)
	}
}

func TestRun_UpdatesResultVars(t *testing.T) {
	n := NewNode("emit-res", "mykey")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"v"`

	n.Run(context.Background(), wc)

	if wc.Result.Vars == nil {
		t.Fatal("Result.Vars is nil")
	}
	if v, ok := wc.Result.Vars["mykey"]; !ok || v != `"v"` {
		t.Errorf("Result.Vars['mykey'] = %q, want %q", v, `"v"`)
	}
}

func TestMultipleEmits(t *testing.T) {
	g := graph.New()
	e1 := Add(g, "emit-first", "var1")
	e2 := Add(g, "emit-second", "var2")

	wc := types.NewWorkflowContext()

	wc.PrevOutput = `"v1"`
	e1.Run(context.Background(), wc)

	wc.PrevOutput = `"v2"`
	e2.Run(context.Background(), wc)

	if wc.Vars["var1"] != `"v1"` {
		t.Errorf("var1 = %q", wc.Vars["var1"])
	}
	if wc.Vars["var2"] != `"v2"` {
		t.Errorf("var2 = %q", wc.Vars["var2"])
	}
}

func TestGraphContainsEmitNode(t *testing.T) {
	g := graph.New()
	Add(g, "graph-emit", "mykey")

	nodes := g.AllNodes()
	found := false
	for _, id := range nodes {
		if id == "graph-emit" {
			found = true
			break
		}
	}
	if !found {
		t.Error("graph-emit not found via AllNodes")
	}
}
