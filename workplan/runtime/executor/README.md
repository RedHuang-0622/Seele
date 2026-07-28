# workplan/runtime/executor

`Executor` 执行一个已选定的 `node.Node`，并把输出或错误写回工作流上下文。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` | 创建执行器 |
| `Executor.Execute` | 执行单个节点 |

## 实现细节

- 执行器按 `NodeKind` 分派 function、LLM、Agent 和 sugar 节点的执行语义，统一生成 `NodeResult`。
- 输入在执行前通过 `WorkflowContext` 模板渲染，结果和状态由上下文集中保存。
- 执行器不决定下一跳；调度策略与边解析留给 Scheduler。

## 依赖与验证

- 节点：[core/node](../../core/node/README.md)
- 调度：[scheduler](../scheduler/README.md)
- 验证：`go test ./workplan/runtime/executor/...`
