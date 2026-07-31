# errors

根目录统一错误包，为 `agent`、`workplan`、`tools` 和适配层提供可序列化的错误信封。它只依赖 Go 标准库，不反向依赖任何业务模块。

## 公共接口

| API | 用途 |
| --- | --- |
| `Error` | 结构化错误，包含 `code`、`message`、`struct`、`function`、`step`、`path`、`raw` 和可选行列号 |
| `New` | 创建无底层 cause 的错误 |
| `Wrap` / `With` | 将任意错误包装或补充结构化上下文 |
| `From` | 从错误链提取第一个 `*Error` |

## 错误字段

- `Struct`：发生错误的数据结构或模块结构。
- `Function`：执行失败的函数或接口操作。
- `Step`：流程中的语义步骤，例如 `nodes[0]` 或 `validate`。
- `Path`：输入文档中的 JSONPath。
- `Raw`：不透明原始信息，通常是被拒绝的 DTO 或 provider 响应摘要。

`Reason` 仅用于兼容旧 codec API；序列化时会映射为 `message`。

## 验证

```powershell
go test ./errors/... -count=1
```
