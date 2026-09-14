# limits

限流与并发的**路数装配**：把请求速率（RPM）、令牌速率（TPM）、加权在途并发、
排队超时与 429/Retry-After 重试分类，装配到任意 `types.ChatCompleter` 上。

## 与 accountpool 的分工

| 层 | 拥有什么 | 不拥有什么 |
| --- | --- | --- |
| `accountpool` | **账号槽位**并发：每个在途调用按账号持有一个租约（`MaxConcurrency`） | 请求速率、令牌速率、队列语义 |
| `limits` | **provider 成本**准入：RPM、TPM、加权在途上限（含图片权重）、排队超时、429 重试分类 | 账号选择与路由（P2C、角色、分支） |

两者互不替换：账号池回答“用哪个账号”，`limits` 回答“现在允不允许发、发了算多少额度”。
`Account.MaxRPM` 在 `agent/core/api` 里已标 Deprecated，本包就是它原本要接的地方。

## 公共接口

| API | 用途 |
| --- | --- |
| `Params` | 参数模型（YAML/JSON tag 齐全，可被产品配置直接填充） |
| `RetryPolicy` | 重试策略：次数、退避基数/上限、抖动、是否尊重 `Retry-After`、可重试状态码 |
| `DefaultParams()` | 推荐初始策略（唯一一处应用推荐值的地方） |
| `Params.Normalize()` / `Validate()` | 派生值填充与非法值拒绝（`ErrInvalidParams`） |
| `Duration` | 同时接受 `30s` 与 `30`（秒）的时长类型 |
| `Cost` / `CostEstimator` / `DefaultEstimator` | 单次调用的额度模型与估算器 |
| `ImageTokens` | 单张图片按 512×512 tile 折算的 token 估算；尺寸未知时走兜底值，绝不返回 0 |
| `Gate` | 一个准入点：加权并发 + 请求桶 + 令牌桶，参数可运行时改 |
| `Stats` | 并发/队列/速率/估算 vs 实付 token/重试/超桶请求等计数 |
| `Permit` | 一次准入；`Release()` 幂等、`Settle(usage)` 用真实用量结算 |
| `Assembly` | 路数装配：key → gate 映射 + `Wrap`/`WrapFor`/`WrapAll` 装饰器 |
| `CompleteOnly` + `WrapComplete`/`WrapAllComplete` | 装饰只保证 `Complete` 的窄接口（如 seelex 的 `agent.Completer`）：具体实现若具备流式能力则走真流式，否则回退为「一次 Complete + 单块回调」 |
| `RetryDelay` / `IsRetryable` / `StatusOf` / `RetryAfterOf` | 可单测的重试分类函数 |
| `types.HTTPStatusError` | provider 非 2xx 的**可分类**错误（`agent/core/api` 产出） |

## 参数语义（重要）

- **0 = 不限**：`MaxConcurrency`、`RequestsPerMin`、`TokensPerMin` 为 0 时该维度不设限。
  `Normalize()` 只补齐**无法表达**的派生值（桶容量），绝不替调用方发明上限；
  推荐值只在 `DefaultParams()` 里。
- **`ImageWeight`**：每张图片折算的**在途权重**（并发维度），0 表示不计权重；负值非法。图片的 token 维度由 `DefaultEstimator` 按 `ImageTokens`（512×512 tile，尺寸未知走兜底）计入 `InputTokens`，并累加 `Cost.Images` 张数——两个维度都按像素而非字节计价。
  图片只影响并发权重、不影响 token 估算——图片成本由**像素/tile**决定，与字节无关。
- **`QueueTimeout` = 0**：不排队，立即失败（`ErrQueueTimeout`）；> 0 时是**整个准入**的预算
  （并发等待 + 速率等待共享同一个 deadline）。
- **`Retry.IgnoreRetryAfter`** 默认 false，即默认尊重 provider 的 `Retry-After`。
- **`Retry.MaxAttempts`** 含首次：1 表示不重试。
- **单次请求超过桶容量**：不能让请求永久排队，桶会把这次消耗记成**透支**（后续请求按
  速率补齐），并累加 `Stats.Oversized`，属于配置可见的异常而非静默放行。

## 准入顺序与失败语义

```
Acquire(ctx, cost)
  1) 加权在途槽位（排队，受 QueueTimeout 约束）
  2) 请求速率桶（等待，受同一 deadline 约束）
  3) 令牌速率桶（按估算 token 扣费，等待，受同一 deadline 约束）
```

- 先占本地槽位再扣共享令牌桶，避免“排队时白扣额度”；速率等待失败会把槽位**退还**。
- 失败一律返回可 `errors.Is` 匹配的哨兵错误：`ErrConcurrencyExceeded`、`ErrRateLimited`、
  并统一包一层 `ErrQueueTimeout`（超时）或透传调用方自己的 `ctx` 错误（取消/超时）。
  这三者都是“稍后可以重试”，UI 应显示“排队中”而不是“失败”。
- `Enabled=false` 时准入直接放行（`Admit` 计数仍在），但**重试分类照旧生效**：重试是传输
  层关切，不是配额。

## 装配与 Set 方法

```go
assembly, err := limits.Assemble(limits.DefaultParams(),
    limits.WithObserver(func(o limits.Observation) { /* 排队/重试事件 */ }),
)

// 按账号装配：每个账号一个 gate（Params.PerKey 为 true 时）
client, err := assembly.WrapFor("agent-1", chatClient)   // 实现 types.ChatCompleter

// 只有同步能力的账号接口（例如 seelex 的 agent.Completer）用 WrapComplete：
// 具体值是 *api.ChatClient 时仍走真流式，只有真缺流式能力才回退单块。
client, err = assembly.WrapComplete("agent-1", agentCompleter)

// 批量
clients, err := assembly.WrapAll(map[string]types.ChatCompleter{"agent-1": c1, "agent-2": c2})
clients, err = assembly.WrapAllComplete(map[string]limits.CompleteOnly{"agent-1": c1, "agent-2": c2})

// 运行时改参（全部线程安全，不打断在途请求）
_ = assembly.SetConcurrency(8)
_ = assembly.SetRate(120, 240000)          // 重置速率并按新速率重算桶容量
_ = assembly.SetBurst(20, 40000)           // 显式钉住桶容量
_ = assembly.SetImageWeight(0.5)
_ = assembly.SetQueueTimeout(45 * time.Second)
_ = assembly.SetRetry(limits.RetryPolicy{MaxAttempts: 4, BaseDelay: 500 * time.Millisecond, Jitter: 0.3})
_ = assembly.SetKeyParams("agent-2", limits.DefaultParams().WithRate(30, 60000))
_ = assembly.SetEnabled(false)             // 熔断式旁路（保留计数）
stats := assembly.Snapshot()
```

`SetParams` 是**全局覆盖**：它同时替换基参数与所有已存在 gate 的参数（含此前 `SetKeyParams`
设过的），这样“最后一次设置说了算”，不会出现难以推理的残留覆盖。

## 重试语义

- 非流式：可重试失败 → 退避后**重新走准入**（每次尝试都消耗真实 provider 额度）→ 成功后用
  provider 返回的 `usage` 调 `Permit.Settle` 结算估算差额（超出部分按 TPM 桶扣，扣不动的
  记入 `Stats.TokenDebt`）。
- 流式：**已经吐出过增量就不再重试**，避免客户端看到重复输出；未吐出任何内容的失败才重试。
- 流式目前按**估算**计费：Seele 现有 SSE 处理不暴露 usage，需要精确计费的产品可在自己的
  事件流里调用 `Permit.Settle`（`Permit` 在装饰器内部，产品若要接管请改用 `Gate.Acquire`
  自行准入）。

## 验证

```powershell
go test ./limits/... -count=1 -race
# 真实 provider 冒烟（opt-in，需要 seelex 的 accounts.yaml）
$env:SEELEX_SMOKE_ACCOUNTS='G:\Program\go\seelex\config\accounts.yaml'
go test -tags livesmoke ./test/... -run TestLiveLimitsSmoke -count=1 -v
```

## 边界行为一览（都有对应测试）

| 场景 | 行为 |
| --- | --- |
| `MaxConcurrency=0` | 不设并发上限，仍计数 |
| `QueueTimeout=0` 且槽位满 | 立即 `ErrQueueTimeout`，不等待 |
| 运行中把并发从 4 调到 1 | 在途不受影响；新的准入等待直到权重降到 1 以下 |
| 运行中把速率调低 | 令牌水位被夹到新容量上限，超额额度立即收回 |
| 单次请求 token 估算 > 桶容量 | 记透支 + `Stats.Oversized`，不永久排队 |
| `Retry-After: 5` | 退避 5s（被 `MaxDelay` 夹住上限） |
| 调用方 ctx 取消/超时 | 立即返回调用方错误，不重试、不吞掉 |
| 流式已输出后失败 | 不重试，返回部分内容 + 原错误 |
| `Enabled=false` | 放行且计数，重试仍生效 |
