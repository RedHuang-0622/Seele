# example_Implement

本目录提供从最小 Agent 到 WorkPlan、MCP、Provider 切换、上下文装配与追踪的可运行示例。每个子目录是独立 `main` package，并有自己的说明。

| 示例 | 场景 |
| --- | --- |
| [01_hello_seele/](01_hello_seele/README.md) | 最小 Agent 与内联工具 |
| [02_inline_tools/](02_inline_tools/README.md) | Schema 与内联工具 |
| [03_workplan/](03_workplan/README.md) | WorkPlan DSL |
| [04_mcp/](04_mcp/README.md) | MCP 接入 |
| [05_graph_tools/](05_graph_tools/README.md) | 图形工具工作流 |
| [06_provider_switch/](06_provider_switch/README.md) | 多 Provider/账户切换 |
| [07_tracer/](07_tracer/README.md) | 追踪树导出 |
| [08_composable_agent/](08_composable_agent/README.md) | 离线显式 Agent 装配、内置工具与 Telemetry |
| [09_context_pipeline/](09_context_pipeline/README.md) | 离线上下文拼装、结果筛选与显式压缩 |
| [10_workplan_codec/](10_workplan_codec/README.md) | 离线自定义 Node、JSON codec 与 DAG 执行 |

`01`–`07` 中需要真实模型的示例请准备本地配置；任何密钥均应通过未跟踪配置文件提供。`08`–`10` 完全离线，可直接运行和测试。
