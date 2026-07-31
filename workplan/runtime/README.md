# workplan/runtime

`workplan/runtime` 为 Plan 内核提供校验、节点执行、依赖调度、分支隔离和 checkpoint 原语。

## 子模块

| 包 | 职责 |
| --- | --- |
| [validate/](validate/README.md) | Plan 拓扑与引用校验 |
| [executor/](executor/README.md) | 单节点执行分派 |
| [scheduler/](scheduler/README.md) | 基于 Plan 边的依赖调度 |
| [runner/](runner/README.md) | Run/Resume 顶层入口 |
| [forkexec/](forkexec/README.md) | 分支并发、隔离与合并 |
| [checkpoint/](checkpoint/README.md) | 快照存储与恢复 |

## 执行路径

`Runner` 校验 Plan 并创建上下文，`Scheduler` 根据 Plan 的边推进节点，`Executor` 调用 `Node.Run`。Plan 是这条执行依赖链中的唯一拓扑状态源。节点如何由 JSON 或产品 DSL materialize 不属于 runtime；请使用 `workplan/codec` 注入 `NodeDecoder`。

## 验证

```text
go test ./workplan/runtime/...
```
