# 代码变更摘要

本记录汇总本次根能力重构以及最后补充的通用 builtin 与真实 API 冒烟入口。所有变更仍未提交 commit。

## 新增、修改与删除文件

| 文件或目录 | 类型 | 说明 | 设计方式 |
| --- | --- | --- | --- |
| [`tools/builtin/`](../../../tools/builtin/README.md) | 新增 | `get_time`、`calculate`、`text_stats` 三个可选、产品无关的 Function Calling 工具 | Provider、依赖注入、严格参数校验 |
| [`tools/builtin/arguments.go`](../../../tools/builtin/arguments.go) | 新增 | JSON 参数解析、JSONPath、行列和语义错误 | 错误对象、输入边界隔离 |
| [`cmd/smoke/`](../../../cmd/smoke/README.md) | 新增 | 真实模型 API 的 builtin Function Calling 冒烟命令和 gated integration test | Composition Root、端到端观测 |
| [`cmd/repl/main.go`](../../../cmd/repl/main.go) | 修改 | 从内联 `get_time` 改为显式注册 `tools/builtin` | 显式装配、无隐式注册 |
| [`agent/gateway/api/`](../../../agent/gateway/api/README.md) | 修改 | 删除旧 round-robin 测试假设，按 P2C 选择语义验证 | 兼容 adapter、不持有租约 |
| [`.github/workflows/ci.yml`](../../../.github/workflows/ci.yml) | 修改 | Develop/main CI 改为质量门禁与 6 个测试分片并行，增加旧运行取消和手动真实 API smoke | Matrix Sharding、Fail Isolation |
| [`tools/README.md`](../../../tools/README.md)、[`cmd/README.md`](../../../cmd/README.md) | 修改 | 增加 builtin 与 smoke 导航 | 就近文档索引 |

## API 变更

| API | 变更 | 兼容性 |
| --- | --- | --- |
| `builtin.New(options ...Option)` | 创建不自动注册的通用工具 Provider | 新 API |
| `builtin.WithClock` / `Clock` | 注入 `get_time` 的时间源 | 新 API |
| `builtin.ArgumentError` | 暴露工具名、JSONPath、行列和原因 | 新 API |
| `cmd/smoke` | 支持 `SMOKE_CONFIG` 或 `SEELE_SMOKE_*` 环境配置 | 新命令 |
| `DefaultGateway.Select` | 文档和测试改为依赖账号池当前选择策略，不再承诺 round-robin | 运行时语义修订 |
| Develop CI | PR 触发范围增加 `develop`；自动测试拆成 6 个并行 race+coverage shard | CI 行为变更 |

## 设计模式使用

| 模式 | 位置 | 效果 |
| --- | --- | --- |
| Provider | `tools/builtin/provider.go` | builtin 可由调用方自由注册、替换或不装配 |
| Dependency Injection | `builtin.WithClock`、`agent.NewWithComponents` | 测试与生产环境不共享隐式全局状态 |
| Adapter | `tools/holder` + `tools/gateway` | 根工具合约与 Agent/Engine 运行接口解耦 |
| Observer | `cmd/smoke` 的 `LoopHooks` | 用 Hook 验证模型是否真实发起并完成工具调用 |
| Matrix Sharding | `.github/workflows/ci.yml` | 按历史耗时和模块边界拆分测试，缩短关键路径 |

## 接口抽象

| 接口 | 实现方 | 使用方 |
| --- | --- | --- |
| `tools.ToolProvider` | `builtin.Provider` | `tools.Registry`、`holder.Holder` |
| `tools.ToolHandler` | builtin handler | Registry/Holder dispatcher |
| `builtin.Clock` | `systemClock`、`ClockFunc` 或调用方实现 | `get_time` |
| `agent.ToolRuntime` | `tools/gateway.DefaultGateway` | `engine.ReActLoop` |

## 边界与安全检查

- [x] builtin 不读取文件、Git、工作区，不执行命令，不依赖 `agent`、`seelectx`、`workplan` 或 `accountpool`。
- [x] Provider 不自动注册；REPL 和 smoke 命令都显式装配。
- [x] 参数采用 `DisallowUnknownFields`；缺失字段、类型错误、未知字段、非法时区、除零和非法 JSON 均返回语义错误。
- [x] API key 只从外部配置或环境变量读取，文档不包含真实凭证。
- [x] `tools` 依赖边界检查无 `agent/workplan/seelectx/accountpool` 反向依赖。

## 验证记录

| 命令 | 结果 |
| --- | --- |
| `go test ./tools/builtin ./cmd/smoke -count=1` | 通过 |
| `go test ./... -count=1 -timeout 300s` | 通过 |
| `go vet ./...` | 通过 |
| `go build ./...` | 通过 |
| `git diff --check` | 通过 |
| 旧包路径与工具反向依赖检查 | 通过 |
| `go test ./tools/builtin -coverprofile=...` | 89.1% |
| `go test ./cmd/smoke -coverprofile=...` | 73.2%；命令入口和真实 API 分支需凭证才能覆盖 |
| `go test ./accountpool -bench=. -benchmem -benchtime=1s` | 通过；当前无 benchmark 用例 |
| `go test ./tools/builtin -bench=. -benchmem -benchtime=1s` | 通过；当前无 benchmark 用例 |
| `go test -race ./...` | 未执行：Windows 环境提示 `-race requires cgo; enable cgo by setting CGO_ENABLED=1` |
| `go test ./cmd/smoke -run TestRealAPIBuiltinSmoke -v` | 明确 skip：当前无 `SMOKE_CONFIG` 或 `SEELE_SMOKE_*` 凭证 |
| `actionlint .github/workflows/ci.yml` | 通过 |
| 多 package 单 coverage profile 命令 | 通过；验证 CI shard 的 profile 参数顺序和绝对路径策略 |

## Commit 记录

| Commit | Type | 子目标 | Message |
| --- | --- | --- | --- |
| 未提交 | `feat` | 通用 builtin 与真实 API 冒烟入口 | `feat(tools): add optional builtins and real API smoke command` |
| 未提交 | `ci` | Develop 并行 CI | `ci(develop): shard race tests for parallel execution` |
