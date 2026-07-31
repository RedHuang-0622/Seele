# README 与 Agent 示例补充

本次变更用于说明 Seele 当前开发框架的优势，并提供可离线运行的 Agent、Context 和 WorkPlan 示例。运行时 API 未发生变化。

## 变更内容

| 内容 | 入口 | 说明 |
| --- | --- | --- |
| 框架特色与 CI badge | [`README.md`](../../../README.md) | 根 README 说明模块边界、优势、CI 和学习路径 |
| 示例索引 | [`example_Implement/README.md`](../../../example_Implement/README.md) | 汇总全部可运行示例及配置要求 |
| 显式 Agent 装配 | [`08_composable_agent/README.md`](../../../example_Implement/08_composable_agent/README.md) | Agent、Tools、ReAct、Telemetry |
| Context 管线 | [`09_context_pipeline/README.md`](../../../example_Implement/09_context_pipeline/README.md) | History、Prompt、ToolResult、显式压缩 |
| WorkPlan codec | [`10_workplan_codec/README.md`](../../../example_Implement/10_workplan_codec/README.md) | Node、Codec、DAG 执行和图格式导出 |

## 验证与审查

- [代码变更摘要](code-changes.md)
- [测试报告](test-report.md)
- [最终审查报告](finish-review.md)

## 入口建议

新开发者建议按 `08 → 09 → 10` 的顺序阅读：先理解显式装配，再理解上下文边界，最后理解 WorkPlan 的 Node/Codec 扩展方式。
