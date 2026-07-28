# agent/core/tool/builtin

该包提供本地内置工具：文件读取/编辑、grep、git、shell、模式匹配和 WorkPlan 工具。

## 实现要点

- 每个工具实现 `interfaces.ToolProvider`，`RegisterAll` 将它们交给 Holder；工具定义和 Handler 保持在同一对象中。
- shell 工具接受超时配置；文件和 git 工具通过 JSON 参数执行受限的本地操作。
- `WorkPlanTool` 将可声明的工作流图转换为一个可由 LLM 调用的工具，并通过 `NewChatAgentFactory` 接入 LLM。
- `builtin_smoke_test`、`e2e_cli_test` 和专项测试覆盖注册、Schema 与命令行路径。

## 公开入口与验证

- `RegisterAll` 注册标准工具；各 `New...Tool` 构造函数支持按需组合。
- 验证：`go test ./agent/core/tool/builtin/...`
