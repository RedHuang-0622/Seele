# workplan/codec

`codec` 负责 Plan 拓扑的通用 JSON 编解码，不解释节点的产品语义，也不依赖 `agent`、`task` 或 Seelex。调用方通过 `NodeEncoder`/`NodeDecoder` 注入节点实现。

## 格式

| API | JSON 形状 | 用途 |
| --- | --- | --- |
| `ExportEdgeList` / `ImportEdgeList` | `version`、`entry`、`nodes`、`edges[{from,to}]` | 正式 nodes/edges 图形状 |
| `ExportAdjacencyList` / `ImportAdjacencyList` | `version`、`entry`、`nodes`、`adjacency` | 邻接表 |
| `ExportAdjacencyMatrix` / `ImportAdjacencyMatrix` | `version`、`entry`、`nodes`、`order`、`matrix` | 0/1 邻接矩阵 |

`NodeDefinition.Type` 和 `Data` 是不透明载荷。调用方可以在 `Data` 中放入 `input`、`kind` 或任何自己的节点配置，然后由 decoder 创建具体 Node。

## 错误

导入会校验版本、入口、节点 ID、边引用、重复边、矩阵行列和 DAG 拓扑。错误包含 JSON 路径；矩阵解析错误还包含行列位置，例如 `$.matrix[1][3]`。

## 生命周期

导入完成后返回已验证并封存的 `core/plan.Plan`。导出只接受可验证的 Plan；条件边无法表达为无条件邻接结构时会返回语义化错误。

## 验证

```text
go test ./workplan/codec/...
```
