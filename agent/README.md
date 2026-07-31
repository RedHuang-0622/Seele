# agent

`agent` 是 LLM client 与可选 runtime 能力的大装配层；新主线不实现或持有 tools provider、账号池算法和产品上下文策略。

## 显式装配

`NewWithComponents(Components)` 只接受 consumer-facing 的 `Completer`、可选流式能力和 `ToolRuntime`，不会启动 microHub、读取 registry、创建账号池或注册产品工具。账号池、根 `tools` provider 和 seelectx 会话由调用方装配后注入；Hub 与旧账号池字段仅属于待移除的 `New(Options)` legacy 路径。

`agent` 是 LLM、工具和账户路由的装配层。调用方以 `agent.New(Options)` 创建实例，再通过 `LLM`、`Tools`、`VisibleTools`、`Dispatch` 和 `Shutdown` 使用其能力。

## 实现要点

- `New` 依次创建账户池、API 网关、工具 Holder、Hub Provider 和 `ChatClient`；`Options` 支持单账户配置或 YAML 多账户配置。
- API 网关从账户池选择账户，工具网关根据插件和权限过滤可见工具；Agent 本身不实现具体协议或工具逻辑。
- `Dispatch` 以 `WaitGroup` 追踪进行中的调用；`Shutdown` 关闭信号、等待调用完成，再回收 MCP/Hub 资源，避免关闭期间的竞态。
- `Pool` 为多个 Agent 会话提供复用与摘要统计；实际会话状态由上层 `engine`/`seelectx` 管理。

子系统见 [core/](core/README.md)（协议与工具）和 [gateway/](gateway/README.md)（选择与可见性边界）。

## 公开入口与验证

- `New` 创建 Agent；`LLM`、`Tools`、`VisibleTools`、`Dispatch` 提供调用能力；`Shutdown` 释放资源。
- 验证：`go test ./agent/...`
