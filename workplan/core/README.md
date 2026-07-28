# workplan/core

`workplan/core` 定义与执行环境无关的工作流模型：节点、边、状态与共享上下文。它不调度 goroutine、不调用 LLM，也不处理持久化。

| 包 | 职责 |
| --- | --- |
| [node/](node/README.md) | 节点接口、节点类型和 Agent 抽象 |
| [edge/](edge/README.md) | 条件边解析 |
| [types/](types/README.md) | 上下文、结果、状态和快照模型 |

这种切分使 runtime 可以执行统一的 `node.Node`，而 sugar 和顶层 DSL 只负责构造模型。

## 验证

- `go test ./workplan/core/...`
