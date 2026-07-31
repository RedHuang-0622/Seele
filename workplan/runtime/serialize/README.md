# workplan/runtime/serialize

`runtime/serialize` 负责在正式 Seele JSON DSL、Plan 内核和可执行核心节点之间转换。

## 公开接口

| API | 用途 |
| --- | --- |
| `FromJSON` | 解析并校验版本化 DSL |
| `ToPlan` | 将 Plan 内核导出为 DSL 数据模型 |
| `Compile` | 使用 `AgentFactory` 创建核心 `AutoNode` 和 `edge.Edge` |
| `FromPlan` | 在没有 AgentFactory 时创建只读检查用占位内核 |

## 实现细节

- 编译路径绕过 sugar，直接调用 `core/node.NewAutoNode` 与 `core/plan.AddEdge`。
- v1 导出包含 `version`、`entry`、节点 `input` 和边数组；不可表达的节点类型会返回明确错误。

## 依赖与验证

- 语法：[../../dsl/README.md](../../dsl/README.md)
- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- 验证：`go test ./workplan/runtime/serialize/...`
