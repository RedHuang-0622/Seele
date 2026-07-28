# agent/gateway/api

该包定义账户访问网关。`DefaultGateway` 包装 `api.AccountPool`，将“选择当前账户”的职责从 HTTP 客户端和 Agent 装配中分离。

## 实现要点

- `Gateway` 是窄接口，便于测试或替换选择策略。
- 默认实现只委托账户池的选择和状态能力；Provider 请求格式仍由 `agent/core/api` 的策略处理。
- 因此调用方不会直接持有和修改账户池内部状态。

## 公开入口与验证

- `Gateway` 定义选择接口，`NewDefaultGateway` 创建默认实现。
- 验证：`go test ./agent/gateway/api/...`
