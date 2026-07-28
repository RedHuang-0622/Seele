# workplan/core/types

该包定义 WorkPlan 的执行状态、变量上下文、节点结果、快照和条件注册表。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `WorkflowContext` | 运行期变量、节点状态和结果容器 |
| `NodeResult` / `WorkPlanResult` | 单节点和整图执行结果 |
| `Status` | pending、running、completed、failed、aborted 等状态 |
| `Snapshot` / `ConditionRegistry` | 续跑状态和条件重建支持 |
| `RenderTemplate` | 从上下文渲染节点输入模板 |

## 实现细节

- `WorkflowContext` 使用锁保护变量、结果和状态，供 Fork 分支和调度器安全读取/更新。
- JSON 转义辅助函数让模板能安全嵌入结构化上下文；`RenderTemplate` 在节点执行前解析变量引用。
- 快照保存可序列化状态，而条件函数经 `ConditionRegistry` 显式登记，避免函数值直接序列化。

## 依赖与验证

- 使用方：[core/edge](../edge/README.md)、[runtime/checkpoint](../../runtime/checkpoint/README.md)
- 验证：`go test ./workplan/core/types/...`
