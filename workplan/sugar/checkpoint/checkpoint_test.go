package checkpoint

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

func TestNewNode(t *testing.T) {
	n := NewNode("cp-1")
	if n == nil {
		t.Fatal("NewNode() returned nil")
	}
	if n.ID() != "cp-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "cp-1")
	}
	if n.Kind() != node.KindCheckpoint {
		t.Errorf("Kind() = %v, want %v", n.Kind(), node.KindCheckpoint)
	}
}

func TestAdd(t *testing.T) {
	g := graph.New()
	n := Add(g, "checkpoint-1")
	if n == nil {
		t.Fatal("Add() returned nil")
	}
	if n.ID() != "checkpoint-1" {
		t.Errorf("ID() = %q, want %q", n.ID(), "checkpoint-1")
	}
	if got := g.GetNode("checkpoint-1"); got == nil {
		t.Error("checkpoint-1 not found in graph")
	}
}

func TestAdd_ReturnsCorrectType(t *testing.T) {
	g := graph.New()
	n := Add(g, "my-cp")
	if _, ok := interface{}(n).(*CheckpointNode); !ok {
		t.Error("Add() should return *CheckpointNode")
	}
}

func TestRun_Passthrough(t *testing.T) {
	n := NewNode("cp-run")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"data"`

	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != `"data"` {
		t.Errorf("Run() = %q, want %q", result, `"data"`)
	}
}

func TestRun_WritesCheckpoint(t *testing.T) {
	n := NewNode("cp-writer")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"snapshot-value"`

	n.Run(context.Background(), wc)

	if cp, ok := wc.Result.Checkpoints["cp-writer"]; !ok {
		t.Error("checkpoint not written to Result")
	} else if cp != `"snapshot-value"` {
		t.Errorf("checkpoint = %q, want %q", cp, `"snapshot-value"`)
	}
}

func TestRun_NilCheckpoints(t *testing.T) {
	n := NewNode("cp-nil")
	wc := types.NewWorkflowContext()
	wc.Result = &types.WorkPlanResult{}

	// Should not panic when Checkpoints map is nil
	result, err := n.Run(context.Background(), wc)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "" {
		t.Errorf("Run() = %q, want empty", result)
	}
}

func TestRun_NilResult(t *testing.T) {
	n := NewNode("cp-noresult")
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

func TestMultipleCheckpoints(t *testing.T) {
	g := graph.New()
	cp1 := Add(g, "cp-first")
	cp2 := Add(g, "cp-second")

	wc := types.NewWorkflowContext()

	wc.PrevOutput = `"first-value"`
	cp1.Run(context.Background(), wc)

	wc.PrevOutput = `"second-value"`
	cp2.Run(context.Background(), wc)

	if len(wc.Result.Checkpoints) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(wc.Result.Checkpoints))
	}
	if wc.Result.Checkpoints["cp-first"] != `"first-value"` {
		t.Errorf("cp-first = %q", wc.Result.Checkpoints["cp-first"])
	}
	if wc.Result.Checkpoints["cp-second"] != `"second-value"` {
		t.Errorf("cp-second = %q", wc.Result.Checkpoints["cp-second"])
	}
}

func TestGraphContainsCheckpoint(t *testing.T) {
	g := graph.New()
	Add(g, "checkpoint-node")

	nodes := g.AllNodes()
	found := false
	for _, id := range nodes {
		if id == "checkpoint-node" {
			found = true
			break
		}
	}
	if !found {
		t.Error("checkpoint-node not found via AllNodes")
	}
}
