# workplan

`workplan` 是声明式工作流门面。它以链式 DSL 构建 DAG，并将本地函数、纯 LLM、完整 Agent、条件、循环、并发分支、审批和检查点组合执行。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` / `Option` | 创建 WorkPlan 并注入 AgentFactory、追踪和 Fork 策略 |
| `Auto`、`Method`、`LLM`、`Pipeline` | 添加顺序执行节点 |
| `If`、`Switch`、`Loop`、`Fork` | 添加控制流 |
| `Approve`、`Emit`、`Checkpoint` | 人工介入、变量写入和快照节点 |
| `Run` / `Resume` | 执行或从检查点继续执行 |

## 实现细节

- 门面维护 `entryID` 与 `lastNodeID`，每次 DSL 调用自动添加边；底层图和执行器仍可单独访问。
- sugar 包只构建节点，`runtime/runner`、`scheduler` 和 `executor` 负责执行，保持声明与运行解耦。
- Fork 使用独立分支上下文和可配置策略/并发上限；Tracer 以接口注入，不强依赖具体观测实现。

## 模块导航

- [core/](core/README.md)：节点、边和执行上下文模型。
- [runtime/](runtime/README.md)：图、调度、验证、序列化和检查点。
- [sugar/](sugar/README.md)：DSL 节点构建器。

## 验证

- `go test ./workplan/...`
