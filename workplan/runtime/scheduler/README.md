# workplan/runtime/scheduler

`Scheduler` 根据 Plan 内核中的边驱动依赖就绪、并发分支和结果汇总。

## 公开接口

| API | 用途 |
| --- | --- |
| `New` | 绑定 `core/plan.Plan` 和 Executor |
| `Run` | 从入口调度完整 Plan |
| `SetBranchRuntimeResolver` | 为节点分支注入只读运行时和 AgentFactory |
| `SetForkPolicy` / `SetForkJoinPolicy` | 配置分支失败与汇合策略 |

## 实现细节

- 同一轮中无依赖冲突的节点经 `forkexec` 并发运行，AutoNode 可收到分支级 AgentFactory。
- Scheduler 直接读取 Plan 的节点与边；Graph 外观只服务编辑和查询。

## 依赖与验证

- 内核：[../../core/plan/README.md](../../core/plan/README.md)
- 执行器：[../executor/README.md](../executor/README.md)
- 验证：`go test ./workplan/runtime/scheduler/...`
