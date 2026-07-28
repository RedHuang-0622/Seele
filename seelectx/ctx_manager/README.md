# seelectx/ctx_manager

该包负责在有限上下文窗口内管理消息历史：估算 token、截断工具结果、裁剪旧消息，并在阈值达到时调用 LLM 生成摘要。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `EstimateTokens` / `EstimateHistoryTokens` | 估算文本和历史预算 |
| `TrimHistory` / `TruncateToolResult` | 无 LLM 的确定性压缩 |
| `NeedCompression` / `CompressHistory` | 判断并执行 LLM 摘要压缩 |
| `Config` | 上下文上限与压缩阈值 |

## 实现细节

- 先执行确定性的工具结果截断与历史裁剪，再在超过阈值时调用 `types.ChatCompleter` 生成摘要，降低不必要的 LLM 调用。
- token 估算是启发式预算而非 tokenizer 精确计数，因此配置以安全阈值使用。
- 压缩依赖最小 `ChatCompleter` 接口，不反向依赖 `agent/core/api`。

## 依赖与验证

- 共享模型：[types](../../types/README.md)
- 验证：`go test ./seelectx/ctx_manager/...`
