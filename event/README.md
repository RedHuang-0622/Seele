# event

`event` 提供无产品语义、可序列化的运行时事件契约。它负责把执行事实标准化并投递给调用方注入的 Sink；它不负责 EventStore、Task、KV 投影、日志 UI 或产品告警策略。

## 公开入口

| API | 用途 |
| --- | --- |
| `Event` | 不可变的结构化执行事实，含 ID、顺序、关联范围、状态、内容和安全错误投影。 |
| `Sink` / `SinkFunc` / `MultiSink` | 调用方实现的事件接收端；可用于追加存储、日志或转发。 |
| `Recorder` / `NewRecorder` | 为一次运行生成 Event ID、Run ID、时间戳和有序 Sequence，并投递到 Sink。 |
| `Locator` / `LocatorFunc` | 由领域模块定义的强类型资源定位扩展点。 |
| `HeartbeatPolicy` / `HeartbeatLease` | 用一套共享 ticker 发布活动作用域的存活事件。 |
| `FailureFrom` | 将控制流 `error` 转为安全、可序列化的 `Failure` 投影。 |

## 装配 Locator

事件的 `Scope` 保留查询友好的通用关联键，例如 `plan_id`、`run_id` 与 `node_id`。特定模块的地址不应继续向 `Event` 添加字段，而是实现 `Locator`：

```go
type EventLocator struct {
    AgentID   string
    SessionID string
}

func (l EventLocator) Locate() event.Location {
    return event.Location{
        Kind: "agent.runtime",
        IDs: map[string]string{
            "agent_id": l.AgentID,
            "session_id": l.SessionID,
        },
    }
}
```

仓库已提供 [`agent.EventLocator`](../agent/event_locator.go) 与 [`workplan.EventLocator`](../workplan/event_locator.go)。调用方可以同时注入多个 Locator；Recorder 把它们的 `Location` 附加到每一个事件。这让 Agent、WorkPlan 或未来模块维持自己的类型和命名空间，同时只依赖本包的最小接口。

## Sink 与顺序语义

```go
sink := event.SinkFunc(func(ctx context.Context, e event.Event) error {
    // 由 Seelex 适配为 EventStore 追加、日志或 Task 投影。
    return nil
})

recorder, err := event.NewRecorder(sink,
    event.WithScope(event.Scope{PlanID: "plan-42"}),
    event.WithLocators(agent.EventLocator{AgentID: "main-agent"}),
)
if err != nil {
    return err
}
defer recorder.Close()

recorder.Publish(ctx, event.Event{
    Type: event.TypeLifecycle,
    Status: event.StatusRunning,
})
```

同一个 `Recorder` 会串行调用同步 `Sink.Append`，因此接收端观察到的 `Sequence` 严格递增。该选择优先保证一次运行内的事实顺序；慢 Sink 会反压发布者。需要异步投递时，调用方应以有界队列和明确的丢弃/重试策略包装 Sink，而不是让框架隐式创建无界后台队列。

Sink 投递失败会交给 `WithErrorHandler`，不会改写 WorkPlan 或 Agent 的控制流结果。`MultiSink` 依次投递所有 Sink 并汇总投递错误。Sink 不得修改收到的事件。

## 错误、内容与心跳

- `errors` 仍是控制流错误层；`Event.Failure` 只是其安全投影，不携带 `Raw` 或 `Cause`。
- `Content` 必须是 JSON。大体积或敏感内容应由调用方自行存储，并只填 `ContentRef`。
- `StartHeartbeat` 返回 lease。lease 存活期间，Recorder 的单个共享 ticker 发出 `TypeHeartbeat`/`StatusRunning`；终态必须调用 `Stop`。心跳只证明运行时尚未到达终态，不代表 LLM、工具或节点产生了真实业务进度。
- 关闭 Recorder 会停止共享 ticker 并拒绝后续事件。它不拥有 Sink 的生命周期。

## 协作与验证

- WorkPlan 把它的 node 生命周期映射为本包事件，见 [`../workplan/README.md`](../workplan/README.md)。
- Agent 可作为事件定位信息的提供方，见 [`../agent/README.md`](../agent/README.md)。
- 跨模块的依赖边界和 Seelex EventStore/投影责任见 [`../docs/arch/12-event-contracts.md`](../docs/arch/12-event-contracts.md)。

```powershell
go test ./event ./workplan/runtime/runner ./workplan/runtime/forkexec -count=1
go vet ./event ./workplan/...
```
