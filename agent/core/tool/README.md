# agent/core/tool

`tool` 是工具域的公共入口。目前主要提供 `SchemaOf`：将 Go 结构体反射为 JSON Schema，供 LLM 调用工具时校验参数。

## 实现要点

- 反射处理基本类型、嵌套对象、数组、指针、`json` 标签、`desc` 标签和枚举信息。
- `EnumOf` 提供显式枚举片段；未导出字段与忽略字段不会暴露到 Schema。
- 工具执行与注册分别拆到子包，避免 Schema 层依赖具体 Provider 或进程协议。

相关子模块： [interfaces/](interfaces/README.md)、[holder/](holder/README.md)、[builtin/](builtin/README.md)、[hub/](hub/README.md)、[mcp/](mcp/README.md)、[permission/](permission/README.md)。

## 公开入口与验证

- `SchemaOf` 从 Go 值生成 JSON Schema，`EnumOf` 创建枚举定义。
- 验证：`go test ./agent/core/tool/...`
