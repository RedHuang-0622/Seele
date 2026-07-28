# agent/core/function

该包负责把框架的 `types.Tool` 和 `types.ToolCall` 转换成 Provider 所要求的工具定义与调用格式。

## 实现要点

- `Strategy` 定义工具编码与 tool-call 解码接口，注册表按名称查找策略。
- OpenAI 策略生成 `type:function` 结构；Anthropic 策略生成 `input_schema` 与 `tool_use` 结构。
- 注册表以互斥锁保护，允许应用在初始化阶段注册新的 Provider 工具格式。

它不处理 HTTP 请求；HTTP/SSE 协议差异属于 [api/](../api/README.md)。

## 公开入口与验证

- `Strategy`、`Register`、`Get` 与 `Names` 管理工具格式策略。
- 验证：`go test ./agent/core/function/...`
