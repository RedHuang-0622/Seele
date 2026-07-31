# workplan/runtime/graph

`runtime/graph` 是围绕 `core/plan` 内核的编辑与查询外观，不拥有独立的节点、边或入口存储。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 创建新的 Plan 内核及其 Graph 外观 |
| `NewWithPlan` | 为已有 Plan 内核装配 Graph 外观 |
| `Plan` | 取得背后的内核 |
| `AddNode` / `AddEdge` / `SetEntry` | 通过外观修改内核 |
| `GetNode` / `AllNodes` / `AllEdges` | 查询内核快照 |

## 实现细节

- Graph 只转发到 `core/plan.Plan`，不会反向要求 Plan 依赖 Graph。
- 调度器、运行器和序列化器直接依赖 Plan，Graph 可被后续可视化和其它修饰功能扩展。

## 依赖与验证

- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- 验证：`go test ./workplan/runtime/graph/...`
