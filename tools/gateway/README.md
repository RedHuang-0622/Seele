# tools/gateway

`gateway` 把 Holder 收敛为 Agent 可注入的可见工具与调用边界，并在执行前装配通用权限和审批；它不实现具体工具或产品 UX。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `Gateway` | 查询全量/可见工具并执行调用的抽象 |
| `NewDefaultGateway` | 创建基于 Holder 的默认实现 |
| `SetPermissionConfig` | 注入 allow/ask/deny 规则与审批 handler |
| `ActivatePlugin` | 切换 Holder 中的工具可见集合 |

## 实现细节

- `VisibleTools` 只应用插件可见性，`Dispatch` 在调用 Holder 前执行权限检查。
- `ask` 结果由注入的 `ApprovalHandler` 处理；记忆允许结果时向 checker 增加精确规则。
- Gateway 透传调用 context，取消、截止时间和 trace 可跨越权限层到达 handler。

## 依赖与验证

- Holder：[`../holder/README.md`](../holder/README.md)
- 权限模型：[`../permission/README.md`](../permission/README.md)
- 验证：`go test ./tools/gateway/...`
