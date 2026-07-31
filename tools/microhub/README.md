# tools/microhub

`microhub` 将 microHub registry 中的远程 Skill 暴露为根 `tools.ToolProvider`，负责 Schema 转换、路由与 gRPC 调用；它不决定 Skill 的产品语义或选择策略。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `NewHubProvider` | 基于 `BaseHub` 创建工具 Provider |
| `NewHubRouter` | 创建 microHub 请求路由器 |
| `Skills` | 导出当前在线 Skill 摘要 |
| `Retire`、`Restore` | 在本地快照中停用或恢复远程 Skill |

## 实现细节

- Provider 从 microHub registry 读取在线工具，把输入 Schema 转换为 Function Calling 参数定义。
- `HubToolHandler` 为调用派生 timeout，聚合成功或部分响应，并把传输故障标记为 `tools.ErrUnavailable`。
- retire 状态由读写锁保护，离线或已 retire 的 Skill 不会进入 Provider 工具列表。
- Hub 服务启动、registry 初始化和健康探测由调用方装配，不在 Agent 或 Provider 构造时隐式执行。

## 依赖与验证

- 根合约：[`../README.md`](../README.md)
- MCP provider：[`../mcp/README.md`](../mcp/README.md)
- 验证：`go test ./tools/microhub/...`
