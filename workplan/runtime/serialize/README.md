# workplan/runtime/serialize

该包把运行时图转换为可传输的 `Plan`，并借助条件注册表从 Plan 重建图。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Plan`、`PlanNodeSpec`、`PlanEdgeSpec` | JSON 友好的图描述 |
| `ToPlan` / `FromPlan` | 图与 Plan 的双向转换 |
| `FromJSON` | 解析 Plan JSON |

## 实现细节

- 序列化只保留节点类型、输入、边和可标识的条件，不试图序列化函数闭包。
- 反序列化通过 `ConditionRegistry` 解析条件名，缺失条件即报错，避免恢复出行为不完整的图。
- Plan 适合工具传参和持久化，Graph 仍是运行时的并发安全结构。

## 依赖与验证

- 图：[graph](../graph/README.md)，条件：[core/types](../../core/types/README.md)
- 验证：`go test ./workplan/runtime/serialize/...`
