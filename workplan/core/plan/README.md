# workplan/core/plan

`core/plan` 是 WorkPlan 的执行内核，唯一持有节点、边和入口状态；它不依赖 Graph、调度器、LLM 或 UI。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 创建空 Plan 内核 |
| `AddNode` / `RemoveNode` | 修改节点索引 |
| `AddEdge` | 添加有向边 |
| `SetEntry` / `Entry` | 管理入口节点 |
| `GetNode` / `AllNodes` / `AllEdges` | 读取内核快照 |
| `GetNextNodes` / `Resolve` | 根据边解析后继节点 |

## 实现细节

- Plan 使用原子快照保存节点索引和边集合，Graph 只持有 Plan 的外观引用。
- 节点执行仍由 `runtime/executor` 负责，Plan 不创建 Agent，也不启动 goroutine。

## 协作与验证

- 节点模型：[../node/README.md](../node/README.md)
- 边模型：[../edge/README.md](../edge/README.md)
- Graph 外观：[../../runtime/graph/README.md](../../runtime/graph/README.md)
- 验证：`go test ./workplan/core/plan/...`
