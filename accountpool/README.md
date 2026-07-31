# accountpool

`accountpool` 提供无 Provider 和产品语义的账号并发租约、P2C 负载选择与 client 解析契约；它不读取账号配置、不解释密钥，也不创建具体 LLM 请求。

## 功能与边界

账号池管理调用方注入的泛型 `Value`。这个值既可以是已经构造好的 client，也可以是包含 `provider/baseURL/key/model` 的不透明账号条目。用于路由的非敏感属性可以放入 `Metadata`，但 `Metadata` 会出现在快照中，因此 API key 等凭证应保留在 `Value` 内。

模块负责：

- 为每个账号维护独立的并发信号量；
- 使用 P2C（Power of Two Choices）从实时可用账号中选择低负载账号；
- 支持指定账号、Metadata 等值过滤和自定义 predicate；
- 支持动态启用、禁用和空闲账号注销；
- 通过 `context.Context` 取消或终止等待；
- 暴露幂等 `Lease.Release/Close`，覆盖普通 HTTP 请求和完整流式请求的生命周期；
- 为硬编码 client 和外部 client factory 提供统一的 `ClientResolver` 入口。

模块不负责账号文件格式、Provider 协议、重试、RPM 计数、费用账本或 HTTP client 的具体实现。这些策略由调用方或上层模块组合。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `Account[T]` | 注册不透明值、并发上限和公开路由元数据 |
| `Pool[T]` | 账号注册、状态切换、租约分配、快照和统计契约 |
| `New[T]` | 创建默认使用占用率 P2C 策略的 `P2CPool[T]` |
| `AcquireRequest` | 指定账号，或按 Metadata 与 predicate 筛选账号 |
| `Lease[T]` | 持有一个账号信号量槽位，`Release` 与 `Close` 均幂等 |
| `Selector` / `LoadMetric` | 替换 P2C 选取算法或实时负载评分来源 |
| `ClientResolver[T]` | 向 agent 隐藏硬编码 client 与账号池之间的来源差异 |
| `ClientFactory` / `MaterializeAccount` | 将调用方配置转换为可注册 client |
| `NewStaticResolver` | 将单个硬编码 client 适配到 `ClientResolver` |
| `Stats` / `AccountSnapshot` | 查看并发容量、占用率、启用状态和路由元数据 |
| `Lookup` / `Entries` | 为配置、UI 和健康检查读取不占用租约的检查视图；不得用于请求分发 |

典型请求需要让租约覆盖实际网络资源的完整生命周期：

```go
lease, err := pool.Acquire(ctx, accountpool.AcquireRequest{
    Metadata: map[string]string{"provider": "openai"},
})
if err != nil {
    return err
}
defer lease.Close()

client := lease.Client()
return client.Complete(ctx, request)
```

流式调用不能在只获得 response headers 后提前释放租约；应在读取完毕或关闭 stream 时调用 `Close`。

## 实现细节

- `P2CPool` 使用 `sync.Map` 保存账号状态。每个账号使用容量为 `MaxConcurrency` 的 `chan struct{}` 作为信号量，租约获取和禁用切换在账号锁下线性化，因而禁用后不会产生新的租约；禁用前已获得的租约仍可自然结束。
- 默认 `P2CSelector` 每次随机抽取两个实时可用候选，使用 `OccupancyMetric` 比较 `Active / MaxConcurrency`，选择评分更低者。调用方可以注入读取外部 CPU、配额或延迟数据的 `LoadMetric`。
- 当全部匹配账号都已满载时，`Acquire` 等待可轮换的广播 channel。释放、启用、禁用或注册账号会唤醒等待者重新选择，避免单个通知被合并后遗留空闲槽位。
- `AcquireRequest.AccountID` 是强约束：账号不存在或被禁用时分别返回可由 `errors.Is` 识别的 `ErrAccountNotFound` 和 `ErrAccountDisabled`。未指定账号且无匹配候选时返回 `ErrNoEligibleAccount`；满载等待则保留 `context.Canceled` 或 `context.DeadlineExceeded` 错误链。
- `Stats` 是用于路由观测和测试的逐账号即时快照，不承诺跨全部账号的事务一致性，也不会暴露泛型 `Value`。
- `Lookup/Entries` 会返回调用方注册的泛型 `Value`，仅供 adapter 做配置和健康检查；网络请求仍必须通过 `Acquire`，否则不会计入并发负载。

## 依赖与协作

- 上游装配：[agent](../agent/README.md) 后续可仅依赖 `ClientResolver`，而不依赖具体 `P2CPool`。
- 公共模型：[types](../types/README.md) 不依赖本模块；账号池同样不导入 `agent` 或 `types`，以避免反向依赖。
- 跨模块文档索引：[docs](../docs/README.md)。

## 验证

- 单元与并发测试：`go test ./accountpool -count=1 -v`
- 数据竞争测试（需要 CGO 与本机 C 编译器）：`go test -race ./accountpool -count=1`
- 主要测试位于 [`accountpool_test.go`](accountpool_test.go)，覆盖 P2C 评分、筛选、超时取消、幂等释放、动态启停、factory/static resolver、并发容量和注销保护。
