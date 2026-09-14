# types

`types` 定义跨模块共享的数据模型和最小 LLM 完成接口；该包不依赖业务包，是依赖图的基础层。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `ChatCompleter` | 同步和流式 LLM 调用的公共接口 |
| `Message`、`Tool`、`ToolCall` | 对话与工具调用的 Provider 无关模型 |
| `LLMConfig`、`AppConfig` | 启动和 Provider 配置模型 |
| `FilePart`、`FileKind` | 消息附件载体（种类 + 原始字节 / URL / Files API `file_id` + 像素尺寸），由 Provider 策略投影为 content parts |
| `StreamEvent`、`Usage` | 流式事件与用量统计 |

## 实现细节

- 模型以 JSON/YAML 标签定义稳定的外部表示，具体 Provider 在边界层负责转换。
- `ChatCompleter` 使 `session`、上下文压缩和 WorkPlan 能依赖抽象，而不是 `api.ChatClient` 的具体实现。
- `Message.Content` 仍是**文本投影**，多模态附件放在 `Message.Files`（`FilePart`）：历史、上下文压缩与 token 估算只读文本，不受附件影响。
- `Message` 的 JSON 形状向后兼容：纯文本消息输出 `"content":"…"`（与历史一致），带附件消息才展开为 content parts（`text` + `image_url` data URL / `file.file_data` / 扁平 `file_id`）；Anthropic 的 `image`、`document` 形态也能反解回 `FilePart`。
- `FilePart.Kind` 决定发射哪种内容块（`image` → `image_url`/`image`，`document` → `file`/`document`），留空时按 MIME 推断；`Data`、`URL`、`FileID`（Files API 引用）三者互斥，`Validate()` 在请求前拦下不自洽的附件。
- 附件字节与像素尺寸只存在本地：wire 只带 data URL / base64 或 `file_id`，尺寸供限流按 tile 计价。
- 共享类型不引入 `agent`、`session` 或 `workplan` 依赖，从而保持无循环依赖。

## 依赖与验证

- 使用方：[agent/core/api](../agent/core/api/README.md)、[session](../session/README.md)、[seelectx](../seelectx/README.md)、[limits](../limits/README.md)
- 验证：`go test ./types/...`
