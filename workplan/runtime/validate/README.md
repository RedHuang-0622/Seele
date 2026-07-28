# workplan/runtime/validate

该包在执行前验证 WorkPlan 图，尽早发现结构错误。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Graph` | 运行完整校验集合 |
| `EntryNode`、`EdgeReferences`、`Cyclic`、`Orphan` | 可独立调用的专项校验 |

## 实现细节

- 校验入口节点、边引用、非 Loop 环、不可达节点等不变量；错误在执行前返回，而非落入调度器死循环。
- 环检测遍历图结构，但 Loop 是显式节点语义，不用普通边环绕过校验。
- 每个函数保持独立，便于构建器或测试精确验证失败原因。

## 依赖与验证

- 图容器：[graph](../graph/README.md)
- 验证：`go test ./workplan/runtime/validate/...`
