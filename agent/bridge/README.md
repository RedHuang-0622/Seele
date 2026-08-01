# agent/bridge

`agent/bridge` 提供 Agent 装配层与平行基础模块之间的官方适配器；它依赖适配两端，但 `agent`、`tools` 等被适配模块不会反向依赖本包。

## 功能与边界

当前的 `RegistryRuntime` 将根 [`tools.Registry`](../../tools/README.md) 适配为 `agent.ToolRuntime`。它解决 `Tools() + Dispatch(ctx, tools.ToolCall)` 与 `VisibleTools(ctx) + Dispatch(ctx, name, argsJSON)` 两种契约之间的形状差异。`AccountCompleter` 则把 [`accountpool.ClientResolver`](../../accountpool/README.md) 中的同步 LLM client 适配为 `agent.Completer`，并确保每次补全结束后释放账号 lease。`AgentFactory` 把装配好的 `session.Agent` 适配为 [WorkPlan](../../workplan/README.md) 的 `node.AgentFactory` 契约，使每个 Plan 节点获得一个独立 Session，且 WorkPlan 内核不依赖 Agent 运行时。

这些适配器不注册或刷新 Provider，不连接 MCP/microHub，不实现权限审批，也不解释工具返回值；这些能力仍由 Registry 及其 middleware 或上层产品装配负责。`NewAgentFactory` 只接收调用方拥有的 `session.Agent`，不创建 provider、账号池、工具注册表或全局状态；WorkPlan 的产品 DSL、节点语义、工具选择和重规划仍由上层 Seelex 解释。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `NewRegistryRuntime` | 从 `tools.Registry` 或兼容的窄接口创建 `agent.ToolRuntime` |
| `WithVisibilityPolicy` | 为每个请求选择模型可见且可分发的工具集合 |
| `ErrToolNotVisible` | 调用未在当前请求可见工具时返回的可判定错误 |
| `NewAccountCompleter` | 从账号池或硬编码 `StaticResolver` 创建同步 `agent.Completer` |
| `WithAccountRequestSelector` | 将调用方定义的账号选择条件传给每次 completion |
| `NewAgentFactory` | 从装配好的 `session.Agent` 创建 WorkPlan `node.AgentFactory` |
| `WithSessionComponents` | 为每个 WorkPlan 节点会话注入可选组件（prompt blocks、durable history 等） |
| `WithSessionID` | 从节点 system prompt 派生会话 ID，用于持久化 checkpoint |

```go
registry := tools.NewRegistry()
// 显式注册 builtin、MCP 或产品 Provider。

runtime, err := bridge.NewRegistryRuntime(registry,
	bridge.WithVisibilityPolicy(selectToolsForRequest),
)
if err != nil {
	return err
}
agt, err := agent.NewWithComponents(agent.Components{
	Completer: client,
	Tools:     runtime,
})
```

## 实现细节

- `VisibleTools` 在每次请求时读取 Registry 的 public snapshot，再执行 `VisibilityPolicy`；因此 Provider 的显式 `Refresh` 或注册后的新快照会被下一次请求使用。
- `Dispatch` 使用同一策略再次验证工具名，保证隐藏的 Registry 条目（如 `_checkpoint`）及策略排除项不能仅靠模型猜测名称执行。工具的实际超时、重试和 middleware 仍由 `Registry.Dispatch` 处理。
- `AccountCompleter` 对每次 `Complete` 调用一次 `ClientResolver.Resolve`，并在成功、失败或取消后释放 lease；账号路由条件由 `WithAccountRequestSelector` 注入，框架不解释账号、密钥或产品费用归属。流式 client 需要独立适配，因为其 lease 必须覆盖整条流。
- 适配器在构造时拒绝空的 `Registry`；默认策略只暴露 Registry 的 public `Tools()`，而不是其全部可分发条目。共享一个 runtime 时，调用方提供的 visibility policy 必须自行保证并发安全。
- `AgentFactory.NewAgent` 为每个节点克隆 SessionComponents（浅拷贝并复制 PromptBlocks），并强制覆盖 `Agent`；默认不注入 durable history，因此并行节点天然隔离。`systemPrompt` 非空时覆盖配置的 `Context.SystemPrompt`；节点会话构造失败以携带精确错误的 `node.Agent` 返回，而非中断计划。

## 依赖与验证

- Agent 装配契约：[`../README.md`](../README.md)
- 工具注册与分发：[`../../tools/README.md`](../../tools/README.md)
- WorkPlan 节点契约：[`../../workplan/README.md`](../../workplan/README.md)
- 验证：`go test ./agent/bridge/...`、`go vet ./agent/bridge/...`
