# 03_workplan

该示例覆盖 WorkPlan 的顺序步骤、分支、循环、Fork、审批、DSL sugar 和图检查。

## 运行

```powershell
go run ./example_Implement/03_workplan
```

## 实现细节

- 示例通过 [`agent/bridge.NewAgentFactory`](../../agent/bridge/README.md) 将装配好的 Agent runtime 注入节点，不让 WorkPlan 内核依赖具体 Agent。
- 各 `Example...` 函数分别构建一种控制流，便于对照 `workplan` 链式 API 与运行时图语义。

## 验证

- `go build ./example_Implement/03_workplan`
