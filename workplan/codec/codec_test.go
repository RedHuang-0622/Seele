package codec

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type testNode struct {
	id    string
	value string
}

func (n *testNode) ID() string { return n.id }
func (n *testNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return n.value, nil
}

type testCodec struct{}

func (testCodec) EncodeNode(n node.Node) (NodeDefinition, error) {
	value := n.(*testNode)
	data, _ := json.Marshal(map[string]string{"value": value.value})
	return NodeDefinition{ID: value.id, Type: "test", Data: data}, nil
}

func (testCodec) DecodeNode(def NodeDefinition) (node.Node, error) {
	if def.Type != "test" {
		return nil, errors.New("unsupported node type " + def.Type)
	}
	var payload struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(def.Data, &payload); err != nil {
		return nil, err
	}
	return &testNode{id: def.ID, value: payload.Value}, nil
}

func makePlan() *coreplan.Plan {
	returnPlan := coreplan.New()
	returnPlan.AddNode(&testNode{id: "inspect", value: "scope"})
	returnPlan.AddNode(&testNode{id: "backend", value: "backend"})
	returnPlan.AddNode(&testNode{id: "tests", value: "tests"})
	returnPlan.AddNode(&testNode{id: "integrate", value: "integrate"})
	returnPlan.SetEntry("inspect")
	returnPlan.AddEdge(edge.Edge{From: "inspect", To: "backend"})
	returnPlan.AddEdge(edge.Edge{From: "inspect", To: "tests"})
	returnPlan.AddEdge(edge.Edge{From: "backend", To: "integrate"})
	returnPlan.AddEdge(edge.Edge{From: "tests", To: "integrate"})
	return returnPlan
}

func TestAdjacencyListRoundTrip(t *testing.T) {
	data, err := ExportAdjacencyList(makePlan(), testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"adjacency"`) {
		t.Fatalf("unexpected document: %s", data)
	}
	decoded, err := ImportAdjacencyList(data, testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.IsSealed() || decoded.Entry() != "inspect" || len(decoded.AllEdges()) != 4 {
		t.Fatalf("round-trip plan = entry %q, sealed=%v, edges=%d", decoded.Entry(), decoded.IsSealed(), len(decoded.AllEdges()))
	}
}

func TestEdgeListRoundTripFormalNodesEdgesShape(t *testing.T) {
	data, err := ExportEdgeList(makePlan(), testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"edges"`) || strings.Contains(string(data), `"adjacency"`) {
		t.Fatalf("unexpected edge-list document: %s", data)
	}
	decoded, err := ImportEdgeList(data, testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.IsSealed() || decoded.Entry() != "inspect" || len(decoded.AllEdges()) != 4 {
		t.Fatalf("round-trip plan = entry %q, sealed=%v, edges=%d", decoded.Entry(), decoded.IsSealed(), len(decoded.AllEdges()))
	}
}

func TestEdgeListReportsPreciseEndpointError(t *testing.T) {
	data := []byte(`{"version":1,"entry":"a","nodes":[{"id":"a","type":"test","data":{"value":"a"}}],"edges":[{"from":"a","to":"missing"}]}`)
	_, err := ImportEdgeList(data, testCodec{})
	if err == nil || !strings.Contains(err.Error(), "$.edges[0].to") || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("error = %v", err)
	}
}

func TestAdjacencyMatrixRoundTrip(t *testing.T) {
	data, err := ExportAdjacencyMatrix(makePlan(), testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ImportAdjacencyMatrix(data, testCodec{})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Entry() != "inspect" || len(decoded.AllEdges()) != 4 {
		t.Fatalf("round-trip plan = entry %q, edges=%d", decoded.Entry(), len(decoded.AllEdges()))
	}
}

func TestAdjacencyMatrixReportsPreciseCellError(t *testing.T) {
	data := []byte(`{"version":1,"entry":"a","nodes":[{"id":"a","type":"test","data":{"value":"a"}},{"id":"b","type":"test","data":{"value":"b"}}],"order":["a","b"],"matrix":[[0,2],[0,0]]}`)
	_, err := ImportAdjacencyMatrix(data, testCodec{})
	if err == nil || !strings.Contains(err.Error(), "$.matrix[0][1]") || !strings.Contains(err.Error(), "0 or 1") {
		t.Fatalf("error = %v", err)
	}
}

func TestAdjacencyListRejectsUnknownTarget(t *testing.T) {
	data := []byte(`{"version":1,"entry":"a","nodes":[{"id":"a","type":"test","data":{"value":"a"}}],"adjacency":{"a":["missing"]}}`)
	_, err := ImportAdjacencyList(data, testCodec{})
	if err == nil || !strings.Contains(err.Error(), `$.adjacency["a"][0]`) || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("error = %v", err)
	}
}
