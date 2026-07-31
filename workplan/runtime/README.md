# workplan/runtime

`workplan/runtime` 为 Plan 内核提供校验、调度、执行、检查点、序列化及 Graph 编辑外观。

## 模块导航

| 包 | 职责 |
| --- | --- |
| [graph/](graph/README.md) | Plan 的编辑与查询外观 |
| [validate/](validate/README.md) | Plan 的 DAG 与引用校验 |
| [executor/](executor/README.md) | 单节点执行分派 |
| [scheduler/](scheduler/README.md) | 基于 Plan 边的依赖调度 |
| [runner/](runner/README.md) | Run/Resume 顶层入口 |
| [serialize/](serialize/README.md) | DSL 与 Plan 内核转换 |
| [forkexec/](forkexec/README.md) | 分支并发、隔离与合并 |
| [checkpoint/](checkpoint/README.md) | 快照存储与恢复 |

## 执行路径

`Runner` 校验 Plan 并创建上下文，`Scheduler` 根据 Plan 的边推进节点，`Executor` 执行节点；Graph 不在这条执行依赖链中。

## 验证

- `go test ./workplan/runtime/...`
