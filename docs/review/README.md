# Seele 设计文档

> 架构讲解与设计决策 · 生成时间: 2026-06-06

## 文档索引

| 序号 | 文档 | 内容 |
|------|------|------|
| 01 | [架构总览](01-architecture-overview.md) | 分层架构、依赖规则、数据流、文件组织 |
| 02 | [设计决策](02-design-decisions.md) | 14 个关键设计决策的"为什么" |
| 03 | [WorkPlan 引擎](03-workplan-engine.md) | 声明式 DAG、9 原语、两段式审批、Signal |
| 04 | [并发模型](04-concurrency-model.md) | 锁/Channel/goroutine 生命周期全梳理 |
| 05 | [上下文管理](05-context-management.md) | Token 估算、LLM 压缩、硬截断、配对保护 |
| 06 | [Provider 模型](06-provider-model.md) | ToolProvider 接口、Hub/MCP 实现、路由重试 |

## 阅读顺序建议

- **新人入门**：01 → 02（了解架构和设计哲学）
- **准备贡献代码**：01 → 04（了解并发安全约束）
- **深入 WorkPlan**：03 → 结合 `workplan/` 源码
- **集成工具**：06 → 结合 `provider/` 源码
- **调优性能**：05 → 结合 `history/` 源码

## 相关文档

- [review.md](../../review.md) — Bug 清单与修复方案
- [CHANGELOG.md](../../CHANGELOG.md) — 版本发布记录
- [ARCHITECTURE.md](../../ARCHITECTURE.md) — 基线架构文档
- [CODE_REVIEW.md](../../CODE_REVIEW.md) — 深度代码审查
- [README.md](../../README.md) — 使用指南
