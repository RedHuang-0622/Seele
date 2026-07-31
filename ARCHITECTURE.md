# Seele 架构入口

当前架构已从旧 Graph facade、产品 builtin 和 ReAct 自动压缩迁移到平行根能力：`agent`、`tools`、`workplan`、`accountpool`、`seelectx` 和可选 `telemetry`。

长期有效的跨模块设计以 [`docs/arch/`](docs/arch/README.md) 为准：

- [`09-seele-runtime-boundary.md`](docs/arch/09-seele-runtime-boundary.md)：Seele/Seelex 边界与运行时职责
- [`10-seele-extension-contracts.md`](docs/arch/10-seele-extension-contracts.md)：Agent、Tools、WorkPlan、Context 和 Telemetry 扩展接口
- [`11-seele-boundary-test-strategy.md`](docs/arch/11-seele-boundary-test-strategy.md)：依赖边界、功能和验收测试

根目录 [`README.md`](README.md) 提供快速开始、依赖方向、formal WorkPlan JSON 和命令入口。一次性迁移、评审和测试记录位于 [`docs/history/`](docs/history/README.md)。

## 当前拓扑

```text
Seelex / caller
      │
      ▼
agent composition root
  ├─ LLM client ← accountpool P2C lease
  ├─ ToolRuntime ← tools provider/gateway
  ├─ ReActLoop   ← seelectx request/history primitives
  └─ telemetry   ← optional Hook/Tracer

workplan.Plan ← Node + Edge + scheduler/executor/runner
```

Seele 不持有 Task、产品工具、工作区数据、费用账本或压缩触发策略；这些由 Seelex 显式装配。
