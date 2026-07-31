# 架构文档

这里记录跨包、跨模块且需要长期稳定的边界与扩展契约。单个 Go package 的 API 和实现细节以对应目录的 `README.md` 为准。

## 文档索引

| 文档 | 内容 |
| --- | --- |
| [09-seele-runtime-boundary.md](09-seele-runtime-boundary.md) | 五个平行根能力、依赖方向和数据流 |
| [10-seele-extension-contracts.md](10-seele-extension-contracts.md) | Agent、WorkPlan、AccountPool、Tools、Seelex Context、Telemetry 的接口设计 |
| [11-seele-boundary-test-strategy.md](11-seele-boundary-test-strategy.md) | 分层测试、真实 API/subagent 冒烟和验收标准 |

阅读顺序：先读运行时边界，再读扩展契约，最后按测试方案验收实现。

