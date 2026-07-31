# workplan

`workplan` 提供无产品语义的 Plan 执行内核：Plan 是唯一图状态源，Node 是最小执行接口，Scheduler/Executor 只负责拓扑、并发、取消和结果合并。

## 职责边界

- `core/plan` 持有节点、边、入口和 `Build → Validate → Seal → Run` 生命周期。
- `core/node` 提供最小 `Node` 合约及可选的 Auto、Function、LLM 等抽象实现。
- `runtime` 提供校验、执行、调度、分支和 checkpoint 原语；不识别 AgentFactory、Task 或产品 DSL。
- `codec` 提供邻接表、0/1 邻接矩阵的导入导出；节点如何 materialize 由调用方注入 `NodeEncoder`/`NodeDecoder`。
- `sugar` 只是可选的构建辅助 API，不是 Plan 内核的第二个状态源。

## 公开入口

| API | 用途 |
| --- | --- |
| `core/plan.New` / `core/plan.Build` | 创建或构建 Plan |
| `core/plan.Validate` / `Seal` | 检查并封存可执行拓扑 |
| `core/node.Node` | 自定义节点的最小执行接口 |
| `codec.ImportAdjacencyList` / `ExportAdjacencyList` | 邻接表 JSON 导入导出 |
| `codec.ImportAdjacencyMatrix` / `ExportAdjacencyMatrix` | 邻接矩阵 JSON 导入导出 |
| `runtime/runner.Runner` | 执行和恢复 Plan |

## 数据格式边界

Seele 只验证通用拓扑和节点编解码结果，不解释 `task`、`auto`、`agent` 等产品字段。Seelex 或其他调用方可以在 `NodeDecoder` 中解释自己的 JSON，并获得包含字段路径、行列和原因的结构化 codec 错误。

## 验证

```text
go test ./workplan/...
```
