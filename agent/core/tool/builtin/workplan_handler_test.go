package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
)

type failingPlanNode struct{ node.BaseNode }

func (n *failingPlanNode) Run(context.Context, *types.WorkflowContext) (string, error) {
	return "", errors.New("branch failed")
}

type handlerTestFactory struct{}

func (handlerTestFactory) NewAgent(string) node.Agent { return handlerTestAgent{} }

type handlerTestAgent struct{}

func (handlerTestAgent) Chat(context.Context, string) (string, error) { return "", nil }

type runtimeHandlerFactory struct{ output string }

func (f runtimeHandlerFactory) NewAgent(string) node.Agent {
	return runtimeHandlerAgent{output: f.output}
}

type runtimeHandlerAgent struct{ output string }

func (a runtimeHandlerAgent) Chat(context.Context, string) (string, error) { return a.output, nil }

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

func TestPlanLoadPropagatesBranchRuntimeConfiguration(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	events := make(chan forkexec.Event, 16)
	tool.SetBranchEventHook(func(event forkexec.Event) { events <- event })
	tool.SetBranchRuntimeResolver(func(branchID string) forkexec.BranchRuntime {
		return forkexec.BranchRuntime{Role: "reviewer", AccountID: "account-1", AgentFactory: runtimeHandlerFactory{output: branchID + "-runtime"}}
	})
	tool.SetForkPolicy(forkexec.ForkPolicyBestEffort)
	tool.SetForkJoinPolicy(forkexec.JoinSuccessful)
	tool.SetMaxForkConcurrency(2)

	load := &planLoadHandler{tool: tool}
	loadArgs, err := json.Marshal(planLoadInput{
		Entry: "start",
		Nodes: map[string]planNodeSpec{
			"start": {Input: "start"},
			"left":  {Input: "left"},
			"right": {Input: "right"},
		},
		Edges: map[string][]string{"start": {"left", "right"}},
	})
	if err != nil {
		t.Fatalf("marshal plan_load args: %v", err)
	}
	_, err = load.Execute(context.Background(), string(loadArgs))
	if err != nil {
		t.Fatalf("plan_load error = %v", err)
	}
	if tool.wp.ForkPolicy != forkexec.ForkPolicyBestEffort || tool.wp.ForkJoinPolicy != forkexec.JoinSuccessful || tool.wp.MaxForkConcurrency != 2 {
		t.Fatalf("loaded WorkPlan lost fork configuration: %#v", tool.wp)
	}
	if tool.wp.BranchRuntimeFor == nil || tool.wp.BranchEventHook == nil {
		t.Fatal("loaded WorkPlan lost branch runtime or event hooks")
	}

	response, err := (&planRunHandler{tool: tool}).Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("plan_run error = %v", err)
	}
	var output struct {
		Status string              `json:"status"`
		Nodes  []*types.NodeResult `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		t.Fatalf("plan_run response is not JSON: %v", err)
	}
	if output.Status != "completed" {
		t.Fatalf("plan_run status = %q", output.Status)
	}
	nodeOutputs := make(map[string]string)
	for _, result := range output.Nodes {
		nodeOutputs[result.NodeID] = types.FromJSON(result.Output)
	}
	if nodeOutputs["left"] != "left-runtime" || nodeOutputs["right"] != "right-runtime" {
		t.Errorf("branch outputs = %#v, want runtime factory outputs", nodeOutputs)
	}
	seen := make(map[string]bool)
	for len(events) > 0 {
		event := <-events
		if event.BranchID == "left" || event.BranchID == "right" {
			seen[event.BranchID] = true
		}
	}
	if !seen["left"] || !seen["right"] {
		t.Errorf("branch events = %#v, want both fan-out branches", seen)
	}
}

func TestPlanLoadManualNodeUsesBranchRuntimeFactory(t *testing.T) {
	tool := NewWorkPlanTool(runtimeHandlerFactory{output: "construction-factory"})
	tool.SetGate(workplan.NewAutoApproveGate())
	tool.SetBranchRuntimeResolver(func(branchID string) forkexec.BranchRuntime {
		return forkexec.BranchRuntime{AgentFactory: runtimeHandlerFactory{output: branchID + "-runtime"}}
	})

	loadArgs, err := json.Marshal(planLoadInput{
		Entry: "start",
		Nodes: map[string]planNodeSpec{
			"start":  {Input: "start"},
			"manual": {Input: "manual", Kind: "manual"},
			"auto":   {Input: "auto"},
		},
		Edges: map[string][]string{"start": {"manual", "auto"}},
	})
	if err != nil {
		t.Fatalf("marshal plan_load args: %v", err)
	}
	if _, err := (&planLoadHandler{tool: tool}).Execute(context.Background(), string(loadArgs)); err != nil {
		t.Fatalf("plan_load error = %v", err)
	}

	response, err := (&planRunHandler{tool: tool}).Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("plan_run error = %v", err)
	}
	var output struct {
		Status string              `json:"status"`
		Nodes  []*types.NodeResult `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(response), &output); err != nil {
		t.Fatalf("plan_run response is not JSON: %v", err)
	}
	if output.Status != "completed" {
		t.Fatalf("plan_run status = %q", output.Status)
	}
	for _, result := range output.Nodes {
		if result.NodeID == "manual" && types.FromJSON(result.Output) == "manual-runtime" {
			return
		}
	}
	t.Errorf("manual node did not use branch runtime factory: %#v", output.Nodes)
}
