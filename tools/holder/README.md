# tools/holder

`holder` 聚合多个 `tools.ToolProvider`，建立工具名称索引并提供内联函数、插件可见性和调用分发；它不选择 LLM，也不实现产品工具。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `New`、`NewWithConfig` | 创建 Holder |
| `Register`、`Unregister` | 管理 Provider |
| `RegisterInline` | 将普通 Go 函数注册为 Function Calling 工具 |
| `Tools`、`Dispatch` | 导出模型定义并按名称调用 handler |
| `PluginManager` | 用 include/exclude 规则控制工具集合 |

## 实现细节

- Provider 变化时重建名称到 `ToolEntry` 的快照，读路径通过原子指针完成 O(1) 查找。
- 分发只对 `tools.ErrUnavailable` 重试，并把 timeout 派生到 handler 的 `context.Context`。
- `_` 开头的内部工具保留分发能力，但不会出现在 LLM 可见定义列表中。

## 依赖与验证

- 根合约：[`../README.md`](../README.md)
- 可见性和权限：[`../gateway/README.md`](../gateway/README.md)
- 验证：`go test ./tools/holder/...`
