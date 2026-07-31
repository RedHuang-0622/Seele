# accountpool 代码变更摘要

## 新增、修改与删除文件

| 文件 | 类型 | 说明 | 设计方式 |
| --- | --- | --- | --- |
| [`accountpool/types.go`](../../../accountpool/types.go) | 新增 | 泛型账号、租约、解析器、快照与统计契约 | 接口隔离、泛型组合 |
| [`accountpool/pool.go`](../../../accountpool/pool.go) | 新增 | `sync.Map`、账号级 channel semaphore、动态状态与租约分配 | P2C 资源池、广播唤醒 |
| [`accountpool/selector.go`](../../../accountpool/selector.go) | 新增 | P2C 选择器与可注入负载指标 | 策略模式 |
| [`accountpool/factory.go`](../../../accountpool/factory.go) | 新增 | client factory、配置物化和硬编码 client resolver | 工厂与适配器模式 |
| [`accountpool/errors.go`](../../../accountpool/errors.go) | 新增 | 可通过 `errors.Is` 区分的稳定错误 | 语义错误契约 |
| [`accountpool/accountpool_test.go`](../../../accountpool/accountpool_test.go) | 新增 | 单元、超时和并发容量测试 | 黑盒行为验证 |
| [`accountpool/README.md`](../../../accountpool/README.md) | 新增 | 模块职责、API、实现与验证说明 | 就近文档 |
| [`agent/core/api/pool.go`](../../../agent/core/api/pool.go) | 修改 | 将旧 round-robin/RPM 池替换为根 P2C 池 adapter | 适配器、单一状态源 |
| [`agent/core/api/client.go`](../../../agent/core/api/client.go) | 修改 | HTTP 与 SSE 请求全生命周期持有账号租约 | 租约模式 |
| [`agent/core/api/config.go`](../../../agent/core/api/config.go) | 修改 | 增加 `max_concurrency` 配置与缺省值 | 显式配置 |
| [`agent/core/api/client_lease_test.go`](../../../agent/core/api/client_lease_test.go) | 新增 | 验证同步、流式与错误路径释放 | 生命周期测试 |

本切片没有修改 `agent/agent.go`。`agent/core/api.AccountPool` 已成为根池的 client-specific adapter；旧 `Get` 类方法只保留检查兼容，`ChatClient` 请求路径全部使用租约。

## API 变更

| API | 变更 | 兼容性 |
| --- | --- | --- |
| `accountpool.Account[T]` | 新增泛型账号注册模型 | 新 API，不影响旧包 |
| `accountpool.Pool[T]` / `ClientResolver[T]` | 新增池与使用方最小解析接口 | 新 API，供后续 agent adapter 采用 |
| `accountpool.P2CPool[T]` | 新增 P2C 租约池实现 | 新 API |
| `accountpool.Lease[T]` | 新增幂等 `Release/Close` 生命周期 | 新 API |
| `accountpool.ClientFactory` / `StaticResolver` | 新增 factory 与硬编码 client 适配入口 | 新 API |
| `api.Account.MaxConcurrency` | 新增账号并发上限，YAML 键为 `max_concurrency` | 缺省值为 `1` |
| `api.ChatClient.SelectAccount` | 从全局池游标改为当前 client 的 Acquire pin | 语义变更 |
| `api.AccountPool` | 从独立 round-robin 状态源改为 `P2CPool[*Account]` adapter | breaking runtime semantics |

## 设计模式使用

| 模式 | 文件 | 效果 |
| --- | --- | --- |
| 策略模式 | `selector.go` | P2C 与负载评分可以独立替换 |
| 工厂模式 | `factory.go` | client 配置和构造规则由外部注入 |
| 适配器模式 | `factory.go` | 硬编码 client 与池化 client 使用同一解析契约 |
| 租约模式 | `types.go` | 并发槽位与 HTTP/stream 生命周期显式绑定 |

## 接口抽象

| 接口 | 实现方 | 使用方 |
| --- | --- | --- |
| `Pool[T]` | `P2CPool[T]` | agent adapter、测试或其它资源消费者 |
| `ClientResolver[T]` | `P2CPool[T]`、`StaticResolver[T]` | agent/client 装配层 |
| `Selector` | `P2CSelector` 或调用方实现 | `P2CPool` |
| `LoadMetric` | `OccupancyMetric`、`LoadMetricFunc` 或调用方实现 | `P2CSelector` |
| `ClientFactory` | 调用方工厂或 `ClientFactoryFunc` | `MaterializeAccount` |

## 循环依赖检查

- [x] `accountpool` 仅依赖 Go 标准库。
- [x] `accountpool` 不导入 `agent`、`workplan`、`tools`、`seelectx` 或 `types`。
- [x] 后续兼容逻辑应由 `agent` 侧 adapter 依赖 `accountpool`，不能反向放入本模块。

## 验证记录

- `go test ./accountpool -count=20`：通过。
- `go vet ./accountpool`：通过。
- `go build ./accountpool`：通过。
- `go test ./agent/core/api -count=10`：通过。
- `go vet ./agent/core/api`：通过。
- `go test ./agent/gateway/api -count=1`：通过兼容检查。
- `go test -race ./accountpool -count=1`：环境缺少 `gcc`，Windows CGO 无法启动，未执行成功。

## Commit 记录

| Commit | Type | 子目标 | Message |
| --- | --- | --- | --- |
| 未提交 | `feat` | 根级账号租约池 | `feat(accountpool): add P2C client lease pool` |
