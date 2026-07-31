# agent

`agent` 是 LLM client、工具 runtime 和用户会话的大装配层；新主线不实现或持有 tools provider、账号池算法和产品上下文策略。

## 显式装配

`NewWithComponents(Components)` 只接受 consumer-facing 的 `Completer`、可选流式能力和 `ToolRuntime`，不会启动 microHub、读取 registry、创建账号池或注册产品工具。会话由调用方使用 `session.NewSession(session.SessionComponents)` 显式装配；`*agent.Agent` 仅通过方法集满足 `session.Agent`。Hub 与旧账号池字段仅属于待移除的 `New(Options)` legacy 路径。

`agent` 是 LLM、工具和账户路由的装配层。新代码使用 `NewWithComponents` 创建无副作用 runtime，再由 `session.NewSession` 创建用户会话；`agent.New(Options)` 仅保留为 legacy 基础设施入口。

## 实现要点

- `New` 依次创建账户池、API 网关、工具 Holder、Hub Provider 和 `ChatClient`；`Options` 支持单账户配置或 YAML 多账户配置。
- API 网关从账户池选择账户，工具网关根据插件和权限过滤可见工具；Agent 本身不实现具体协议或工具逻辑。
- `Dispatch` 以 `WaitGroup` 追踪进行中的调用；`Shutdown` 关闭信号、等待调用完成，再回收 MCP/Hub 资源，避免关闭期间的竞态。
- `Agent` 不导入 `session`；它只提供 `LLM`、`VisibleTools` 和 `Dispatch`，因而天然满足 `session.Agent`。Session、durable history 与 context policy 的生命周期均属于调用方。
- `Pool` 为多个 Agent 会话提供复用与摘要统计；实际会话状态由 `session`/`seelectx` 管理。
- `agent.Session` 是为 `Pool` 保留的旧最小接口；它与根 `session` 包的会话对象没有所有权关系。

子系统见 [core/](core/README.md)（协议与工具）、[gateway/](gateway/README.md)（选择与可见性边界）和 [bridge/](bridge/README.md)（与平行模块的官方装配适配器）。

## 公开入口与验证

- `NewWithComponents` 创建无副作用 Agent；`LLM`、`Tools`、`VisibleTools`、`Dispatch` 提供 runtime 能力；调用方将该 runtime 注入 `session.NewSession`；`Shutdown` 释放 Agent 自己拥有的资源。
- 验证：`go test ./agent/...`
