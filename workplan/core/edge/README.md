# workplan/core/edge

该包表示 DAG 中的有向边，并根据工作流上下文决定下一个节点。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Edge` | 起点、终点、优先级与可选条件 |
| `Resolve` | 从当前节点的候选边中选出下一跳 |
| `HasEdgeFrom` / `HasEdgeTo` | 图校验辅助函数 |

## 实现细节

- `Resolve` 只评估当前节点出边，先按优先级处理匹配条件，再处理默认边。
- 条件函数读取 `WorkflowContext`，因此路由逻辑与节点执行逻辑分离。
- 该包只依赖 core types，可被 Plan 校验和 Scheduler 共用。

## 依赖与验证

- 上下文：[types](../types/README.md)
- 调度：[runtime/scheduler](../../runtime/scheduler/README.md)
- 验证：`go test ./workplan/core/edge/...`
