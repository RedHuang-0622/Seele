package hubprov

import (
	"fmt"
	"log"

	pb "github.com/RedHuang-0622/microHub/proto/gen/proto"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// NewHubRouter 创建一个 hubRouter，实现 hubbase.HubHandler 接口。
func NewHubRouter() hubbase.HubHandler { return &hubRouter{} }

type hubRouter struct{}

func (r *hubRouter) ServiceName() string { return "seele-hub" }

func (r *hubRouter) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	if req == nil {
		return nil, nil
	}
	for _, t := range registry.GetOnlineTools() {
		if t.Method == req.Method {
			return []hubbase.DispatchTarget{
				{Addr: t.Addr, Request: req, Stream: true},
			}, nil
		}
	}
	return nil, fmt.Errorf("no online tool for method=%q", req.Method)
}

func (r *hubRouter) OnResults(results []hubbase.DispatchResult) {
	for _, ri := range results {
		if ri.Err != nil {
			log.Printf("[hubRouter] addr=%s connection error, marking offline: %v",
				ri.Target.Addr, ri.Err)
			registry.MarkOffline(ri.Target.Addr)
			continue
		}
		for _, resp := range ri.Responses {
			if resp.Status != "ok" && resp.Status != "partial" {
				log.Printf("[hubRouter] tool=%s business error: %s", resp.ToolName, resp.Status)
			}
		}
	}
}

func (r *hubRouter) Addrs() []string {
	tools := registry.GetOnlineTools()
	addrs := make([]string, 0, len(tools))
	for _, t := range tools {
		addrs = append(addrs, t.Addr)
	}
	return addrs
}
