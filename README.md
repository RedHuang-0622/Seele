# Seele

Seele 是一个无产品语义的 Agent runtime：提供 LLM client、通用工具运行时、Plan/DAG 执行原语、账号租约池、上下文基础能力和可选观测设施；Seelex 负责 Task、产品工具、上下文策略、费用归集和用户体验。

架构原则：

> Seele 提供无产品语义的执行能力；Seelex 决定何时调用、调用什么、上下文放什么、费用如何归集。

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
    {"id": "inspect", "input": "检查项目范围", "kind": "auto"},
    {"id": "backend", "input": "检查后端实现", "kind": "auto"},
    {"id": "tests", "input": "执行验证", "kind": "auto"},
    {"id": "integrate", "input": "整合结果", "kind": "auto"}
  ],
  "edges": [
    {"from": "inspect", "to": "backend"},
    {"from": "inspect", "to": "tests"},
    {"from": "backend", "to": "integrate"},
    {"from": "tests", "to": "integrate"}
  ]
}
```

Plan 生命周期是 `Build → Validate → Seal → Run`；不存在第二份 Graph 状态。

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

实现最小 `Node` 接口：`ID()` 和 `Run(context.Context, RunInput) (RunOutput, error)`。Scheduler/Executor 不识别 AgentFactory、Task 或产品节点 kind；混合 tool/agent/subagent 行为由 Seelex 在 Node 实现中装配。

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

## 文档导航

- [`docs/arch/09-seele-runtime-boundary.md`](docs/arch/09-seele-runtime-boundary.md)：运行时边界
- [`docs/arch/10-seele-extension-contracts.md`](docs/arch/10-seele-extension-contracts.md)：扩展契约
- [`docs/arch/11-seele-boundary-test-strategy.md`](docs/arch/11-seele-boundary-test-strategy.md)：边界测试策略
- [`docs/README.md`](docs/README.md)：架构文档索引
- [`cmd/README.md`](cmd/README.md)：可执行命令索引
