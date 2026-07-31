# tools/mcp

`mcp` 将 stdio 或 SSE MCP Server 的工具目录和调用能力适配为根 `tools.ToolProvider`；它不持有 Agent、会话或工作区状态。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `NewProvider` | 创建多 Server MCP provider |
| `ServerConfig` | 声明 stdio/SSE 连接参数 |
| `Attach`、`Detach` | 显式建立或释放 Server 连接 |
| `RefreshTools` | 重新读取指定 Server 的工具目录 |
| `ServerStatus`、`IsAlive` | 查询连接和工具状态 |

## 实现细节

- Provider 缓存每个 Server 的 MCP client 和工具定义；多 Server 时用 `server__tool` 避免名称冲突。
- Handler 将 JSON 参数转换为 MCP `CallToolRequest`，透传 context 并聚合文本响应。
- 熔断器按 Server 隔离连续连接故障，以指数退避和后台 ping 恢复，业务错误不会污染连接健康度。
- Server 生命周期必须由调用方显式管理；构造 Provider 不建立网络连接。

## 依赖与验证

- 根合约：[`../README.md`](../README.md)
- 通用远端适配：[`../adapter/README.md`](../adapter/README.md)
- 验证：`go test ./tools/mcp/...`
