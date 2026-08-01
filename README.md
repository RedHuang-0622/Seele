# Seele

[![CI](https://github.com/RedHuang-0622/Seele/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/RedHuang-0622/Seele/actions/workflows/ci.yml)

Seele 提供无产品语义的 Agent runtime：LLM client、工具调度、WorkPlan DAG、账号池、上下文基础能力和可选观测能力。Seelex 负责产品任务、工具选择、上下文策略、费用归集和用户体验。

> Seele 提供执行能力；Seelex 决定何时调用、调用什么、上下文放什么、费用如何归集。

## 能力边界

| 模块 | Seele 负责 | 不负责 |
| --- | --- | --- |
| `agent` | 装配 LLM client 与 tools runtime；通过方法集实现 `session.Agent` | 产品工具、Task 语义、主动压缩策略或会话状态 |
| `session` | 一次会话的 working history、ReActLoop、Chat/ChatStream 和显式 Reset | 创建 provider、账号池、工具、durable history 或产品上下文策略 |
| `tools` | Function Calling 接口、注册、分发、超时、权限和 provider 适配 | 文件、Git、工作区等产品环境工具 |
| `workplan` | 通用 Node/Edge、DAG 校验、调度和 codec | Agent/Task 产品语义 |
| `accountpool` | P2C 账号选择、并发 lease 和负载均衡 | 产品费用账本和请求中转 |
| `seelectx` | 可装配的 history、切片、QuickChat 和上下文构建原语 | 在 ReActLoop 内隐式触发产品压缩 |
| `event` | 通用结构化事件、Sink、共享心跳和模块 Locator 扩展点 | EventStore、Task、KV 投影、日志 UI 或产品告警 |
| `errors` | 跨模块统一结构化错误 | 业务层错误策略 |

## 快速开始

```powershell
go test ./... -count=1 -timeout 300s
go vet ./...
go build ./...
```

可运行示例位于 [`example_Implement`](example_Implement)。Agent 装配、上下文管线和 WorkPlan codec 分别见对应目录 README。

创建用户会话时，调用方先装配 `agent.Agent`，再以 `session.NewSession(session.SessionComponents)` 显式注入可选的 durable history、请求 assembler 与 telemetry；会话实现位于 [`session`](session/README.md)。

## WorkPlan 产品 DSL

codec 的 canonical 格式是 `nodes + edges`。节点和边都是最小数据单位，`kind` 与 `input` 的解释由 Seelex 提供的 `NodeFactory[T]` 完成：

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

```go
plan, err := codec.Import[string](payload, codec.NodeFactoryFunc[string](buildNode))
if err != nil {
    return err
}
result, err := runner.New(plan).Run(ctx)
```

复杂输入使用 `codec.Document[MyInput]`；结构化节点使用 `types.Value` 或 `node.NewTypedFunctionNode[I,O]`。旧字符串 Node API 继续兼容。

## 统一错误

跨模块边界返回根目录 [`errors`](errors/README.md) 的结构化错误。使用 `seeleerrors.From(err)` 可读取 `Struct`、`Function`、`Step`、`Path` 和 `Raw`，无需依赖错误字符串。

## 设计文档

- [`workplan/README.md`](workplan/README.md)：WorkPlan 内核与数据传输。
- [`workplan/codec/README.md`](workplan/codec/README.md)：产品 DSL、泛型 NodeFactory 和错误契约。
- [`session/README.md`](session/README.md)：会话装配、working history 与 ReAct 生命周期。
- [`event/README.md`](event/README.md)：结构化事件、Sink、心跳与 Locator 装配。
- [`docs/README.md`](docs/README.md)：跨模块架构文档索引。
- [`docs/arch/14-seelex-integration-guide.md`](docs/arch/14-seelex-integration-guide.md)：Seelex 对各根能力模块的装配与生命周期指南。

## 开发验证

```powershell
go test ./... -count=1 -timeout 300s
go vet ./...
go build ./...
git diff --check
```
