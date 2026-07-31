# session

`session` 是 Agent 之上的会话 ChatLoop：它把调用方提供的抽象 `Agent`、history、上下文组件和 telemetry 组装成可复用的 Chat/ChatStream 会话。它不创建 provider、账号池或产品工具，也不依赖具体 `agent` 包。

## 公共接口

| API | 用途 |
| --- | --- |
| `NewSession` | 从 `SessionComponents` 创建显式会话 |
| `Session.Chat` / `ChatStream` | 执行一次同步或流式对话 |
| `Session.Reset` | 显式清空 working history 与已注入的 durable history，开始新会话 |
| `NewReActLoop` | 创建底层 ReAct 执行循环 |
| `SessionComponents` / `ContextComponents` | 注入 history、请求装配、结果处理和 telemetry |

## 实现边界

- 会话只持有调用方注入的 `Agent` 和可选 history；`agent.Agent` 只是这一接口的一种实现，不隐式创建全局 provider。
- `ClearHistory` 仅清空当前 working view 以兼容底层 Loop；需要同时清除持久快照时使用可返回错误的 `Reset(ctx)`。
- ReActLoop 默认不主动压缩上下文；压缩必须通过显式 Compressor 或 ContextController 装配。

## 并发约束

一个 `Session` 会串行执行 `Chat` 与 `ChatStream`，以保护同一份 working history。多个 Session 可以并发执行；若它们共享同一个 `DurableHistory`，冲突解决与持久化一致性由该 history 实现负责。流式回调不得重入同一个 Session。

## 协作与验证

- 上游装配入口：[agent](../agent/README.md)
- 上下文原语：[seelectx](../seelectx/README.md)
- 验证：`go test ./session/... -count=1`
