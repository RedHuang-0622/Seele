# tools/adapter

`tools/adapter` 将 MCP、microHub、Skills 等远程工具目录与调用通道适配为根 `tools.ToolProvider`。它只依赖 catalog/invoker 接口，不依赖 `agent`、`seelectx`、工作区或任何产品 Task。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `Descriptor` | 传输无关的工具目录记录 |
| `Catalog`、`Invoker` | 远端发现与执行的最小接口 |
| `NewMCPProvider` | 构造 MCP 目录适配器 |
| `NewMicroHubProvider` | 构造 microHub 目录适配器 |
| `NewSkillsProvider` | 构造 Skills 目录适配器 |
| `WithNamespace` | 为 LLM 可见名称增加命名空间，避免多源重名 |

## 实现细节

- provider 创建时为空；调用方显式 `Refresh(ctx)` 拉取目录，失败时保留上一次成功快照。
- `RemoteName` 与 LLM 可见 `Name` 分离；命名空间只影响可见名，invoker 收到原始远程名。
- 适配器仅封装 Function Calling schema 和 handler，不定义 MCP/microHub/Skills 的连接、鉴权、审批或用户体验。
- MCP、microHub、Skills 三个构造函数共享实现但保留独立 kind 元数据，便于 trace、审计和后续策略选择。

## 依赖与验证

- 根合约：[`../README.md`](../README.md)
- 验证：`go test ./tools/adapter/...`、`go vet ./tools/adapter/...`
