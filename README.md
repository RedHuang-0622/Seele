# Seele

[![CI](https://github.com/RedHuang-0622/Seele/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/RedHuang-0622/Seele/actions/workflows/ci.yml)

Seele 是一个无产品语义的 Agent runtime：提供 LLM client、通用工具运行时、Plan/DAG 执行原语、账号租约池、上下文基础能力和可选观测设施；Seelex 负责 Task、产品工具、上下文策略、费用归集和用户体验。

架构原则：

> Seele 提供无产品语义的执行能力；Seelex 决定何时调用、调用什么、上下文放什么、费用如何归集。

## 为什么选择 Seele

Seele 的特色在于把“执行能力”和“产品编排”拆成可替换的窄接口。它适合需要替换模型、工具来源、上下文策略或观测后端的 Agent 应用，而不是要求所有调用方接受一套隐式全家桶。

| 特色 | 当前实现 | 对开发者的价值 |
| --- | --- | --- |
| 显式装配 | `agent.NewWithComponents` 只要求 `Completer`，工具 runtime、日志和流式能力均可注入 | 可用 fake 离线测试，也能在生产环境替换 Provider/账号池，不启动隐式基础设施 |
| 工具平行于 Agent | `tools.Registry`、Provider、Middleware、超时和重试保持 Provider-neutral | 本地函数、MCP、microHub、Skills 可以统一成 Function Calling，不把产品工具写进 Agent |
| 上下文策略可控 | `seelectx` 提供 history、请求拼装、结果筛选、QuickChat 和显式 Compressor | ReActLoop 不自动压缩；Seelex 可以决定何时压缩、保留什么、如何计费和持久化 |
| Plan 内核最小化 | `workplan.Node` 只有 `ID`/`Run`，codec 只处理拓扑和不透明节点载荷 | 自定义 tool、agent、subagent 或人工节点不需要修改调度器；同一 Plan 可导出 edge list、邻接表和矩阵 |
| 可观测但不侵入 | OTel 对齐的 `telemetry.Hook`、`Tracer`、intent/effect correlation、Metric/Audit | 调试、审计和可视化可以按需启用，观测故障默认不会改变业务结果 |
| 账号租约与负载隔离 | `accountpool` 以 P2C 选择账号并用 lease 覆盖完整请求生命周期 | 多账号并发限制与 Provider 选择可独立演进，不把费用账本塞进 Seele |

推荐的学习顺序是：先看[显式 Agent 装配](example_Implement/08_composable_agent/README.md)，再看[上下文请求管线](example_Implement/09_context_pipeline/README.md)，最后看[自定义 WorkPlan 节点与 JSON codec](example_Implement/10_workplan_codec/README.md)。

## 能力边界

| 根模块 | 负责 | 不负责 |
| --- | --- | --- |
| [`agent/`](agent/README.md) | LLM client 与可选组件的装配入口，提供 Chat/ChatStream | 不拥有产品工具、Task 或压缩策略 |
| [`tools/`](tools/README.md) | Function Calling 合约、注册分发、权限和 Provider 适配 | 不读取文件、不执行 Git/Shell、不理解工作区 |
| [`workplan/`](workplan/README.md) | Plan、Node、Edge、DAG 校验、编排和 codec | 不依赖 Agent、Context 或 Task 语义 |
| [`accountpool/`](accountpool/README.md) | P2C 账号选择、并发信号量和租约生命周期 | 不计费、不做产品预算、不中转请求 |
| [`seelectx/`](seelectx/README.md) | history、请求装配、切片、压缩和工具结果处理原语 | 不在 ReActLoop 中主动触发产品压缩 |
| [`telemetry/`](telemetry/README.md) | 可选 Hook、Trace、Metric、Audit 和 OpenTelemetry 适配 | 不改变业务结果或持久化产品状态 |

`Task`、文件/Git/工作区工具、Plan DSL、上下文压缩触发和 TokenLedger 属于 Seelex；Seele 只提供可复用接口和显式装配点。

## 数据流

```text
Seelex / caller
      │ 选择 client、tools、context、hooks
      ▼
agent composition root
      ├── ChatClient ── accountpool.P2C lease ── provider API
      ├── tools.ToolRuntime ── holder/gateway/provider ── handler
      ├── seelectx assembler ── working messages / tool result view
      ├── engine.ReActLoop ── LLM ↔ tool call loop
      └── telemetry Hook ── Tracer / Metric / Audit

workplan.Plan ── Node.Run + Edge ── scheduler / executor / runner
```

Agent 的最低装配要求是一个 `Completer`；工具、Stream client、history、cache、ContextController、Hook 和 Tracer 均可选注入。`agent.NewWithComponents` 不启动 microHub、不加载 registry、不创建账号池，也不隐式注册工具。

## 快速开始

### 交互式 REPL

准备不提交到仓库的账号 YAML 后运行：

```powershell
go run ./cmd/repl -c <accounts.yaml>
```

REPL 显式注册 [`tools/builtin`](tools/builtin/README.md)，产品工具由调用方自行提供。

### 真实 API Function Calling 冒烟

```powershell
$env:SMOKE_CONFIG = "<absolute-path-to-accounts.yaml>"
go run ./cmd/smoke
```

该命令会真实调用配置的模型，并强制验证 `calculate`、`get_time`、`text_stats` 的 tool call、执行结果和最终回复。没有凭证时，Go 集成测试明确 skip：

```powershell
$env:RUN_REAL_API_SMOKE = "true"
$env:SMOKE_CONFIG = "<absolute-path-to-accounts.yaml>"
go test ./cmd/smoke -run TestRealAPIBuiltinSmoke -v -count=1 -timeout 180s
```

### WorkPlan formal JSON

WorkPlan codec 支持通用 `nodes + edges` 形状；节点实例由调用方 `NodeDecoder` 或自定义 Node 实现物化：

```json
{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id": "inspect", "type": "seelex", "data": {"input": "检查项目范围", "kind": "auto"}},
    {"id": "backend", "type": "seelex", "data": {"input": "检查后端实现", "kind": "auto"}},
    {"id": "tests", "type": "seelex", "data": {"input": "执行验证", "kind": "auto"}},
    {"id": "integrate", "type": "seelex", "data": {"input": "整合结果", "kind": "auto"}}
  ],
  "edges": [
    {"from": "inspect", "to": "backend"},
    {"from": "inspect", "to": "tests"},
    {"from": "backend", "to": "integrate"},
    {"from": "tests", "to": "integrate"}
  ]
}
```

`type` 与 `data` 是调用方提供给 `NodeDecoder` 的不透明载荷；Seelex 可以在自己的适配层把产品 JSON 的 `input`/`kind` 映射到这里。Plan 生命周期是 `Build → Validate → Seal → Run`；不存在第二份 Graph 状态。

## 扩展方式

### Agent

```go
agt, err := agent.NewWithComponents(agent.Components{
    Completer: client,
    Tools:     gateway,
})
```

调用方可以替换 client、工具网关、上下文装配器、历史存储、ContextController 和 Telemetry Hook；核心 Loop 只依赖最小接口。

### Tools

实现 `tools.ToolProvider`，将 `ToolEntry{Definition, Handler}` 注册到 `tools.Registry` 或 `tools/holder`。远程 MCP、microHub、Skills 和普通 Function Calling provider 都通过适配器接入。参见 [`tools/builtin`](tools/builtin/README.md) 的无副作用示例。

### WorkPlan

实现最小 `Node` 接口：`ID()` 和 `Run(context.Context, *WorkflowContext) (string, error)`。Scheduler/Executor 不识别 AgentFactory、Task 或产品节点 kind；混合 tool/agent/subagent 行为由 Seelex 在 Node 实现中装配。

### Context 与 Telemetry

`seelectx` 提供切片、规整、相关性排序、Placeholder、QuickChat 和显式压缩 Controller；`telemetry` 提供 OTel 对齐的 lifecycle Hook、intent/effect correlation、Trace/Metric/Audit sink。二者都不会被 Loop 隐式启用产品策略。

## 依赖约束

```text
agent       → accountpool / tools contracts / seelectx
workplan    → workplan contracts only
tools       → tools contracts + external provider SDKs
seelectx    → provider-neutral types
accountpool → standard library
```

禁止 `tools → agent`、`workplan → agent/seelectx/task`、`seelectx → agent` 和任何旧 `agent/core/tool` 反向依赖。文件、Git、Shell、工作区和 Task 适配器必须由 Seelex 实现并显式注册。

## 验证

```powershell
go test ./... -count=1 -timeout 300s
go vet ./...
go build ./...
git diff --check
```

Develop push 和面向 `develop` 的 PR 使用 [并行 CI](.github/workflows/ci.yml)：质量门禁与 6 个 race+coverage 测试分片同时启动，并通过 workflow concurrency 自动取消同一分支的旧运行。真实 API smoke 仅在手动触发并显式启用时执行。

Windows 若没有可用 C 编译器，`go test -race ./...` 会被 Go 工具链拒绝；这不等同于 race 通过，需在具备 CGO 工具链的环境补跑。完整变更、测试和审查记录位于 [`docs/history/2026-07-31-workplan-tasklist-root-refactor/`](docs/history/2026-07-31-workplan-tasklist-root-refactor/)。

新增的离线示例也可单独验证：

```powershell
go test ./example_Implement/08_composable_agent ./example_Implement/09_context_pipeline ./example_Implement/10_workplan_codec -count=1
```

## 文档导航

- [`docs/arch/09-seele-runtime-boundary.md`](docs/arch/09-seele-runtime-boundary.md)：运行时边界
- [`docs/arch/10-seele-extension-contracts.md`](docs/arch/10-seele-extension-contracts.md)：扩展契约
- [`docs/arch/11-seele-boundary-test-strategy.md`](docs/arch/11-seele-boundary-test-strategy.md)：边界测试策略
- [`docs/README.md`](docs/README.md)：架构文档索引
- [`cmd/README.md`](cmd/README.md)：可执行命令索引
- [`docs/history/2026-07-31-readme-agent-examples/README.md`](docs/history/2026-07-31-readme-agent-examples/README.md)：本次 README 与离线示例变更说明、测试和审查入口
