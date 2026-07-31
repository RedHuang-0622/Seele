package main

import (
	"context"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/codec"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
)

func TestRunWorkPlanDemo(t *testing.T) {
	result, err := runWorkPlanDemo(context.Background())
	if err != nil {
		t.Fatalf("runWorkPlanDemo() error = %v", err)
	}
	if result.ExecutedNodes != 4 {
		t.Fatalf("executed nodes = %d, want 4", result.ExecutedNodes)
	}
	if !strings.Contains(result.FinalOutput, "检查后端实现") || !strings.Contains(result.FinalOutput, "执行验证") {
		t.Fatalf("final output = %q", result.FinalOutput)
	}
	if !strings.Contains(result.EdgeList, `"edges"`) || !strings.Contains(result.AdjacencyList, `"adjacency"`) || !strings.Contains(result.AdjacencyMatrix, `"matrix"`) {
		t.Fatalf("missing exported topology formats")
	}
}

func TestCodecErrorIdentifiesInvalidEdgeTarget(t *testing.T) {
	invalid := []byte(`{"version":1,"entry":"inspect","nodes":[{"id":"inspect","type":"example","data":{"kind":"function","input":"inspect"}}],"edges":[{"from":"inspect","to":"missing"}]}`)
	_, err := codec.ImportEdgeList(invalid, exampleNodeCodec{})
	if err == nil || !strings.Contains(err.Error(), "$.edges[0].to") || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("error = %v", err)
	}
}

func TestNodeCodecRejectsUnsupportedImplementationsAndPayloads(t *testing.T) {
	nodeCodec := exampleNodeCodec{}
	if _, err := nodeCodec.EncodeNode(node.NewFunctionNode("function", func(context.Context, string) (string, error) {
		return "", nil
	})); err == nil || !strings.Contains(err.Error(), "unsupported node implementation") {
		t.Fatalf("EncodeNode() error = %v", err)
	}
	if _, err := nodeCodec.DecodeNode(codec.NodeDefinition{ID: "x", Type: "unknown"}); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("DecodeNode(unknown) error = %v", err)
	}
	if _, err := nodeCodec.DecodeNode(codec.NodeDefinition{ID: "x", Type: "example", Data: []byte(`{"kind":"function"}`)}); err == nil || !strings.Contains(err.Error(), "input is required") {
		t.Fatalf("DecodeNode(empty input) error = %v", err)
	}
}

func TestMainEntrypoint(t *testing.T) {
	main()
}
