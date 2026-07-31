# workplan/runtime/validate

`runtime/validate` 校验 Plan 内核的入口、边引用、环和孤立节点。

## 公开接口

| API | 用途 |
| --- | --- |
| `Plan` | 执行完整 Plan 校验 |
| `EntryNode` | 校验入口存在 |
| `EdgeReferences` | 校验边端点存在 |
| `Cyclic` / `Orphan` | 校验 DAG 与连接性 |

## 实现细节

- 校验器直接依赖 `core/plan.Plan`，不依赖 Graph 外观。
- DSL 层提前提供带 JSON 路径的用户输入错误；本包保护任意编程式 Plan 的运行时不变量。

## 依赖与验证

- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- DSL：[../../dsl/README.md](../../dsl/README.md)
- 验证：`go test ./workplan/runtime/validate/...`
