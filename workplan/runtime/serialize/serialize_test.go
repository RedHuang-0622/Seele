package serialize

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/dsl"
)

const examplePlan = `{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id": "inspect", "input": "inspect scope", "kind": "auto"},
    {"id": "integrate", "input": "integrate result", "kind": "auto"}
  ],
  "edges": [{"from": "inspect", "to": "integrate"}]
}`

type testFactory struct{}

func (testFactory) NewAgent(string) node.Agent { return testAgent{} }

type testAgent struct{}

func (testAgent) Chat(context.Context, string) (string, error) { return "ok", nil }

func TestNewPlanStartsAtCurrentVersion(t *testing.T) {
	p := NewPlan()
	if p.Version != dsl.Version {
		t.Fatalf("version = %d, want %d", p.Version, dsl.Version)
	}
}

func TestFromJSONToJSONRoundTrip(t *testing.T) {
	p, err := FromJSON(examplePlan)
	if err != nil {
		t.Fatalf("FromJSON error = %v", err)
	}
	jsonText, err := p.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error = %v", err)
	}
	restored, err := FromJSON(jsonText)
	if err != nil {
		t.Fatalf("round-trip parse error = %v", err)
	}
	if restored.Entry != "inspect" || len(restored.Nodes) != 2 || len(restored.Edges) != 1 {
		t.Fatalf("restored plan = %#v", restored)
	}
}

func TestCompileBuildsDirectCoreNodesAndEdges(t *testing.T) {
	p, err := FromJSON(examplePlan)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(p, testFactory{})
	if err != nil {
		t.Fatalf("Compile error = %v", err)
	}
	if compiled.Entry() != "inspect" || len(compiled.AllEdges()) != 1 {
		t.Fatalf("compiled plan = entry %q, edges %#v", compiled.Entry(), compiled.AllEdges())
	}
	inspect := compiled.GetNode("inspect")
	auto, ok := inspect.(*node.AutoNode)
	if !ok {
		t.Fatalf("compiled node = %T, want *node.AutoNode", inspect)
	}
	if auto.DSLInput() != "inspect scope" {
		t.Fatalf("input = %q", auto.DSLInput())
	}
}

func TestToPlanExportsCorePlan(t *testing.T) {
	kernel := coreplan.New()
	kernel.AddNode(node.NewAutoNode("inspect", testFactory{}, "inspect scope"))
	kernel.AddNode(node.NewAutoNode("integrate", testFactory{}, "integrate result"))
	kernel.SetEntry("inspect")
	kernel.AddEdge(edge.Edge{From: "inspect", To: "integrate"})

	p, err := ToPlan(kernel)
	if err != nil {
		t.Fatalf("ToPlan error = %v", err)
	}
	if p.Version != 1 || p.Entry != "inspect" || len(p.Nodes) != 2 || len(p.Edges) != 1 {
		t.Fatalf("exported plan = %#v", p)
	}
}

func TestCompileAndParseReturnSemanticErrors(t *testing.T) {
	_, err := FromJSON(`{"version":1,"entry":"a","nodes":[{"id":"a","input":"run","kind":"auto"}],"edges":[{"from":"a","to":"a"}]}`)
	if err == nil || !strings.Contains(err.Error(), "$.edges[0]") || !strings.Contains(err.Error(), "self-edge") {
		t.Fatalf("error = %v", err)
	}
}

func TestFromPlanCreatesInspectablePlaceholder(t *testing.T) {
	p, err := FromJSON(examplePlan)
	if err != nil {
		t.Fatal(err)
	}
	kernel, err := FromPlan(p, types.NewConditionRegistry())
	if err != nil {
		t.Fatalf("FromPlan error = %v", err)
	}
	_, err = kernel.GetNode("inspect").Run(context.Background(), types.NewWorkflowContext())
	if err == nil || !strings.Contains(err.Error(), "compile the DSL") {
		t.Fatalf("placeholder error = %v", err)
	}
}

// ToPlan is the only guard preventing a non-representable graph from being
// exported as a v1 document, so each rejection path is pinned here.
func TestToPlanRejectsNodesTheDSLCannotRepresent(t *testing.T) {
	t.Run("non-auto kind", func(t *testing.T) {
		kernel := coreplan.New()
		kernel.AddNode(&kindOnlyNode{BaseNode: node.NewBaseNode("emit", node.KindEmit)})
		kernel.SetEntry("emit")

		_, err := ToPlan(kernel)
		if err == nil || !strings.Contains(err.Error(), `node "emit" has kind "emit"`) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("auto kind without declarative input", func(t *testing.T) {
		kernel := coreplan.New()
		kernel.AddNode(&kindOnlyNode{BaseNode: node.NewBaseNode("opaque", node.KindAuto)})
		kernel.SetEntry("opaque")

		_, err := ToPlan(kernel)
		if err == nil || !strings.Contains(err.Error(), "does not expose its declarative input") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("plan failing DSL validation", func(t *testing.T) {
		kernel := coreplan.New()
		kernel.AddNode(node.NewAutoNode("a", testFactory{}, "run"))
		kernel.AddNode(node.NewAutoNode("orphan", testFactory{}, "run"))
		kernel.SetEntry("a")

		_, err := ToPlan(kernel)
		if err == nil || !strings.Contains(err.Error(), "unreachable") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCompileRejectsInvalidPlanAndNilFactory(t *testing.T) {
	valid, err := FromJSON(examplePlan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(valid, nil); err == nil || !strings.Contains(err.Error(), "agent factory is nil") {
		t.Fatalf("nil factory error = %v", err)
	}

	invalid := &Plan{Version: dsl.Version, Entry: "a"}
	if _, err := Compile(invalid, testFactory{}); err == nil {
		t.Fatal("Compile must reject a plan that fails validation")
	}
	if _, err := FromPlan(invalid, nil); err == nil {
		t.Fatal("FromPlan must reject a plan that fails validation")
	}
}

// kindOnlyNode reports a kind but exposes no DSLInput.
type kindOnlyNode struct{ node.BaseNode }

func (n *kindOnlyNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return "", nil
}
