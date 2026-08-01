# Seelex 集成指南

## 目的与装配顺序

Seelex 是 Seele 的产品层：它拥有 Task、工作区、用户会话、模型账户配置、工具实现、上下文策略、EventStore 和计费账本。Seele 只提供无产品语义的执行原语与可替换接口。

建议按以下顺序装配，而不是让任一框架模块自动发现配置或创建产品资源：

```text
Seelex provider/client adapter ─┐
Seelex account routing policy ──┼─> accountpool / agent bridge ─> agent.Agent
Seelex tool providers ──────────┼─> tools.Registry ──────────────┘
Seelex history + context policy ─┴─> session.Session
Seelex task JSON + node factory ───> workplan.WorkPlan
Seelex EventStore / projections ───> event.Sink
```

其中 `session` 是一次对话的装配容器，`agent` 是 LLM 与工具运行时，`workplan` 是 DAG 内核。产品任务、TaskList、工作区与用户权限不应进入 Seele 的根模块。

## agent：装配 LLM 与工具运行时

Seelex 实现或适配 `agent.Completer`，并把它和请求可见的 `agent.ToolRuntime` 注入 `agent.NewWithComponents`。框架不会读取密钥、账号配置、工作区或工具注册表。

```go
runtime, err := bridge.NewRegistryRuntime(registry,
    bridge.WithVisibilityPolicy(selectToolsForRequest),
)
if err != nil {
    return err
}

agt, err := agent.NewWithComponents(agent.Components{
    Completer: seelexCompleter,
    Tools:     runtime,
})
if err != nil {
    return err
}
```

`seelexCompleter` 可以是直接持有配置的 client，也可以是账户池适配器。流式 client 应同时实现 `StreamCompleter` 或 `StreamEventCompleter`；不要把同步 completion 的租约适配器误用于整段流式连接。

- 模块 API 与边界：[agent README](../../agent/README.md)
- 官方装配桥接：[agent/bridge README](../../agent/bridge/README.md)

## accountpool：提供本地账户路由

Seelex 创建实际 provider client，并作为 `accountpool.Account[agent.Completer].Value` 注册。账号的 key、base URL、model、成本归属和选择策略属于 Seelex；只有非敏感路由信息可放进 `Metadata`。

```go
pool := accountpool.New[agent.Completer]()
if err := pool.Register(accountpool.Account[agent.Completer]{
    ID: "account-a", Value: clientA, MaxConcurrency: 4,
    Metadata: map[string]string{"provider": "provider-a"},
}); err != nil {
    return err
}

completer, err := bridge.NewAccountCompleter(pool,
    bridge.WithAccountRequestSelector(selectAccountForRequest),
)
if err != nil {
    return err
}
```

仅一个硬编码 client 时，用 `accountpool.NewStaticResolver` 适配为同一接口即可。Seelex 必须让租约覆盖完整网络资源生命周期；若自行实现流式适配器，lease 必须直到 stream EOF 或 Close 才释放。

- 模块 API 与并发约束：[accountpool README](../../accountpool/README.md)

## tools：注册产品工具并控制调用

文件、Git、工作区、审批、用户数据和业务系统工具均由 Seelex 实现为 `tools.ToolHandler` 或 `tools.ToolProvider`，再显式注册到 `tools.Registry`。MCP、microHub 与 Skills 同样通过 provider/adaptor 接入；连接、刷新和关闭由 Seelex 生命周期管理。

```go
registry := tools.NewRegistry(
    tools.WithCallTimeout(toolTimeout),
    tools.WithMiddleware(auditMiddleware),
)
if err := registry.Register(seelexWorkspaceProvider); err != nil {
    return err
}
if err := registry.Refresh(ctx); err != nil {
    return err
}
```

使用 `bridge.NewRegistryRuntime` 将 Registry 变成 Agent 所需的 `ToolRuntime`。可见性策略既决定发给模型的工具，也在分发前再次校验；Seelex 应在 middleware 或 provider 中实现审批、审计和业务权限。工具原始输出可由 `seelectx.ToolResultProcessor` 过滤或改为引用后再送入模型。

- 模块 API：[tools README](../../tools/README.md)
- 工具到 Agent 的桥接：[agent/bridge README](../../agent/bridge/README.md)

## seelectx 与 session：拥有会话与上下文策略

Seelex 是 `DurableHistory`、缓存、持久化、上下文压缩触发、压缩 Profile 和 token 账本的唯一所有者。它通过 `session.NewSession` 注入 history、prompt blocks、request assembler、工具结果处理器与 context controller；Session 不会主动创建这些对象或自动压缩。

```go
conversation, err := session.NewSession(session.SessionComponents{
    Agent: agt,
    History: seelexHistory,
    Context: session.ContextComponents{
        SystemPrompt:        systemPrompt,
        PromptBlocks:        blocksForTaskAndSkill,
        Assembler:           seelexAssembler,
        ToolResultProcessor: seelexToolResultProcessor,
        Compressor:          seelexCompressor,
        Controller:          seelexContextController,
    },
    SessionID: seelexSessionID,
})
```

压缩应由 Seelex 的 ContextService 显式发起：选取 checkpoint 和有界历史，以无工具、独立 history 的临时 completion 运行压缩 Profile，校验结构化结果后写入 ContextSnapshot 和 TokenLedger。短对话直接保留原文；递归压缩和预算判断也由 Seelex 策略决定。

同一 `session.Session` 不可并发 Chat；并发子代理须创建独立 Session。若多个 Session 共用 DurableHistory，冲突解决和持久化一致性由 Seelex 的 history 实现承担。

- 上下文原语：[seelectx README](../../seelectx/README.md)
- 会话生命周期：[session README](../../session/README.md)

## workplan：把产品工作流材料化为通用 DAG

Seelex 解释 Task JSON 或产品 DSL 的图语义，并通过 `codec.NodeFactory[T]` 把节点材料化为 `node.Node`。节点可为自定义节点，或装配 Seele 提供的 function/auto/agent 实现；WorkPlan 内核不解释 Task、工作区或重规划。

```go
plan, err := codec.Import(payload, codec.NodeFactoryFunc[SeelexNodeInput](buildNode))
if err != nil {
    return err
}

factory, err := agentbridge.NewFactory(agt,
    agentbridge.WithSessionComponents(nodeSessionComponents),
)
if err != nil {
    return err
}
workflow := workplan.NewFromPlan(plan, factory)
```

使用 `workplan/agent.NewFactory` 时，每个 Agent 节点默认获得独立 Session，因此适合 DAG 并发和 Fork。只有 Seelex 明确需要共享上下文时才注入同一 DurableHistory。Task 打点、节点业务状态和重规划仍属于 Seelex；WorkPlan 只返回运行结果和通用事件。

- DAG、Node 与 codec：[workplan README](../../workplan/README.md)
- Agent 节点桥接：[workplan/agent README](../../workplan/agent/README.md)

## event：持久化执行事实并建立投影

Seelex 实现 `event.Sink`，在创建 WorkPlan 时注入。Seele 的 Recorder 在运行时同步调用 `Sink.Append`；Seelex 决定追加 EventStore、日志、队列、重试、批处理与 KV 投影策略。

```go
workflow := workplan.New(factory,
    workplan.WithEventSink(seelexEventSink, planID),
    workplan.WithEventRunID(runID),
    workplan.WithEventLocators(
        agent.EventLocator{AgentID: agentID, SessionID: sessionID},
        workplan.EventLocator{PlanID: planID, RunID: runID},
    ),
)
```

推荐先追加不可变事件，再异步或同步维护 Task/节点状态投影。不要只把最新状态覆盖到 KV，否则会丢失有序审计事实。Sink 故障会进入 ErrorHandler 而不改变 WorkPlan 控制流；若写入可能慢，Seelex 应以有界队列 Sink 包装并明确背压、重试和丢弃策略。

- 事件 API：[event README](../../event/README.md)
- 事件边界：[事件契约](12-event-contracts.md)

## errors：转换为产品错误与用户反馈

Seele 返回的结构化 `errors.Error` 适合 Seelex 做错误码映射、重试分类、日志关联和用户提示。Seelex 可以包裹错误来补充自己拥有的 `Code`、`Step` 或 `Path`，但不应依赖字符串匹配，也不应把 API key、完整 provider payload 或其他敏感数据写入可外发日志。

控制流错误仍应正常返回；观察事件只携带经过 `event.FailureFrom` 脱敏后的 Failure 投影。产品界面、告警和 Task 状态根据两者与产品策略综合决定。

- 模块 API：[errors README](../../errors/README.md)

## 最小集成检查

发布 Seelex 集成前，至少验证：

1. 每个 client、registry、history、Sink 都由 Seelex 显式创建并拥有生命周期。
2. 所有环境相关工具均在 Seelex provider 中，不在 Seele builtin 中。
3. 主会话、子代理、压缩会话的 history、工具权限和 token 账本隔离正确。
4. WorkPlan 的 Plan/Run ID、Agent/Node Locator 和 EventSink 都已注入。
5. 事件库为追加式；KV 只作为可重建投影。
6. 同一 Session 没有并发 Chat，账户与流式资源的 lease 均被释放。

框架验证命令：

```powershell
go test ./... -count=1 -timeout 300s
go vet ./...
go build ./...
```
