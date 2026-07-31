# 10_workplan_codec

该示例展示如何实现自己的 `Node` 与 `NodeCodec`，再从通用 `nodes + edges` JSON 导入、执行并导出三种图表示；WorkPlan 内核不会解释 `kind` 或 `input` 的产品含义。

## 运行

```powershell
go run ./example_Implement/10_workplan_codec
```

无需 API Key 或外部服务。示例中的四个节点均为本地确定性实现，其中 `backend` 与 `tests` 在依赖满足后可并行执行。

## 展示的 API 与场景

- `node.Node`：自定义节点只实现 `ID` 与 `Run`。
- `codec.NodeCodec`：调用方解释 `NodeDefinition.Data` 中的 `kind`、`input` 等字段。
- `codec.ImportEdgeList`：校验版本、入口、边引用与 DAG，并返回已封存 Plan。
- `runner.New(...).Run`：按依赖关系调度节点并汇合分支结果。
- `ExportEdgeList`、`ExportAdjacencyList`、`ExportAdjacencyMatrix`：从同一个 Plan 状态源导出三种拓扑。

预期输出显示执行了四个节点，最终结果同时包含“检查后端实现”和“执行验证”，随后打印三种 JSON 图表示。

## 验证

```powershell
go test ./example_Implement/10_workplan_codec -count=1
```

测试还验证错误会精确指向非法边的 `$.edges[0].to`。
