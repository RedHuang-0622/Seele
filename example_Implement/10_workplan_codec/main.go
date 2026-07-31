package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RedHuang-0622/Seele/workplan/codec"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	plantypes "github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/runner"
)

const workflowJSON = `{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id":"inspect","type":"example","data":{"kind":"function","input":"检查项目范围"}},
    {"id":"backend","type":"example","data":{"kind":"function","input":"检查后端实现"}},
    {"id":"tests","type":"example","data":{"kind":"function","input":"执行验证"}},
    {"id":"integrate","type":"example","data":{"kind":"function","input":"整合结果"}}
  ],
  "edges": [
    {"from":"inspect","to":"backend"},
    {"from":"inspect","to":"tests"},
    {"from":"backend","to":"integrate"},
    {"from":"tests","to":"integrate"}
  ]
}`

type exampleNode struct {
	id    string
	kind  string
	input string
}

func (n *exampleNode) ID() string { return n.id }

func (n *exampleNode) Run(_ context.Context, workflow *plantypes.WorkflowContext) (string, error) {
	if n.id != "integrate" {
		return n.input, nil
	}
	return fmt.Sprintf("%s：%s + %s",
		n.input,
		workflow.PrevResults["backend"],
		workflow.PrevResults["tests"],
	), nil
}

type exampleNodeCodec struct{}

type nodePayload struct {
	Kind  string `json:"kind"`
	Input string `json:"input"`
}

func (exampleNodeCodec) EncodeNode(value node.Node) (codec.NodeDefinition, error) {
	typed, ok := value.(*exampleNode)
	if !ok {
		return codec.NodeDefinition{}, fmt.Errorf("unsupported node implementation %T", value)
	}
	data, err := json.Marshal(nodePayload{Kind: typed.kind, Input: typed.input})
	if err != nil {
		return codec.NodeDefinition{}, fmt.Errorf("encode payload: %w", err)
	}
	return codec.NodeDefinition{ID: typed.id, Type: "example", Data: data}, nil
}

func (exampleNodeCodec) DecodeNode(definition codec.NodeDefinition) (node.Node, error) {
	if definition.Type != "example" {
		return nil, fmt.Errorf("unsupported type %q", definition.Type)
	}
	var payload nodePayload
	if err := json.Unmarshal(definition.Data, &payload); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	if strings.TrimSpace(payload.Input) == "" {
		return nil, fmt.Errorf("input is required")
	}
	return &exampleNode{id: definition.ID, kind: payload.Kind, input: payload.Input}, nil
}

type workplanDemoResult struct {
	FinalOutput     string
	ExecutedNodes   int
	EdgeList        string
	AdjacencyList   string
	AdjacencyMatrix string
}

func runWorkPlanDemo(ctx context.Context) (workplanDemoResult, error) {
	nodeCodec := exampleNodeCodec{}
	plan, err := codec.ImportEdgeList([]byte(workflowJSON), nodeCodec)
	if err != nil {
		return workplanDemoResult{}, fmt.Errorf("import edge list: %w", err)
	}
	result, err := runner.New(plan).Run(ctx)
	if err != nil {
		return workplanDemoResult{}, fmt.Errorf("run workplan: %w", err)
	}
	edgeList, err := codec.ExportEdgeList(plan, nodeCodec)
	if err != nil {
		return workplanDemoResult{}, fmt.Errorf("export edge list: %w", err)
	}
	adjacencyList, err := codec.ExportAdjacencyList(plan, nodeCodec)
	if err != nil {
		return workplanDemoResult{}, fmt.Errorf("export adjacency list: %w", err)
	}
	adjacencyMatrix, err := codec.ExportAdjacencyMatrix(plan, nodeCodec)
	if err != nil {
		return workplanDemoResult{}, fmt.Errorf("export adjacency matrix: %w", err)
	}
	return workplanDemoResult{
		FinalOutput:     result.FinalOutputString(),
		ExecutedNodes:   len(result.NodeResults),
		EdgeList:        string(edgeList),
		AdjacencyList:   string(adjacencyList),
		AdjacencyMatrix: string(adjacencyMatrix),
	}, nil
}

func main() {
	result, err := runWorkPlanDemo(context.Background())
	if err != nil {
		panic(err)
	}
	fmt.Printf("executed=%d, final=%s\n", result.ExecutedNodes, result.FinalOutput)
	fmt.Println("\nedge list:\n" + result.EdgeList)
	fmt.Println("\nadjacency list:\n" + result.AdjacencyList)
	fmt.Println("\nadjacency matrix:\n" + result.AdjacencyMatrix)
}
