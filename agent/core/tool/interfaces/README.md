# agent/core/tool/interfaces

该包定义工具层最小抽象：`ToolProvider` 提供工具列表，`ToolEntry` 绑定定义与执行器，`ToolHandler` 执行一次调用。

## 实现要点

- `ToolEntry` 将工具描述与执行行为组合，Holder 无需知道工具来自内置、Hub 或 MCP。
- 所有 Handler 以 `context.Context` 和 JSON 参数工作，使超时、取消与重试能由统一调度层处理。
- 接口位于使用方侧，Provider 实现只依赖这些稳定抽象，避免包间循环依赖。

## 验证

- `go test ./agent/core/tool/interfaces/...`
