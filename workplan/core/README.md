# workplan/core

`workplan/core` 定义与运行环境无关的 WorkPlan 内核模型：Plan、节点、边、状态和共享上下文。

## 模块导航

| 包 | 职责 |
| --- | --- |
| [plan/](plan/README.md) | 节点、边、入口和后继解析的执行内核 |
| [node/](node/README.md) | 节点接口、节点类型和 Agent 抽象 |
| [edge/](edge/README.md) | 有向边及条件路由 |
| [types/](types/README.md) | 上下文、结果、状态和快照 |

Plan 不依赖 Graph；Graph 是运行时提供的编辑/查询外观。

## 验证

- `go test ./workplan/core/...`
