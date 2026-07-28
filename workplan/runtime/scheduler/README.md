# workplan/runtime/scheduler

`Scheduler` 是 WorkPlan 的主循环：按当前节点执行、记录结果、解析边并推进到下一个节点。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` | 绑定图与 Executor |
| `Scheduler.Run` | 从入口或指定节点运行调度循环 |

## 实现细节

- 每轮选择当前节点，调用 Executor，再使用 `edge.Resolve` 根据结果与上下文确定下一跳。
- 节点 Hook 在状态变化后触发，提供实时进度而不把 UI 回调混入执行器。
- 分支、审批或暂停节点以节点语义返回控制状态，Scheduler 保留可恢复的上下文。

## 依赖与验证

- 执行：[executor](../executor/README.md)
- 路由：[core/edge](../../core/edge/README.md)
- 验证：`go test ./workplan/runtime/scheduler/...`
