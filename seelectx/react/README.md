# seelectx/react

该包将 `types.ChatCompleter` 的同步与流式结果适配成 ReAct 循环可消费的统一完成结果。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `CompletionStrategy` | 抽象一次完成调用 |
| `SyncStrategy` | 处理普通完成 |
| `StreamStrategy` / `StreamEventStrategy` | 处理流式文本和事件 |
| `CompletionResult` | 统一内容和 tool call 结果 |

## 实现细节

- 各策略把不同调用形式归一为内容指针和工具调用列表，调用循环不需要分叉处理协议。
- `ContentPtr` 保留“空文本”与“无内容”的语义，避免 tool-call-only 响应被误判。

## 依赖与验证

- 共享模型：[types](../../types/README.md)
- 验证：`go test ./seelectx/react/...`
