# workplan/agent

`workplan/agent` 是 WorkPlan 与 Session Agent 之间的可选桥接层：它把调用方装配好的 `session.Agent` 变成 `workplan/core/node.AgentFactory`。WorkPlan 核心不会依赖 Agent；需要在 WorkPlan 中运行 ReAct agent 时才导入本包。

## 功能与边界

- `NewFactory` 只接收调用方拥有的 `session.Agent`，不创建 provider、账号池、工具注册表或全局状态。
- `Factory.NewAgent` 每次创建一个独立 Session，默认没有 durable history，适合并行节点和 fork 分支。
- 节点传入的非空 system prompt 覆盖桥接层配置的默认 system prompt；其它上下文组件可通过 `WithSessionComponents` 注入。
- `WithSessionID` 可将产品的 WorkPlan/node 标识映射到 Session ID；持久化 history 是显式选择，复用同一 history 可能导致节点间共享上下文。
- WorkPlan 的产品 DSL、节点语义、工具选择和重规划仍由上层 Seelex 解释；本包只负责装配和生命周期转接。

## 公开入口

| API | 用途 |
| --- | --- |
| `NewFactory(agent, opts...)` | 校验 `session.Agent` 并创建惰性 AgentFactory |
| `WithSessionComponents(...)` | 注入 prompt、context、telemetry 或 durable history |
| `WithSessionID(...)` | 自定义每次 Session 的 ID 生成策略 |
| `Factory.NewAgent(systemPrompt)` | 创建满足 `node.Agent` 的 Session |

`node.AgentFactory` 没有返回错误的位置，因此 Session 装配失败会被封装为一个返回原始原因的失败 Agent；常规依赖错误会在 `NewFactory` 阶段直接返回。

## 依赖与验证

- 上游：[`agent/README.md`](../../agent/README.md)、[`workplan/core/node/README.md`](../core/node/README.md)。
- `workplan/core` 不反向导入本包，避免 WorkPlan kernel 与 Agent 形成循环依赖。
- 验证：`go test ./workplan/agent/...`；完整仓库验证使用 `go test ./... -count=1`。
