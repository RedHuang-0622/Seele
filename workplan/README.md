# workplan

`workplan` 是无产品语义的 DAG 执行内核。`Plan` 持有节点、边和入口；`Node` 是最小执行单位；runtime 负责校验、调度、并发、取消、分支合并和 checkpoint。

## 职责边界

- `core/plan`：Plan 的构建、拓扑校验和 Seal。
- `core/node`：最小 `Node` 接口、可选 `ValueNode` 以及通用节点实现。
- `core/edge`：无状态边和条件解析。
- `runtime`：Executor、Scheduler、Runner、Fork 和 checkpoint。
- `codec`：产品 DSL、邻接表和邻接矩阵的导入导出；节点语义由调用方注入。
- `sugar`：可选的构建辅助，不是第二个状态源。

## 公共接口

| API | 用途 |
| --- | --- |
| `core/plan.New` / `Build` | 创建空 Plan 或从 Definition 构建并 Seal |
| `core/node.Node` | 自定义节点的最小执行契约 |
| `core/node.ValueNode` | 使用 JSON-backed `types.Value` 的结构化节点扩展 |
| `core/node.NewTypedFunctionNode[I,O]` | 泛型输入/输出的函数节点 |
| `codec.Document[T]` | 产品友好的 `nodes + edges` DSL |
| `runtime/runner.Runner` | 从入口执行或从 checkpoint 恢复 |
| `WithEventSink` / `WithEventLocators` | 向一次 WorkPlan 运行装配统一事件 Sink 与模块定位信息 |

## 数据传输

WorkPlan 内部优先维护 `WorkflowContext.Prev`、`Results` 和 `Variables` 这三个 `types.Value` 容器。`PrevOutput`、`PrevResults`、`Vars` 仍作为兼容镜像，旧节点可以继续实现字符串接口；新节点应优先实现 `ValueNode` 或使用 `NewTypedFunctionNode`。

## 与 Seelex 协作

Seele 不解释 `task`、`auto`、`agent`、文件或工作区语义。Seelex 通过 `codec.NodeFactory[T]` 将产品 JSON 装配成 Node，再交给 `Plan` 和 runtime 执行。

## 事件装配

WorkPlan 不保存 EventStore，也不决定 Task 状态。调用方可在构建时装配根级 [`event`](../event/README.md) 的 Sink；Sink 通常由 Seelex 适配到追加事件库、日志或投影层。

```go
sink := event.SinkFunc(func(ctx context.Context, e event.Event) error {
    return nil // Seelex EventStore / logger / projection adapter
})

plan := workplan.New(factory,
    workplan.WithEventSink(sink, "plan-42"),
    workplan.WithEventRunID("run-7"),
    workplan.WithEventHeartbeatPolicy(event.HeartbeatPolicy{Interval: 15 * time.Second}),
    workplan.WithEventLocators(
        agent.EventLocator{AgentID: "main-agent"},
        workplan.EventLocator{PlanID: "plan-42", RunID: "run-7"},
    ),
)
```

`plan_id` 必须由调用方提供；未指定 `run_id` 时由每次运行的 Recorder 生成。DAG 节点会输出 queued、running 和终态事件；活动节点可选地输出共享 ticker 驱动的 heartbeat。事件中的 `Failure` 是控制流 error 的安全投影，不能替代 `Run`/`Resume` 返回的 error。完整 Sink 语义和异步投递约束见 [`event README`](../event/README.md)。

## 验证

```powershell
go test ./workplan/... -count=1
```

详细 DSL 说明见 [codec README](codec/README.md)。
