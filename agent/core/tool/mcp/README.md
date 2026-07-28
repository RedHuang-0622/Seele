# agent/core/tool/mcp

该包以 MCP（stdio 或 SSE）接入外部工具服务器。

## 实现要点

- `Provider` 维护服务器配置和客户端，按需连接并把 MCP tool 定义转换为 `ToolEntry`。
- `Handler` 在调用时执行 MCP 请求、传递 context 取消，并把结果归一为框架字符串结果。
- `breaker` 记录失败事件并实施熔断/恢复，避免持续请求不可用服务器。
- 内部 map 使用读写锁保护；Agent 关闭时统一关闭 Provider 资源。

## 公开入口与验证

- `NewProvider` 创建 MCP Provider；`ServerConfig` 描述一个服务器连接。
- 验证：`go test ./agent/core/tool/mcp/...`
