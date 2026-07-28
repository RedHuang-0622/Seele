# workplan/runtime/graph

该包是 WorkPlan 的运行时图容器，保存节点和边并提供安全的查询与变更操作。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` | 创建空图 |
| `Graph` | 节点、边及其查询/变更 API |

## 实现细节

- 图以节点索引和边集合管理结构，读写由 `RWMutex` 协调，供构建阶段和运行期检查安全使用。
- 图不执行节点也不解释边条件；这些职责分别属于 Executor 和 Scheduler。
- 对外获取集合时采用快照式返回，避免调用方修改内部集合。

## 依赖与验证

- 模型：[core/node](../../core/node/README.md)、[core/edge](../../core/edge/README.md)
- 验证：`go test ./workplan/runtime/graph/...`
