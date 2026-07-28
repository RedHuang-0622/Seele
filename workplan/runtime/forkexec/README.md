# workplan/runtime/forkexec

该包执行 WorkPlan 的并发 Fork 分支，并定义隔离、并发限制和合并策略。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `ForkCoordinator` / `Spec` | 配置并执行一组分支 |
| `Policy`、`JoinPolicy` | 控制失败、取消与合并行为 |
| `BranchRuntime`、`Event` | 注入分支运行资源并观察生命周期 |
| `ContextManager` | 创建和合并分支上下文 |

## 实现细节

- 每个分支从父上下文的快照创建隔离视图，完成后按 JoinPolicy 合并，避免并发直接写同一变量 map。
- 限流器控制启动的分支数；事件覆盖 queued、started、completed、failed、canceled 与 panic。
- 协调器收集所有分支结果并统一处理取消和 panic，调用方获得确定的汇总结果。

## 依赖与验证

- 上下文：[core/types](../../core/types/README.md)
- 节点构造：[sugar/fork](../../sugar/fork/README.md)
- 验证：`go test ./workplan/runtime/forkexec/...`
