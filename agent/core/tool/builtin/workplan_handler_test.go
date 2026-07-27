package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

type failingPlanNode struct{ node.BaseNode }

func (n *failingPlanNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return "", errors.New("branch failed")
}

type handlerTestFactory struct{}

func (handlerTestFactory) NewAgent(string) node.Agent { return handlerTestAgent{} }

type handlerTestAgent struct{}

func (handlerTestAgent) Chat(context.Context, string) (string, error) { return "", nil }

func TestPlanRunFailureIncludesKnownNodeResults(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	plan := workplan.New(handlerTestFactory{})
	plan.Graph().AddNode(&failingPlanNode{BaseNode: node.NewBaseNode("failed", node.KindMethod)})
	plan.Graph().SetEntry("failed")
	tool.wp = plan

	response, err := (&planRunHandler{tool: tool}).Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	var output struct {
		Status string              `json:"status"`
		Error  string              `json:"error"`
		Nodes  []*types.NodeResult `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if output.Status != "failed" || output.Error == "" {
		t.Errorf("failure response = %#v, want status and error", output)
	}
	if len(output.Nodes) != 1 || output.Nodes[0].NodeID != "failed" || output.Nodes[0].Status != "failed" {
		t.Errorf("known node results = %#v, want failed node result", output.Nodes)
	}
}
