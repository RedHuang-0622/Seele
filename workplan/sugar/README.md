# workplan/sugar

`sugar` 是 WorkPlan DSL 的节点构建层。各子包只将语义明确的节点和边写入 `core/plan.Plan`；执行逻辑集中在 runtime。

| 包 | DSL 语义 |
| --- | --- |
| [auto/](auto/README.md) | 本地函数、纯 LLM、完整 Agent 节点 |
| [switch/](switch/README.md) | If/Switch 条件分支 |
| [loop/](loop/README.md) | 可观察的循环 |
| [fork/](fork/README.md) | 并发分支 |
| [approve/](approve/README.md) | 人工审批 |
| [emit/](emit/README.md) | 写入命名变量 |
| [checkpoint/](checkpoint/README.md) | 触发检查点 |

## 实现细节

- `Add`/`NewNode` 系列函数将具体节点注册到图，顶层 `workplan` 再负责自动连边。
- 控制流的运行语义由 `runtime/executor` 和 `scheduler` 解释，sugar 包不启动 goroutine 或直接调用 Agent。

## 验证

- `go test ./workplan/sugar/...`
