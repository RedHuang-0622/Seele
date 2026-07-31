# workplan/dsl

`workplan/dsl` 定义并校验版本化的 Seele WorkPlan JSON DSL；它只负责语法和 DAG 语义，不负责创建 Graph 或执行 Agent。

## 公开接口

| API | 用途 |
| --- | --- |
| `Plan` / `Node` / `Edge` | 正式 JSON 数据模型 |
| `Parse` | 解析 JSON，并返回带路径的语义错误 |
| `Plan.Validate` | 校验版本、字段、引用、环和可达性 |
| `Plan.ToJSON` | 校验后导出正式 JSON |

## 实现细节

- DSL v1 使用 `version`、`entry`、节点数组和边数组；v1 的节点类型为 `auto`。
- JSON 语法错误报告行列号；字段、节点和边错误报告 JSON 路径，例如 `$.nodes[1].input`。
- 编排层通过 [runtime/serialize](../runtime/serialize/README.md) 将 Plan 编译为核心节点和边。

## 验证

- `go test ./workplan/dsl/...`
