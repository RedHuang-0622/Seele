# workplan/runtime/checkpoint

该包保存和恢复 WorkPlan 的可序列化执行快照。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Store` | 快照存储接口 |
| `NewMemoryStore` | 测试或进程内运行的存储实现 |
| `Manager` | 保存、加载和删除快照的协调器 |

## 实现细节

- `MemoryStore` 用锁保护快照 map，供并发测试和简单嵌入式使用。
- `Manager` 只依赖 `Store` 与 core `Snapshot`，持久化后端可替换而不会影响 Runner。
- 快照保存状态数据；条件函数等不可序列化依赖由条件注册表在恢复时重新绑定。

## 依赖与验证

- 模型：[core/types](../../core/types/README.md)
- 验证：`go test ./workplan/runtime/checkpoint/...`
