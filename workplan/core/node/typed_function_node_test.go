package node

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

func TestTypedFunctionNodeUsesStructuredInputAndOutput(t *testing.T) {
	type request struct {
		Value int `json:"value"`
	}
	type response struct {
		Double int `json:"double"`
	}

	n := NewTypedFunctionNode[request, response]("double", func(_ context.Context, input request) (response, error) {
		return response{Double: input.Value * 2}, nil
	})
	wc := types.NewWorkflowContext()
	wc.SetPrevValue(types.RawValue(`{"value":21}`))

	value, err := n.RunValue(context.Background(), wc)
	if err != nil {
		t.Fatal(err)
	}
	var got response
	decoded, err := types.DecodeValue[response](value)
	if err != nil {
		t.Fatal(err)
	}
	got = decoded
	if got.Double != 42 {
		t.Fatalf("response = %#v", got)
	}
}

func TestTypedFunctionNodeNilFunctionReturnsError(t *testing.T) {
	n := NewTypedFunctionNode[string, string]("nil", nil)
	_, err := n.RunValue(context.Background(), types.NewWorkflowContext())
	if err == nil {
		t.Fatal("expected nil function error")
	}
}
