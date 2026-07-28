# workplan/sugar/auto

该包构建最常用的工作节点：本地方法、纯 LLM 和完整 Agent 策略节点。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `AddMethod` / `AddLLM` | 把对应节点加入图 |
| `NodeStrategy` | 节点执行策略抽象 |
| `MethodStrategy`、`LLMStrategy`、`AgentStrategy` | 三种执行粒度 |
| `WithToolFilter`、`WithSystemPrompt`、`WithOnChunk` | 配置 Agent 节点 |

## 实现细节

- `StrategyNode` 将不同执行方式统一为一个节点类型，Executor 只需调用策略接口。
- Method 不消耗 LLM token；LLM 直接调用 Provider；Agent 由 Factory 创建完整工具循环，调用方可按成本选择。
- 选项保留在节点上，流式回调和工具过滤不会污染工作流图的其他节点。

## 依赖与验证

- 节点模型：[core/node](../../core/node/README.md)
- 验证：`go test ./workplan/sugar/auto/...`
