# workplan/core/node

`core/node` 定义 WorkPlan 的可执行节点契约和基础节点实现，不负责图存储或调度。

## 公开接口

| API | 用途 |
| --- | --- |
| `Node` / `BaseNode` / `NodeKind` | 统一节点结构和类别 |
| `InputNode` | 可导出声明式任务输入的节点契约 |
| `NewAutoNode` | 直接执行 DSL `kind: "auto"` 子代理任务 |
| `NewFunctionNode` / `NewLLMNode` / `NewAgentNode` | 其它核心节点原语 |
| `AgentFactory` / `LLMProvider` | 运行时依赖抽象 |

## 实现细节

- `AutoNode` 在运行时渲染其任务输入，支持 Scheduler 注入分支级 `AgentFactory`，因此并行分支可使用独立子代理。
- `DSLInput` 保留未渲染的任务文本，供正式 DSL 导出使用。
- 节点不依赖 Plan 或 Graph；路由与调度由运行时负责。

## 依赖与验证

- 执行器：[../../runtime/executor/README.md](../../runtime/executor/README.md)
- DSL 编译：[../../runtime/serialize/README.md](../../runtime/serialize/README.md)
- 验证：`go test ./workplan/core/node/...`
