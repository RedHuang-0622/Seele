# engine

`engine` 提供可装配的 ReAct 执行循环；它维护单次执行的 working history，但不会隐式创建 durable history 或主动压缩上下文。

## 显式上下文装配

`ReActLoop` 只依赖 `ToolRuntime`、LLM client 和可选 seelectx 接口。通过 `WithHistoryOwner` 分离 working history 与 durable history，通过 `WithRequestAssembler`/`WithPromptBlocks` 选择 prompt 拼装，通过 `WithToolResultProcessor` 选择 raw、筛选或引用结果，通过 `WithContextController` 显式接收结构化上下文事件。没有 controller 时 loop 不会触发上下文策略。

`engine` 是面向应用的 ReAct 会话入口：它把 `Agent`、历史、缓存、持久化和追踪组合成 `Chat`/`ChatStream` 调用；它不实现 Provider HTTP 协议或工具本身。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `New` 与 `Option` | 构造会话并注入缓存、存储、追踪、系统提示词和 Hook |
| `Engine.Chat` / `ChatStream` | 同步或流式执行一次 ReAct 会话 |
| `Loop` / `NewReActLoop` | 替换或构造循环实现 |
| `ReActLoop.CompressNow` | 由调用方显式请求一次通用历史压缩 |
| `ReActLoop.AppendHistory` | 显式装配或回放调用方持有的历史消息 |
| `SessionConfig` | 控制循环次数和上下文限制 |

## 实现细节

- 默认 `ReActLoop` 依次恢复历史、追加用户消息、取得可见工具、调用 LLM、分发 tool call，直到得到文本结果或达到循环上限；它不会按 token 阈值主动压缩或裁剪历史。
- `Engine` 使用函数选项把可选基础设施注入循环；未注入追踪时使用 `NoopTracer`，避免业务分支。
- Hook 在 LLM 和工具调用前后触发，供 CLI 或 UI 显示进度；缓存和存储通过接口实现，Engine 不绑定具体后端。
- `CompressNow` 先把候选 history 写入已注入的 storage/cache，再替换内存快照；持久化失败会返回错误并保留原 history。压缩触发、输入选取和结构化校验仍由上层调用方负责。

## 依赖与验证

- 装配和工具调度：[agent/](../agent/README.md)
- 会话缓存、历史和存储：[seelectx/](../seelectx/README.md)
- 验证：`go test ./engine/...`
