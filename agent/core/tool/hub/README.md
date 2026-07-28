# agent/core/tool/hub

该包把 microHub 服务注册表中的远程 Skill 暴露为 Seele 工具。

## 实现要点

- `HubProvider` 从 `BaseHub` 获取服务元数据并生成 `ToolEntry`；状态变化后可重建工具集合。
- `HubToolHandler` 负责带超时的 gRPC 调用和响应转换。
- `hubRouter` 将 Hub 请求路由到已注册 Handler，隔离 Hub 协议细节。

这是远程工具适配器；本地工具与 MCP 工具分别见 [builtin/](../builtin/README.md) 与 [mcp/](../mcp/README.md)。

## 公开入口与验证

- `NewHubProvider` 创建 Provider，`NewHubRouter` 创建 Hub 路由处理器。
- 验证：`go test ./agent/core/tool/hub/...`
