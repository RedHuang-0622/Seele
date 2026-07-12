package switchpkg

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/graph"
)

type dummyNode struct {
	id string
}

func (d *dummyNode) ID() string { return d.id }
func (d *dummyNode) Kind() node.NodeKind { return node.KindMethod }
func (d *dummyNode) Run(_ context.Context, _ *types.WorkflowContext) (string, error) { return `""`, nil }

func addDummy(g *graph.Graph, id string) {
	g.AddNode(&dummyNode{id: id})
}

func TestNewNode(t *testing.T) {
	n := NewNode("ctrl-1", node.KindIf)
	if n == nil { t.Fatal("nil") }
	if n.ID() != "ctrl-1" { t.Errorf("ID = %q", n.ID()) }
}

func TestNewNode_KindSwitch(t *testing.T) {
	n := NewNode("switch-1", node.KindSwitch)
	if n.Kind() != node.KindSwitch { t.Errorf("kind = %v", n.Kind()) }
}

func TestRun_Passthrough(t *testing.T) {
	n := NewNode("ctrl-run", node.KindIf)
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"data"`
	r, e := n.Run(context.Background(), wc)
	if e != nil { t.Fatal(e) }
	if r != `"data"` { t.Errorf("got %q", r) }
}

func TestRun_EmptyOutput(t *testing.T) {
	n := NewNode("ctrl-empty", node.KindIf)
	r, e := n.Run(context.Background(), types.NewWorkflowContext())
	if e != nil { t.Fatal(e) }
	if r != "" { t.Errorf("got %q", r) }
}

func TestIf(t *testing.T) {
	g := graph.New()
	addDummy(g, "true-target")
	addDummy(g, "false-target")
	n := If(g, "if-node", func(s string) bool { return s == "yes" }, "true-target", "false-target")
	if n == nil { t.Fatal("nil") }
	if g.GetNode("if-node") == nil { t.Error("not found") }
}

func TestIf_EdgeCount(t *testing.T) {
	g := graph.New()
	addDummy(g, "y"); addDummy(g, "n")
	If(g, "if-ec", func(s string) bool { return true }, "y", "n")
	e := g.GetEdgesFrom("if-ec")
	if len(e) != 2 { t.Errorf("got %d edges", len(e)) }
}

func TestIf_EdgeCountWithoutFalse(t *testing.T) {
	g := graph.New()
	addDummy(g, "y")
	If(g, "if-nf", func(s string) bool { return true }, "y", "")
	e := g.GetEdgesFrom("if-nf")
	if len(e) != 1 { t.Errorf("got %d edges", len(e)) }
}

func TestIf_EdgeLabels(t *testing.T) {
	g := graph.New()
	addDummy(g, "t"); addDummy(g, "f")
	If(g, "if-el", func(s string) bool { return true }, "t", "f")
	edges := g.GetEdgesFrom("if-el")
	m := map[string]string{}
	for _, e := range edges { m[e.Label] = e.To }
	if m["true"] != "t" { t.Errorf("true label: %v", m) }
	if m["false"] != "f" { t.Errorf("false label: %v", m) }
}

func TestIf_ConditionEvaluation(t *testing.T) {
	g := graph.New()
	addDummy(g, "pos"); addDummy(g, "neg")
	If(g, "if-ce", func(s string) bool { return s == "positive" }, "pos", "neg")
	edges := g.GetEdgesFrom("if-ce")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"positive"`
	for _, e := range edges {
		if e.Condition != nil {
			if e.Label == "true" && !e.Condition(wc) { t.Error("true cond should match") }
			if e.Label == "false" && e.Condition(wc) { t.Error("false cond should not match") }
		}
	}
}

func TestSwitch(t *testing.T) {
	g := graph.New()
	addDummy(g, "a"); addDummy(g, "b"); addDummy(g, "d")
	n := Switch(g, "sw",
		node.SwitchCase{Match: func(s string) bool { return s == "a" }, NextID: "a"},
		node.SwitchCase{Match: func(s string) bool { return s == "b" }, NextID: "b"},
		node.SwitchCase{Match: nil, NextID: "d"},
	)
	if n == nil { t.Fatal("nil") }
	if g.GetNode("sw") == nil { t.Error("not found") }
}

func TestSwitch_EdgeCount(t *testing.T) {
	g := graph.New()
	addDummy(g, "a"); addDummy(g, "b"); addDummy(g, "c")
	Switch(g, "sw3",
		node.SwitchCase{Match: func(s string) bool { return false }, NextID: "a"},
		node.SwitchCase{Match: func(s string) bool { return false }, NextID: "b"},
		node.SwitchCase{Match: nil, NextID: "c"},
	)
	if len(g.GetEdgesFrom("sw3")) != 3 { t.Error("expected 3 edges") }
}

func TestSwitch_DefaultCase(t *testing.T) {
	g := graph.New()
	addDummy(g, "m"); addDummy(g, "d")
	Switch(g, "swd",
		node.SwitchCase{Match: func(s string) bool { return s == "specific" }, NextID: "m"},
		node.SwitchCase{Match: nil, NextID: "d"},
	)
	edges := g.GetEdgesFrom("swd")
	wc := types.NewWorkflowContext()
	wc.PrevOutput = `"anything"`
	for _, e := range edges {
		if e.To == "d" && e.Condition != nil && e.Condition(wc) { return }
	}
	t.Error("default case condition should always return true")
}

func TestContains(t *testing.T) {
	if !Contains("hello")("say hello world") { t.Error("should match") }
	if Contains("hello")("goodbye") { t.Error("should not match") }
}

func TestContains_Empty(t *testing.T) {
	if Contains("")("anything") { t.Error("empty should not match") }
}

func TestNotContains(t *testing.T) {
	if !NotContains("error")("success") { t.Error("should match") }
	if NotContains("error")("error occurred") { t.Error("should not match") }
}

func TestNotContains_Empty(t *testing.T) {
	if !NotContains("")("anything") { t.Error("empty should match") }
}

func TestContainsAndNotContainsAreInverses(t *testing.T) {
	if Contains("x")("x") == NotContains("x")("x") { t.Error("should be inverses") }
}

func TestGraphContainsControlNode(t *testing.T) {
	g := graph.New()
	addDummy(g, "t")
	If(g, "g-if", func(s string) bool { return true }, "t", "")
	for _, id := range g.AllNodes() {
		if id == "g-if" { return }
	}
	t.Error("g-if not found")
}
