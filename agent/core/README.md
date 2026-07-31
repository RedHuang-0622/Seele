# agent/core

`agent/core` 保存 LLM client、Provider 请求编解码和 Function Calling 响应解析能力；工具注册、权限、MCP 和 microHub 已迁移到根 `tools`。

## 子模块

| 目录 | 职责 |
| --- | --- |
| [`api/`](api/README.md) | HTTP LLM client、Provider strategy 与旧账户配置适配 |
| [`function/`](function/README.md) | Provider Function Calling 请求和响应编解码 |

## 依赖方向

- `agent` 可以装配这些 LLM client 能力。
- 工具运行时位于根 [`tools`](../../tools/README.md)，不得反向依赖 `agent`。
- 新账号池位于根 [`accountpool`](../../accountpool/README.md)，旧 API pool 仅作为迁移路径。

## 验证

- `go test ./agent/core/...`
