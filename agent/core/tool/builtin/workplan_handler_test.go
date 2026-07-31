package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
)

const formalPlanJSON = `{
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
}`

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

func TestPlanLoadCompilesFormalDSLAndExportsRoundTrip(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	response, err := (&planLoadHandler{tool: tool}).Execute(context.Background(), formalPlanJSON)
	if err != nil {
		t.Fatalf("plan_load error = %v", err)
	}
	if !strings.Contains(response, `"version":1`) || !strings.Contains(response, `"node_count":4`) {
		t.Fatalf("load response = %s", response)
	}

	compiled := tool.wp.Plan()
	loaded := compiled.GetNode("inspect")
	if _, ok := loaded.(*node.AutoNode); !ok {
		t.Fatalf("plan_load compiled %T, want direct *node.AutoNode", loaded)
	}
	if loaded.Kind() != node.KindAuto {
		t.Fatalf("compiled kind = %q, want auto", loaded.Kind())
	}

	exported, err := (&planExportHandler{tool: tool}).Execute(context.Background(), "{}")
	if err != nil {
		t.Fatalf("plan_export error = %v", err)
	}
	var plan planLoadInput
	if err := json.Unmarshal([]byte(exported), &plan); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if plan.Version != 1 || plan.Entry != "inspect" || len(plan.Nodes) != 4 || len(plan.Edges) != 4 {
		t.Fatalf("exported plan = %#v", plan)
	}
	for _, spec := range plan.Nodes {
		if spec.ID == "inspect" && spec.Input != "检查项目范围" {
			t.Fatalf("exported inspect input = %q", spec.Input)
		}
	}
}

func TestPlanLoadReportsPreciseDSLValidationErrors(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	cases := []struct {
		name string
		data string
		want string
	}{
		{
			name: "syntax location",
			data: "{\n  \"version\": 1,\n",
			want: "line 2, column 16",
		},
		{
			name: "node path and reason",
			data: `{"version":1,"entry":"start","nodes":[{"id":"start","input":"run","kind":"manual"}],"edges":[]}`,
			want: "$.nodes[0].kind: must be \"auto\"",
		},
		{
			name: "edge path and reason",
			data: `{"version":1,"entry":"start","nodes":[{"id":"start","input":"run","kind":"auto"}],"edges":[{"from":"start","to":"missing"}]}`,
			want: "$.edges[0].to: references undeclared node \"missing\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&planLoadHandler{tool: tool}).Execute(context.Background(), tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPlanLoadPropagatesBranchRuntimeToDirectAutoNodes(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	events := make(chan forkexec.Event, 16)
	tool.SetBranchEventHook(func(event forkexec.Event) { events <- event })
	tool.SetBranchRuntimeResolver(func(branchID string) forkexec.BranchRuntime {
		return forkexec.BranchRuntime{Role: "reviewer", AccountID: "account-1", AgentFactory: runtimeHandlerFactory{output: branchID + "-runtime"}}
	})
	tool.SetForkPolicy(forkexec.ForkPolicyBestEffort)
	tool.SetForkJoinPolicy(forkexec.JoinSuccessful)
	tool.SetMaxForkConcurrency(2)

	data := `{"version":1,"entry":"start","nodes":[{"id":"start","input":"start","kind":"auto"},{"id":"left","input":"left","kind":"auto"},{"id":"right","input":"right","kind":"auto"}],"edges":[{"from":"start","to":"left"},{"from":"start","to":"right"}]}`
	if _, err := (&planLoadHandler{tool: tool}).Execute(context.Background(), data); err != nil {
		t.Fatalf("plan_load error = %v", err)
	}
	if tool.wp.ForkPolicy != forkexec.ForkPolicyBestEffort || tool.wp.ForkJoinPolicy != forkexec.JoinSuccessful || tool.wp.MaxForkConcurrency != 2 {
		t.Fatalf("loaded WorkPlan lost fork configuration: %#v", tool.wp)
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
		t.Errorf("branch outputs = %#v, want branch runtime outputs", nodeOutputs)
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

func TestPlanRunFailureIncludesKnownNodeResults(t *testing.T) {
	tool := NewWorkPlanTool(handlerTestFactory{})
	plan := workplan.New(handlerTestFactory{})
	plan.Plan().AddNode(&failingPlanNode{BaseNode: node.NewBaseNode("failed", node.KindMethod)})
	plan.Plan().SetEntry("failed")
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
