# agent/core/api

本包实现 LLM HTTP/SSE client 与 Provider 协议适配；账号并发状态由根级 [`accountpool`](../../../accountpool/README.md) 持有，本包只提供 client-specific adapter。

## 功能与边界

`ChatClient` 负责构建和发送一次 completion 请求，并通过 `ProviderStrategy` 处理 OpenAI、Anthropic 或调用方扩展协议。账号配置可以覆盖 BaseURL、API key、模型和请求参数，但 P2C 选择、并发信号量、启用状态与等待取消不在本包重复实现。

本包不负责会话历史、上下文压缩、工具选择、费用账本或产品级重试策略。上层应优先依赖 [`types.ChatCompleter`](../../../types/README.md) 等最小接口。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `NewChatClient` | 创建同步和 SSE completion client |
| `ChatClient.WithAccountPool` | 注入账号池 adapter |
| `ChatClient.SelectAccount` | 在当前 client 上设置指定账号，不改变全局池状态 |
| `ChatClient.SetProviderFilter` | 将 Provider 条件加入账号 `AcquireRequest` |
| `AccountPool.Acquire` | 从根级 P2C 池取得必须显式释放的账号租约 |
| `NewAccountPool` | 将 API `Account` 注册到根级 `P2CPool[*Account]` |
| `LoadAccountsConfig` / `LoadFullAccountsConfig` | 解析账号 YAML 和 LLM 默认参数 |
| `ProviderStrategy` | 注入请求编码、响应解析、认证头和 SSE 解析差异 |

账号配置的 `max_concurrency` 控制单账号同时持有的请求租约数；缺省值为 `DefaultMaxConcurrency`（当前为 `1`）。`max_rpm` 仅为旧配置兼容字段，不再由本包维护滑动窗口。

## 实现细节

- `AccountPool` 内部只持有 `accountpool.P2CPool[*Account]`，不再维护账号切片、round-robin cursor、RPM window 或另一份负载状态。`All`、`Lookup` 和旧 `Get` 方法仅用于配置/健康检查兼容；实际请求必须经过 `Acquire`。
- `Complete` 在选择账号后立即持有租约，直到 HTTP response body 读取和解析路径结束才释放；请求构造、连接和解析错误同样通过 `defer` 释放。
- 流式请求的账号、认证信息和 `ProviderStrategy` 来自同一次 Acquire。返回 body 由 `leasedReadCloser` 包装，在 EOF、读取错误或 `Close` 时幂等释放，因此不会在 strategy 选择和 HTTP 请求之间二次选择账号。
- Provider filter 写入 `AcquireRequest.Metadata["provider"]`，指定账号写入 `AcquireRequest.AccountID`。二者同时存在时必须匹配，否则根账号池返回语义错误。
- `Account.APIKey` 保留在根池的不透明 `Value` 中，不复制到公开快照 Metadata；快照只包含 provider、base URL 与 model 等路由信息。

## 依赖与协作

- 账号租约与 P2C：[accountpool](../../../accountpool/README.md)。
- 公共消息和 completion 接口：[types](../../../types/README.md)。
- Function Calling Provider 编码：[function](../function/README.md)。

依赖方向固定为 `agent/core/api -> accountpool`；根账号池不得反向导入 agent 或具体 Provider。

## 验证

- 包测试：`go test ./agent/core/api -count=1`
- 静态检查：`go vet ./agent/core/api`
- [`client_lease_test.go`](client_lease_test.go) 验证 HTTP/stream 整个生命周期持有同一租约以及错误路径释放。
- [`pool_test.go`](pool_test.go) 验证 P2C、并发上限、Provider/指定账号过滤与动态启停。
- [`config_test.go`](config_test.go) 验证 `max_concurrency` 的显式值和缺省值。
