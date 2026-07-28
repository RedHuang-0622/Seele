# workplan/runtime

`workplan/runtime` 执行并维护 WorkPlan 图：从图构建、校验、逐节点执行到 Fork 协调、检查点和序列化。

| 包 | 职责 |
| --- | --- |
| [graph/](graph/README.md) | 并发安全的节点/边容器 |
| [validate/](validate/README.md) | DAG 与引用校验 |
| [executor/](executor/README.md) | 单节点执行分派 |
| [scheduler/](scheduler/README.md) | 依据边推进主循环 |
| [runner/](runner/README.md) | Run/Resume 顶层入口 |
| [forkexec/](forkexec/README.md) | 分支并发、隔离和合并 |
| [checkpoint/](checkpoint/README.md) | 快照存储与恢复 |
| [serialize/](serialize/README.md) | 图与 Plan 的双向转换 |

## 执行路径

`Runner` 校验图并创建上下文 → `Scheduler` 选择当前节点与下一条边 → `Executor` 执行节点；Fork 节点交给 `forkexec`，暂停状态由 checkpoint 保存。

## 验证

- `go test ./workplan/runtime/...`
