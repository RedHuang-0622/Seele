# workplan/runtime/runner

## 事件装配

`EventConfig` 与 `WithEventSink` 让调用方装配自己持有的 Sink、Plan/Run ID、Locator、心跳策略和投递错误处理器。配置 Sink 后，`PlanID` 必须非空；`Run` 与 `Resume` 各自创建独立 Recorder，并将其放进运行 context。

普通 `Run` 经 `forkexec` 输出节点生命周期事件；`Resume` 的直接节点执行路径也输出 running、终态和可选 heartbeat。Sink 投递失败只交给 `EventConfig.ErrorHandler`，不会改变 `Run`/`Resume` 的控制流结果。根事件契约见 [`event`](../../../event/README.md)。

## 概览

`Runner` 是 Plan 内核的执行入口，提供全新运行和从检查点恢复的能力。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 绑定 `core/plan.Plan` 和可选能力 |
| `Runner.Plan` | 获取绑定的执行内核 |
| `Runner.Run` | 校验并执行完整 Plan |
| `Runner.Resume` | 从已保存检查点继续 |
| `Option` | 注入检查点及观察能力 |

## 实现细节

- Runner 校验并传递 Plan 给 Scheduler，不接收或保存 AgentFactory；节点自行持有执行依赖。
- 检查点由 Manager 独立处理，Scheduler 不感知存储后端。

## 依赖与验证

- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- 调度：[../scheduler/README.md](../scheduler/README.md)
- 验证：`go test ./workplan/runtime/runner/...`
