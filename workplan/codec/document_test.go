package codec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	seeleerrors "github.com/RedHuang-0622/Seele/errors"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type productNode struct {
	node.BaseNode
	input string
}

func newProductNode(spec NodeSpec[string]) (node.Node, error) {
	return &productNode{
		BaseNode: node.NewBaseNode(spec.ID, node.KindAuto),
		input:    spec.Input,
	}, nil
}

func (n *productNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return n.input, nil
}

func TestDocumentStringProductShapeRoundTrip(t *testing.T) {
	data := []byte(`{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id": "inspect", "input": "检查项目范围", "kind": "auto"},
    {"id": "backend", "input": "检查后端实现", "kind": "auto"},
    {"id": "tests", "input": "执行验证", "kind": "auto"},
    {"id": "integrate", "input": "整合结果", "kind": "auto"}
  ],
  "edges": [
    {"from": "inspect", "to": "backend"},
    {"from": "inspect", "to": "tests"},
    {"from": "backend", "to": "integrate"},
    {"from": "tests", "to": "integrate"}
  ]
}`)

	plan, err := Import(data, NodeFactoryFunc[string](newProductNode))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Entry() != "inspect" || len(plan.AllNodes()) != 4 || len(plan.AllEdges()) != 4 {
		t.Fatalf("plan = entry %q, nodes=%d, edges=%d", plan.Entry(), len(plan.AllNodes()), len(plan.AllEdges()))
	}

	exported, err := ExportDocument[string](plan, NodeEncoderFunc[string](func(n node.Node) (NodeSpec[string], error) {
		product := n.(*productNode)
		return NodeSpec[string]{ID: product.ID(), Input: product.input, Kind: product.Kind().String()}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	var got Document[string]
	if err := json.Unmarshal(exported, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || got.Entry != "inspect" || len(got.Nodes) != 4 || len(got.Edges) != 4 {
		t.Fatalf("exported document = %#v", got)
	}
}

type typedInput struct {
	Prompt string `json:"prompt"`
	Limit  int    `json:"limit"`
}

func TestDocumentGenericStructInput(t *testing.T) {
	document := Document[typedInput]{
		Version: 1,
		Entry:   "inspect",
		Nodes: []NodeSpec[typedInput]{
			{ID: "inspect", Input: typedInput{Prompt: "scope", Limit: 2}, Kind: "custom"},
		},
	}
	var received typedInput
	plan, err := Render(document, NodeFactoryFunc[typedInput](func(spec NodeSpec[typedInput]) (node.Node, error) {
		received = spec.Input
		return &productNode{BaseNode: node.NewBaseNode(spec.ID, node.KindMethod)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if plan == nil || received.Prompt != "scope" || received.Limit != 2 {
		t.Fatalf("typed input was not preserved: %#v", received)
	}
}

func TestDocumentErrorHasStructuredLocation(t *testing.T) {
	data := []byte(`{"version":1,"entry":"a","nodes":[{"id":"a","input":"a"}],"edges":[{"from":"a","to":"missing"}]}`)
	_, err := Import(data, NodeFactoryFunc[string](newProductNode))
	if err == nil {
		t.Fatal("expected invalid edge error")
	}
	structured := seeleerrors.From(err)
	if structured == nil {
		t.Fatalf("error is not structured: %T", err)
	}
	if structured.Struct != "EdgeSpec" || structured.Function != "Validate" || structured.Step != "edges[0].to" || structured.Path != "$.edges[0].to" {
		t.Fatalf("structured error = %#v", structured)
	}
	if structured.Raw == nil {
		t.Fatal("structured error lost raw edge information")
	}
}

func TestDocumentRejectsFactoryErrorWithNodePath(t *testing.T) {
	document := Document[string]{Version: 1, Entry: "a", Nodes: []NodeSpec[string]{{ID: "a", Input: "bad"}}}
	_, err := Render(document, NodeFactoryFunc[string](func(NodeSpec[string]) (node.Node, error) {
		return nil, errors.New("unsupported product node")
	}))
	structured := seeleerrors.From(err)
	if structured == nil || structured.Path != "$.nodes[0]" || structured.Step != "nodes[0]" {
		t.Fatalf("structured factory error = %#v", structured)
	}
}
