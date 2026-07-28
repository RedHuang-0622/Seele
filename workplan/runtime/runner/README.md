# workplan/runtime/runner

`Runner` 是执行运行时的顶层协调器，提供全新运行和从检查点恢复运行的入口。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` / `Option` | 绑定图、AgentFactory 和可选检查点存储 |
| `Runner.Run` | 校验并执行完整工作流 |
| `Runner.Resume` | 从已保存的快照恢复 |
| `NodeHook` | 观察节点进度 |

## 实现细节

- Runner 先进行图校验，再构造 Executor 与 Scheduler；这将配置错误与实际执行分开。
- 需要保存或恢复时，它通过 checkpoint Manager 处理快照，不让 Scheduler 感知存储后端。
- Options 使持久化和回调为可选能力，默认运行路径保持轻量。

## 依赖与验证

- 图校验：[validate](../validate/README.md)，检查点：[checkpoint](../checkpoint/README.md)
- 验证：`go test ./workplan/runtime/runner/...`
