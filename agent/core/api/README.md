# agent/core/api

该包实现 LLM 请求层：`ChatClient`、账户池和 Provider 协议策略。

## 实现要点

- `ProviderStrategy` 把请求构建、响应解析、SSE 帧解析和端点差异封装为策略；内置 OpenAI 与 Anthropic，并可注册新策略。
- `ChatClient` 在发送前根据显式 Provider 或账户选择策略，支持同步与 SSE 流式完成；流式路径累计分帧 tool call 参数。
- `AccountPool` 用锁保护账户状态，按优先级和 RPM 可用性选择账户；账户可覆盖基础 LLM 配置。
- `LoadAccountsConfig`/`LoadFullAccountsConfig` 解析 YAML，将账户列表与默认 LLM 参数拆分为可注入对象。

上层只应依赖 `types.ChatCompleter` 或 `ChatClient` 的公开方法；工具的 Provider 格式转换由同级 [function/](../function/README.md) 负责。

## 公开入口与验证

- `NewChatClient`、`AccountPool`、`ProviderStrategy` 与配置加载函数是主要扩展/构造入口。
- 验证：`go test ./agent/core/api/...`
