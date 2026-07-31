# tools/permission

`permission` 提供工具调用前的通用 allow、ask、deny 决策和审批数据结构；它不负责渲染 UI，也不持有 Agent 会话。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `PermissionConfig`、`PermissionRule` | 声明模式、工具名和参数匹配规则 |
| `PermissionChecker` | 编译并执行权限判断 |
| `ApprovalRequest`、`ApprovalResponse` | 与 CLI、TUI 或 HTTP 审批层交换数据 |
| `NewChannelApprovalHandler` | 用 channel 适配异步审批实现 |

## 实现细节

- `full_access` 默认放行；`manual` 模式在无命中规则时返回 `ask`。
- checker 按工具名和参数模式匹配规则，记忆允许选择时可以动态追加规则。
- channel handler 只传递请求和等待响应，不绑定任何界面框架。

## 依赖与验证

- 调用网关：[`../gateway/README.md`](../gateway/README.md)
- 验证：`go test ./tools/permission/...`
