# workplan/core/plan

`core/plan` 是 WorkPlan 的执行内核，唯一持有节点、边和入口状态；它不依赖 Graph、调度器、LLM 或 UI。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 创建空 Plan 内核 |
| `Build` | 从节点、边、入口构建、校验并 Seal 一个不可变 Plan |
| `Validate` / `Seal` / `IsSealed` | 执行生命周期校验并切换到不可变执行阶段 |
| `AddNode` / `RemoveNode` | 以 upsert 语义修改节点索引；同 ID 节点由后写覆盖 |
| `AddNodeIfAbsent` / `ReplaceNode` | 显式选择 first-write-wins 或仅替换已存在节点 |
| `AddEdge` | 追加有向边；条件函数不参与隐式身份比较 |
| `AddUnconditionalEdgeIfAbsent` | 按端点幂等添加无条件边 |
| `SetEntry` / `Entry` | 管理入口节点 |
| `GetNode` / `AllNodes` / `AllEdges` | 读取内核快照 |
| `GetNextNodes` / `Resolve` | 根据边解析后继节点 |

## 实现细节

- Plan 使用原子快照和 CAS 保存节点索引与边集合；Plan 本身就是唯一图聚合，不存在第二份 Graph 状态。
- 普通写入保持既有 upsert/append 兼容语义；并发构建需要幂等时使用显式 `IfAbsent` API，避免用不可比较的 Go function value 推断条件边身份。
- 节点执行仍由 `runtime/executor` 负责，Plan 不创建 Agent，也不启动 goroutine。

## 协作与验证

- 节点模型：[../node/README.md](../node/README.md)
- 边模型：[../edge/README.md](../edge/README.md)
- 拓扑编解码：[../../codec/README.md](../../codec/README.md)
- 验证：`go test ./workplan/core/plan/...`
