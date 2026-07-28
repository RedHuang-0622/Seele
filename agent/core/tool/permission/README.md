# agent/core/tool/permission

该包提供工具调用前的权限决策和人工审批模型。

## 实现要点

- `PermissionConfig` 以规则匹配动作并返回 allow、deny 或 approve 决策。
- `PermissionChecker` 编译规则后进行检查；需要审批时构造 `ApprovalContext`。
- `NewChannelApprovalHandler` 将审批请求送入 channel，使 CLI/TUI/HTTP 层可独立实现用户交互。
- 默认选项统一定义在类型层，调用方通过 `ApproveOption` 调整超时与默认结果。

## 公开入口与验证

- `NewPermissionChecker` 创建决策器；`NewChannelApprovalHandler` 对接异步审批 UI。
- 验证：`go test ./agent/core/tool/permission/...`
