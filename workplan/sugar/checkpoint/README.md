# workplan/sugar/checkpoint

该包构建显式检查点节点，让工作流在指定位置生成可恢复快照。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `NewNode` | 创建检查点节点 |
| `CheckpointNode` | 标识检查点语义 |

## 实现细节

- 节点在执行路径上声明“此处应保存”，实际快照存储由 Runner 注入的 checkpoint Manager 完成。
- 声明与存储后端解耦，图定义可以在内存测试和持久化部署间复用。

## 依赖与验证

- 检查点运行时：[runtime/checkpoint](../../runtime/checkpoint/README.md)
- 验证：`go test ./workplan/sugar/checkpoint/...`
