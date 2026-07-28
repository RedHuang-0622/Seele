# agent/gateway

网关层是 Agent 对外操作的边界：API 网关负责选择可用账户，工具网关负责返回或执行当前可见的工具。

| 包 | 功能 | 关键实现 |
| --- | --- | --- |
| [api/](api/README.md) | 账户获取 | 委托 `AccountPool`，不理解 HTTP 协议 |
| [tool/](tool/README.md) | 工具可见性与调用 | 在 Holder 之上叠加插件和权限控制 |

将选择逻辑放在网关，能让 `agent` 保持为装配协调者，也让各调用路径共享同一套规则。
