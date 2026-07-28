# workplan/sugar/emit

该包构建 Emit 节点，将当前结果写入工作流上下文中的命名变量。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `NewNode` | 创建 Emit 节点 |
| `EmitNode` | 保存变量键 |

## 实现细节

- Emit 从前序结果提取值并写入 `WorkflowContext`，后续节点可通过模板读取该键。
- 写入由上下文的同步机制保护，Fork 之后仍保持一致的变量接口。
- 该节点只处理状态传递，不执行 LLM 或外部工具调用。

## 依赖与验证

- 上下文：[core/types](../../core/types/README.md)
- 验证：`go test ./workplan/sugar/emit/...`
