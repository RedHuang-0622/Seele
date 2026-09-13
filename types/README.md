# types

`types` 定义跨模块共享的数据模型和最小 LLM 完成接口；该包不依赖业务包，是依赖图的基础层。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `ChatCompleter` | 同步和流式 LLM 调用的公共接口 |
| `Message`、`Tool`、`ToolCall` | 对话与工具调用的 Provider 无关模型 |
| `LLMConfig`、`AppConfig` | 启动和 Provider 配置模型 |
| `ImagePart` | 随消息下发的图片（原始字节 + 像素尺寸 + 清晰度偏好），由 Provider 策略投影为 content parts |
| `StreamEvent`、`Usage` | 流式事件与用量统计 |

## 实现细节

- 模型以 JSON/YAML 标签定义稳定的外部表示，具体 Provider 在边界层负责转换。
- `ChatCompleter` 使 `session`、上下文压缩和 WorkPlan 能依赖抽象，而不是 `api.ChatClient` 的具体实现。
- `Message.Content` 仍是**文本投影**，多模态图片放在 `Message.Images`：历史、上下文压缩与 token 估算只读文本，不受图片影响。
- `Message` 的 JSON 形状向后兼容：纯文本消息输出 `"content":"…"`（与历史一致），带图消息才展开为 content parts（`text` + `image_url` data URL）；Anthropic 的 `image`/`source` 形态也能反解回 `ImagePart`。
- 图片字节与像素尺寸只存在本地：wire 只带 data URL / base64，尺寸供限流按 tile 计价。
- 共享类型不引入 `agent`、`session` 或 `workplan` 依赖，从而保持无循环依赖。

## 依赖与验证

- 使用方：[agent/core/api](../agent/core/api/README.md)、[session](../session/README.md)、[seelectx](../seelectx/README.md)
- 验证：`go test ./types/...`
