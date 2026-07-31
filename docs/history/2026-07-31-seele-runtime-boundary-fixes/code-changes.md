# Seele 运行时边界首轮修复变更摘要

## 新增、修改与删除文件

| 文件 | 类型 | 说明 | 设计模式 |
| --- | --- | --- | --- |
| [`engine/loop.go`](../../../engine/loop.go) | 修改 | 删除主 ReAct 的隐式压缩触发；显式 `CompressNow` 先持久化再替换 history | Explicit Invocation、原子状态替换 |
| [`engine/engine_test.go`](../../../engine/engine_test.go) | 修改 | 验证长 history 原样透传、压缩持久化和失败回滚 | Spy、Failure Injection |
| [`engine/engine_smoke_test.go`](../../../engine/engine_smoke_test.go) | 新增 | 验证显式压缩、并发 session 取消和 history 隔离 | A/B Smoke Test |
| [`workplan/core/plan/plan.go`](../../../workplan/core/plan/plan.go) | 修改 | 恢复 `AddNode` upsert；增加显式幂等节点/无条件边 API | Copy-on-write、CAS、Command 分离 |
| [`workplan/runtime/graph/graph.go`](../../../workplan/runtime/graph/graph.go) | 修改 | Graph facade 转发 Plan 的不同写入模式 | Facade |
| `workplan/core/plan/*_test.go` | 修改/新增 | 覆盖同 ID 覆盖、first-write-wins、条件闭包和并发构造 | Contract Test |
| `workplan/runtime/scheduler/scheduler_smoke_test.go` | 新增 | 修正 deadline 取消测试的节点 ID，并检查具体 context error | A/B Smoke Test |
| [`seelectx/tracer/tracer.go`](../../../seelectx/tracer/tracer.go) | 修改 | 在时钟采样相等时保证 span duration 至少为 1ns | Normalization |
| [`engine/README.md`](../../../engine/README.md) | 修改 | 记录“主循环不主动压缩”和显式压缩持久化语义 | 模块文档 |
| [`workplan/core/plan/README.md`](../../../workplan/core/plan/README.md) | 修改 | 记录兼容写入与显式幂等 API | 模块文档 |
| [`workplan/runtime/graph/README.md`](../../../workplan/runtime/graph/README.md) | 修改 | 记录 facade 的写入模式 | 模块文档 |

## API 变更

| API | 变更 | 兼容性 |
| --- | --- | --- |
| `ReActLoop.Run` | 不再按固定阈值或 callback 主动压缩 history | 行为修复；恢复调用方拥有上下文策略 |
| `ReActLoop.CompressNow` | 显式压缩，并在持久化成功后替换内存 history | 新增 API |
| `ReActLoop.AppendHistory` | 显式追加调用方提供的历史消息 | 新增 API |
| `Plan.AddNode` | 保持原有 upsert/后写覆盖语义 | 向后兼容 |
| `Plan.AddNodeIfAbsent` | 并发 first-write-wins 写入，返回是否新增 | 新增 API |
| `Plan.ReplaceNode` | 仅替换已存在节点，返回是否命中 | 新增 API |
| `Plan.AddEdge` | 保持追加语义，不比较 function identity | 向后兼容 |
| `Plan.AddUnconditionalEdgeIfAbsent` | 按端点幂等添加无条件边 | 新增 API |
| `Graph` 对应写入方法 | 转发到同一个 Plan 内核 | 新增 facade API |

## 设计模式使用

| 模式 | 文件 | 效果 |
| --- | --- | --- |
| Explicit Invocation | `engine/loop.go` | Seele 不再决定何时压缩，调用方必须显式请求 |
| Copy-on-write + CAS | `workplan/core/plan/plan.go` | 保持并发安全，同时区分 upsert 与 if-absent |
| Facade | `workplan/runtime/graph/graph.go` | Graph 不复制状态，只暴露 Plan 的编辑方式 |
| Failure Injection | `engine/engine_test.go` | 模拟 storage 拒绝写入，验证 history 不被部分替换 |
| Normalization | `seelectx/tracer/tracer.go` | 屏蔽平台时钟分辨率造成的零 duration |

## 接口抽象

| 接口 | 实现方 | 使用方 |
| --- | --- | --- |
| `cache.Provider` | `seelectx/cache` | `ReActLoop.persistHistory` |
| `storage.Storage` | `seelectx/storage` 或调用方实现 | `ReActLoop.persistHistory` |
| `node.Node` | WorkPlan 节点实现 | `Plan` / `Graph` |

## 循环依赖检查

- [x] `engine` 仍只依赖通用 cache/storage/tracer contract，没有引入 Seelex 产品类型。
- [x] `core/plan` 不依赖 Graph、scheduler 或 UI。
- [x] `Graph -> Plan` 单向依赖保持不变。
- [x] 未新增包级可变状态。

## 验证

- `go test ./...`：通过。
- `go vet ./...`：通过。
- `go build ./...`：通过。
- `RUN_AB=true go test -tags seele_ab ./engine ./workplan/core/plan ./workplan/runtime/scheduler -run TestAB -count=1 -v`：通过；deadline 用例返回 `context deadline exceeded`，取消会话与其它 history 隔离验证通过。
- race：当前 Windows 环境缺少 `gcc`，无法启用 CGO/race detector。

## Commit 记录

本轮未自动提交。建议 commit message：

```text
fix(runtime): enforce explicit context and plan writes

- remove main-loop compression triggers and persist explicit compression
- restore AddNode compatibility and add safe idempotent write APIs
- fix cancellation smoke tests and normalize zero-duration spans

Refs: runtime-boundary, workplan-kernel, boundary-tests
```
