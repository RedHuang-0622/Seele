# workplan/core/node

该包定义 WorkPlan 的可执行节点契约与基础节点实现。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Node`、`BaseNode`、`NodeKind` | 所有节点的统一结构与类别 |
| `NewFunctionNode` | 执行本地 Go 函数 |
| `NewLLMNode` | 执行纯 LLM 调用 |
| `NewAgentNode` | 通过 `AgentFactory` 创建完整 Agent 节点 |
| `AgentFactory`、`LLMProvider` | 对上层运行依赖的抽象 |

## 实现细节

- `NodeKind` 区分 function、LLM、agent 与 sugar 控制节点，Executor 可据此进行统一分派。
- 节点持有 ID、输入和必要依赖，不持有全局 Agent；`AgentFactory` 在需要时创建实例，避免核心包依赖装配层。
- 各具体节点仅封装自己的执行语义，分支、重试和状态转移由 runtime 处理。

## 依赖与验证

- 运行方：[runtime/executor](../../runtime/executor/README.md)
- 构造方：[sugar](../../sugar/README.md)
- 验证：`go test ./workplan/core/node/...`
