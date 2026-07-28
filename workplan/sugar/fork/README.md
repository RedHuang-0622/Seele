# workplan/sugar/fork

该包把多个 `ForkBranch` 定义封装成可由运行时并发执行的 Fork 节点。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `NewNode` | 创建并注册 Fork 节点 |
| `ForkNode` | 保存分支和并发配置 |

## 实现细节

- 节点仅记录分支定义、AgentFactory 和最大并发数；不在 DSL 构建阶段启动分支。
- 执行时由 `forkexec.ForkCoordinator` 创建隔离上下文、实施限流并合并结果。
- 将配置留在节点上，使同一图可包含不同并发预算的 Fork。

## 依赖与验证

- 并发执行：[runtime/forkexec](../../runtime/forkexec/README.md)
- 验证：`go test ./workplan/sugar/fork/...`
