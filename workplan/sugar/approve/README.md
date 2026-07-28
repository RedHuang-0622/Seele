# workplan/sugar/approve

该包构建需要外部确认的审批节点，适合高风险工具调用或人工选择分支。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `Add` / `NewNode` | 创建审批节点 |
| `ApprovalGate` | 提交问题并取得用户选择的接口 |
| `Question`、`ChoiceOption` | 审批问题和可选答案模型 |
| `Choices` / `DefaultOptions` | 创建标准选项 |

## 实现细节

- 节点将问题、上下文键值、超时和选项交给 Gate；CLI、网络或 TUI Gate 可独立实现。
- 审批返回值以显式节点结果进入上下文，后续边可根据结果分支。
- 该包不直接读取终端，顶层 `workplan` 提供可选 Gate 实现。

## 依赖与验证

- Gate：[workplan/gate.go](../../gate.go)
- 验证：`go test ./workplan/sugar/approve/...`
