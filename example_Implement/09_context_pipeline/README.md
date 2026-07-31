# 09_context_pipeline

该示例展示 `seelectx` 如何作为可选上下文装配模块使用，同时保持压缩触发、结果筛选和持久化所有权在调用方。

## 运行

```powershell
go run ./example_Implement/09_context_pipeline
```

无需 API Key 或外部服务。压缩调用使用确定性的本地 `QuickChat` fake。

## 展示的 API 与场景

- `MemoryHistory`：由调用方显式持有和加载会话历史。
- `PromptBlock`、`PlaceholderRequestAssembler`：按 Plan、Skill、历史的顺序拼装请求，并在调用前解析动态占位符。
- `ToolResultProcessorFunc`：把原始工具结果筛选为可重取的 `result_ref`，避免把冗长日志塞回模型上下文。
- `RecursiveCompressor`：短对话不调用 LLM；长历史只有在调用方显式请求时才调用隔离的 `QuickChat`。

预期输出会依次显示 Plan/Skill/历史消息、筛选后的工具结果、压缩 checkpoint，以及 `short=0 long=1` 的调用统计。

## 验证

```powershell
go test ./example_Implement/09_context_pipeline -count=1
```
