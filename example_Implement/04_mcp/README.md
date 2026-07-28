# 04_mcp

该示例展示将 MCP 服务器连接到 Agent 后，把远程工具作为普通 Agent 工具使用。

## 运行

```powershell
go run ./example_Implement/04_mcp
```

运行前根据 `main.go` 配置可访问的 MCP 服务端；不要提交本地命令、令牌或服务地址。

## 实现细节

- 示例通过 MCP Provider 连接服务器并将发现的工具注册进统一 Holder。
- 后续 ReAct 调用不区分工具来自 MCP 还是本地 Provider，验证协议适配边界。

## 验证

- `go build ./example_Implement/04_mcp`
