# workplan/codec

`workplan/codec` 负责把产品可读的 `nodes + edges` 文档转换为可执行的 `workplan/core/plan.Plan`，以及把 Plan 导出回稳定 JSON。codec 只处理拓扑和节点装配，不解释 Seelex 的产品语义。

## 公共接口

| API | 用途 |
| --- | --- |
| `Document[T]` | 泛型产品文档，包含 `version`、`entry`、`nodes`、`edges` |
| `NodeSpec[T]` | 最小节点单位：`id`、`input`、`kind`、可选 `metadata` |
| `EdgeSpec` | 最小边单位：`from`、`to` |
| `NodeFactory[T]` | 将产品节点描述装配为 `core/node.Node` |
| `Import` / `Render` | 导入 JSON 或已解码的 Document 并构建已校验 Plan |
| `ExportDocument` / `ToDocument` | 从 Plan 导出产品 DSL |
| `ImportEdgeList` / `ExportEdgeList` | 兼容旧的 `NodeDefinition{type,data}` edge-list 格式 |
| `ImportAdjacencyList` / `ExportAdjacencyList` | 邻接表格式 |
| `ImportAdjacencyMatrix` / `ExportAdjacencyMatrix` | 0/1 邻接矩阵格式 |

## 产品 DSL

Seelex 决定 `input` 和 `kind` 的含义，Seele 只把它们原样交给 `NodeFactory[T]`：

```json
{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id": "inspect", "input": "检查项目范围", "kind": "auto"},
    {"id": "backend", "input": "检查后端实现", "kind": "auto"},
    {"id": "tests", "input": "执行验证", "kind": "auto"},
    {"id": "integrate", "input": "整合结果", "kind": "auto"}
  ],
  "edges": [
    {"from": "inspect", "to": "backend"},
    {"from": "inspect", "to": "tests"},
    {"from": "backend", "to": "integrate"},
    {"from": "tests", "to": "integrate"}
  ]
}
```

`Document[string]` 适合简单产品输入；复杂产品可以使用 `Document[MyInput]`，由标准 JSON 编码器完成结构化传输。

## 错误契约

导入、导出和节点装配错误统一返回根目录 `errors` 包的结构化错误，包含 `struct`、`function`、`step`、`path`、`raw` 和可选行列号。调用方可以使用 `seeleerrors.From(err)` 获取字段，不需要解析错误字符串。

## 实现边界

- `Plan` 在导入成功后会完成拓扑校验并 Seal；循环、孤立节点、未知端点会被拒绝。
- 条件边无法无损表示为纯 `edges[{from,to}]`，导出时返回带路径的错误。
- `Value`/泛型节点是结构化传输扩展；旧 `Node.Run(string)` 契约继续保留用于兼容。

## 验证

```powershell
go test ./workplan/codec/... -count=1
go test ./errors/... -count=1
```

相关实现：[workplan/core/plan](../core/plan)、[workplan/core/node](../core/node)、[统一错误包](../../errors/README.md)。
