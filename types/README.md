# types

`types` 定义跨模块共享的数据模型和最小 LLM 完成接口；该包不依赖业务包，是依赖图的基础层。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `ChatCompleter` | 同步和流式 LLM 调用的公共接口 |
| `Message`、`Tool`、`ToolCall` | 对话与工具调用的 Provider 无关模型 |
| `LLMConfig`、`AppConfig` | 启动和 Provider 配置模型 |
| `StreamEvent`、`Usage` | 流式事件与用量统计 |

## 实现细节

- 模型以 JSON/YAML 标签定义稳定的外部表示，具体 Provider 在边界层负责转换。
- `ChatCompleter` 使 `session`、上下文压缩和 WorkPlan 能依赖抽象，而不是 `api.ChatClient` 的具体实现。
- 共享类型不引入 `agent`、`session` 或 `workplan` 依赖，从而保持无循环依赖。

## 依赖与验证

- 使用方：[agent/core/api](../agent/core/api/README.md)、[session](../session/README.md)、[seelectx](../seelectx/README.md)
- 验证：`go test ./types/...`
