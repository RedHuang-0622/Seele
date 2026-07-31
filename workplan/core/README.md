# workplan/core

`workplan/core` 定义与运行环境无关的 WorkPlan 内核模型：Plan、节点、边、状态和共享上下文。

## 模块导航

| 包 | 职责 |
| --- | --- |
| [plan/](plan/README.md) | 节点、边、入口和后继解析的执行内核 |
| [node/](node/README.md) | 节点接口、节点类型和 Agent 抽象 |
| [edge/](edge/README.md) | 有向边及条件路由 |
| [types/](types/README.md) | 上下文、结果、状态和快照 |

Plan 是节点、边和入口的唯一状态源；runtime 只通过 Plan 的只读视图进行调度，不维护第二份图状态。

## 验证

- `go test ./workplan/core/...`
