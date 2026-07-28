# workplan/sugar/loop

该包构建循环节点，并用 `Signal` 暴露每次迭代的实时状态。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `NewNode` | 创建循环节点 |
| `Signal` / `NewSignal` | 订阅迭代结果与完成信号 |
| `WithUntil`、`WithMaxIter`、`WithOnExhausted` | 配置终止条件和耗尽分支 |

## 实现细节

- `Signal` 用锁保护当前值、迭代计数和回调，并用 `sync.Once` 保证完成通知只关闭一次。
- Loop 节点保存 body ID 和终止配置，实际重复执行由 runtime 根据节点语义调度。
- 达到条件、最大次数或耗尽分支时均形成显式控制结果，避免隐式无限循环。

## 依赖与验证

- 运行时：[runtime/scheduler](../../runtime/scheduler/README.md)
- 验证：`go test ./workplan/sugar/loop/...`
