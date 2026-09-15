# tools/gateway

`gateway` 把 Holder 收敛为 Agent 可注入的可见工具与调用边界，并在执行前装配通用权限和审批；它不实现具体工具或产品 UX。

## 公开接口

| 符号 | 用途 |
| --- | --- |
| `Gateway` | 查询全量/可见工具并执行调用的抽象 |
| `NewDefaultGateway` | 创建基于 Holder 的默认实现 |
| `SetPermissionConfig` | 注入规则 / 路由组 / 主体授权与审批 handler |
| `SetSubjectResolver` | 安装主体解析器，把调用主体接入权限模型 |
| `SetBitEnforcer` | 安装位与沙箱 / 路径的挂载点 |
| `Subject`、`ToolMeta` | 读取当前主体与工具元数据（含控制信号） |
| `ActivatePlugin` | 切换 Holder 中的工具可见集合 |

## 实现细节

- `VisibleTools` 叠加插件可见性与主体可见性：断位工具（不在 PATH）与非 root 的控制类工具不会返回。
- `Dispatch` 在调用 Holder 前检查权限，并把结果映射为可 `errors.Is` 辨别的两类错误：断位 ⇒ `tools.ErrToolNotVisible`，位齐被拒 ⇒ `tools.ErrPermissionDenied`。
- `ToolMeta.Kind == control` 的工具默认仅 `SubjectRoot` 可路由；`Signal` 经 `ToolMeta(name)` 透出给上层 loop。
- `ask` 结果由注入的 `ApprovalHandler` 处理；记忆允许结果时向 checker 增加精确规则，并按 `ApprovalResponse.Scope` 记录提权（发审计事件）。
- `assessRisk` 优先读 `ToolMeta.Kind`，无 Meta 时回退旧的按名 switch；Gateway 透传调用 context，取消、截止时间和 trace 可跨越权限层到达 handler。

## 依赖与验证

- Holder：[`../holder/README.md`](../holder/README.md)
- 权限模型：[`../permission/README.md`](../permission/README.md)
- 验证：`go test ./tools/gateway/...`
