# workplan/runtime/runner

`Runner` 是 Plan 内核的执行入口，提供全新运行和从检查点恢复的能力。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 绑定 `core/plan.Plan`、AgentFactory 和可选能力 |
| `Runner.Plan` | 获取绑定的执行内核 |
| `Runner.Run` | 校验并执行完整 Plan |
| `Runner.Resume` | 从已保存检查点继续 |
| `Option` | 注入检查点及观察能力 |

## 实现细节

- Runner 校验并传递 Plan 给 Scheduler，不依赖 Graph 外观。
- 检查点由 Manager 独立处理，Scheduler 不感知存储后端。

## 依赖与验证

- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- 调度：[../scheduler/README.md](../scheduler/README.md)
- 验证：`go test ./workplan/runtime/runner/...`
