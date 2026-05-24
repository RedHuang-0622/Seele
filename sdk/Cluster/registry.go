package cluster

// 日后多 Agent 调度时，注册表中的 subagents 字段类型。
//
// 注册表示例：
//
//	subagents:
//	  - name: "data_agent"
//	    addr: "localhost:51112"
//	    capabilities: ["data_explore", "data_clean"]
type SubagentRef struct {
	Name         string   `yaml:"name"`
	Addr         string   `yaml:"addr"`
	Capabilities []string `yaml:"capabilities"`
}
