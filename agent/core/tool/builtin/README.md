# agent/core/tool/builtin

该包提供本地内置工具，包括文件、编辑、grep、git、shell、匹配和 WorkPlan 工具。

## 公开接口

| API | 用途 |
| --- | --- |
| `RegisterAll` | 注册标准内置工具 |
| `NewWorkPlanTool` | 创建 WorkPlan DSL 工具提供者 |
| `NewChatAgentFactory` | 将 ChatCompleter 注入自动子代理节点 |
| `plan_load` | 校验并原子装载 Seele DSL v1 |
| `plan_run` / `plan_export` | 执行或导出已装载的 Plan 内核 |

## 实现细节

- `plan_load` 接受 `{version, entry, nodes, edges}` 正式形状，并返回 JSON 行列号或 DSL 路径定位的错误。
- 装载时直接编译为 Plan 内核、`AutoNode` 和 `edge.Edge`；不经 sugar 适配。
- `plan_run` 的独立后继节点由 Scheduler 作为子代理分支运行；分支运行时可注入独立 AgentFactory。

## 依赖与验证

- DSL：[../../../../workplan/dsl/README.md](../../../../workplan/dsl/README.md)
- 编译器：[../../../../workplan/runtime/serialize/README.md](../../../../workplan/runtime/serialize/README.md)
- 验证：`go test ./agent/core/tool/builtin/...`
