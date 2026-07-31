# tools

`tools` 是 Seele 与 `agent` 平行的产品无关 Function Calling 子系统，负责工具合约、注册分发、权限网关以及远程 provider 适配；它不实现文件、Git、Shell、工作区或 WorkPlan 产品工具。

## 子模块

| 目录 | 职责 |
| --- | --- |
| [`builtin/`](builtin/README.md) | 可选注册的时间、基础计算和文本统计通用工具 |
| [`adapter/`](adapter/README.md) | 把通用 catalog/invoker 适配为 MCP、microHub、Skills provider |
| [`holder/`](holder/README.md) | Provider 注册、工具索引、内联函数与插件可见性 |
| [`gateway/`](gateway/README.md) | 在 Holder 上装配权限、审批和可见性边界 |
| [`permission/`](permission/README.md) | 通用 allow/ask/deny 规则与审批类型 |
| [`mcp/`](mcp/README.md) | MCP stdio/SSE 连接、工具发现、调用与熔断 |
| [`microhub/`](microhub/README.md) | microHub registry、路由和 gRPC 调用适配 |

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `ToolHandler`、`ToolEntry` | 组合模型可见定义与不透明执行器 |
| `ToolProvider`、`FunctionProvider` | 提供普通 Function Calling 工具集合 |
| `Registry`、`Dispatcher` | 建立工具快照并按名称执行调用 |
| `Middleware` | 注入 trace、审查、输出过滤或指标 |
| `SchemaOf`、`EnumOf` | 生成 Function Calling 使用的 JSON Schema 子集 |

## 实现细节

- 根 `Registry` 在注册和显式 `Refresh` 时发布快照，分发时不持锁执行 handler。
- `builtin.Provider` 只包含产品无关、无工作区写入能力的工具，并且不会被 Agent 隐式注册。
- `holder.Holder` 保留现有插件装配 API；`gateway.DefaultGateway` 在它之上执行权限与审批检查。
- MCP、microHub 与 Skills 都实现根 `ToolProvider`，因此 `agent` 只依赖调用方注入的工具运行时，不需要了解具体协议。
- 远程 provider 的连接、刷新和关闭均由调用方显式控制；根工具系统不会读取 Agent history 或工作区。

## 依赖与验证

- 公共模型：[`../types/README.md`](../types/README.md)
- Agent 装配：[`../agent/README.md`](../agent/README.md)
- 验证：`go test ./tools/...`、`go vet ./tools/...`
