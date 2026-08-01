# 事件契约与可观测性装配

## 架构决定

根级 `event` 包是 Seele 的通用运行时观察契约。它记录可序列化的执行事实，但不引入 Task、事件数据库、KV 模型、审计 UI 或产品级告警语义。

依赖方向固定为：

```text
errors  <-  event  <-  agent / session / tools / workplan
                             ^
                             |
              Seelex implements Sink, EventStore, and projections
```

`errors` 保持控制流职责：调用方仍可包装、返回、匹配 Go error。`event.Failure` 只是可安全写入日志和事件库的投影，明确排除可能敏感或不可序列化的 `Raw` 与 `Cause`。

## 核心模型

`event.Event` 的一等字段是 `ID`、`Sequence`、`OccurredAt`、`Type`、`Status`、`Scope`、`Content`、`ContentRef` 与 `Failure`。

- `Scope` 放稳定且便于索引的通用关联 ID：Trace、Run、Plan、Node、Branch、Agent、ToolCall。
- `Locations` 是可组合的模块定位信息。模块定义自己的强类型 struct 并实现 `event.Locator`，不修改根事件结构。
- `Content` 是可选 JSON；大内容和敏感内容只能由调用方持久化，并通过 `ContentRef` 引用。

因此 Agent 可以提供 `agent.EventLocator`，WorkPlan 可以提供 `workplan.EventLocator`。二者都只依赖 `event.Locator`，相互之间没有依赖。Seelex 也可以增加自己的 Locator，例如工作区、任务或用户范围，而不把这些产品语义带入 Seele。

## 投递与顺序

每个 `Recorder` 属于一次运行。它生成 Run ID（若调用方未提供）、事件 ID、时间戳和单调递增的 Sequence，并以同步方式调用调用方提供的 `Sink`。

同步投递的含义是：同一 Recorder 的 Sink 观察到严格递增的 Sequence；代价是慢 Sink 会对节点生命周期埋点产生背压。异步队列、批量写入、失败重试、事件落库和丢弃策略均由 Seelex 在 Sink 适配层显式选择。Sink 投递错误只进入 ErrorHandler，绝不替代执行路径上的业务错误。

Seelex 应以只追加 EventStore 保存事件事实，再将需要快速读取的状态投影到 KV 或数据库。单独使用可覆盖的 KV 会丢失审计与顺序信息。

## 心跳

心跳由 `Recorder` 内部的 `HeartbeatManager` 统一管理。一个 Recorder 只有一个 ticker；节点开始时申请 `HeartbeatLease`，进入终态时释放 lease。这样并行节点不会各自创建 ticker 或 goroutine。

心跳事件表达“当前 runtime 仍未结束”，而不是模型生成进度、工具 I/O 进度或业务完成百分比。消费者必须把它当作存活信号，不能用作 Task 状态的唯一事实来源。

## WorkPlan 适配

WorkPlan 的 `runner` 接受 `EventConfig`：调用方提供 Sink、Plan ID、可选 Run ID、心跳策略、投递错误处理器和 Locator。普通 DAG 节点经 `forkexec` 映射为 queued、running、completed、failed、canceled 或 panicked 的生命周期事件；`Resume` 直接执行节点的路径也生成相同的根事件。

Plan ID 是 Seelex 或其他产品层提供的标识；Seele 不创建产品 Plan 或 Task。Run ID 未指定时由每次执行创建的 Recorder 生成。

## 验收

```powershell
go test ./event ./workplan/runtime/runner ./workplan/runtime/forkexec -count=1
go test ./event ./agent ./workplan/... -count=1 -timeout 180s
go vet ./event ./agent ./workplan/...
```

验收覆盖事件的 ID/Sequence/Scope/Locator 正规化、Failure 的脱敏投影、共享心跳、WorkPlan 节点生命周期映射以及缺失 Plan ID 的配置错误。
