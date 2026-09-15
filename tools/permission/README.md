# tools/permission

`permission` 提供工具调用前的通用决策：扁平 `allow/ask/deny` 规则，以及 Linux 式
「主体（Subject）× 路由组（PermissionGroup）× 位（r/w/x）+ sudo」模型；它不渲染
UI，也不持有 Agent 会话。

## 公开入口

| 符号 | 用途 |
| --- | --- |
| `PermissionConfig`、`PermissionRule` | 声明模式、工具名/参数匹配与可选位要求（`Rule.Bits`） |
| `PermissionGroup` | 路由组：名字 glob `Match` → 所需位 `Mode` + 组默认动作 `Default` |
| `Subject`、`SubjectGrant`、`GrantBit`、`SudoMode` | 主体及其按组的位 / 资源 / sudo 授权 |
| `PermissionChecker` | 编译并执行权限判断 |
| `Check`、`CheckFor`、`VisibleFor` | 旧扁平入口 / 带主体入口 / 可见性查询 |
| `ApprovalRequest`、`ApprovalResponse`、`Scope*` | 与 CLI、TUI 或 HTTP 审批层交换数据 |
| `SetElevationAuditor`、`GrantElevation`、`ElevationEvent` | 提权范围与审计 |
| `SubjectResolver`、`BitEnforcer` | 主体解析与位 / 沙箱挂载点（由网关安装；`Gate.Enforcer` 也会调用） |
| `Gate`、`Gate.Middleware`、`Gate.Decide` | 中间件形态的判定：装配自由，判定集中在中间件 |
| `DecideForMeta`、`VisibleForMeta` | 簇属优先的判定（工具自带 `ToolMeta` 优先于名字路由） |
| `Engine`、`WithEngine`、`EngineResolver` | 授权主体：session 下的 engine |
| `WithSessionID`、`SessionIDFromContext` | 审批请求的 `SessionID` 来源（ctx 透传） |
| `NewApprovalRequest`、`RiskOf`、`FormatPreview`、`DefaultApprovalTimeout` | 组装审批请求：ID / Risk / Preview / Timeout |
| `DenialError`、`InvisibilityError` | 英文拒绝错误（可 `errors.Is` 区分两类） |
| `Elevator`、`CheckerElevator` | 提权台账接口（由 harness 持有）与框架默认实现 |
| `SetElevationSource`、`Elevator()`、`Elevated` | 安装 / 读取 harness 的提权台账 |
| `NewChannelApprovalHandler` | 用 channel 适配异步审批实现（带超时与 ctx 取消） |

## 求值顺序

`PermissionChecker.CheckFor(subject, tool, args)`：

1. `full_access` → 直接放行。
2. allow 缓存命中 → 放行。
3. 按工具名路由到 `Groups` 中最后一个命中组（LMRW，与 Rules 一致）。
4. 若配置了 `Subjects`：判定主体位是否覆盖 `group.Mode`（含资源匹配）；断位取
   `MissingBit`（零值 = `ask`）。
5. 最后套用 `Rules`（永远最后、最细，覆盖组默认）。

## 实现细节

- 位常量为 `BitRead=4 / BitWrite=2 / BitExecute=1`，与 `tools` 包同源（重导出）。
- `Check(tool, args)` 是 `CheckFor(SubjectAnonymous, tool, args)` 的薄封装；未
  配置 `Groups/Subjects` 时与历史逐字节一致。
- `VisibleFor` 在断位时返回 false（“不在 PATH”）；`Subjects` 为空时恒为 true。
- 提权按 `Scope` 复用：`once` 不持久化，`session` 仅同 subject+group，`tool` 同
  工具，`args` 同工具+参数；每次 `GrantElevation` 都发出审计事件。
- `PermissionChecker` 用 `RWMutex` 保护规则、主体表与提权表：`CheckFor` /
  `VisibleFor` 只读加锁，`GrantElevation` / `SetElevationAuditor` 写锁。

## 示例

```go
import "github.com/RedHuang-0622/Seele/tools/permission"

checker := permission.NewPermissionChecker(permission.PermissionConfig{
    Mode: permission.ModeManual,
    Groups: []permission.PermissionGroup{
        {Name: "ro", Match: []string{"read_*"}, Mode: permission.BitRead, Default: permission.ActionAllow},
        {Name: "ctl", Match: []string{"ctl_*"}, Mode: permission.BitExecute, Default: permission.ActionAsk},
    },
    Subjects: map[permission.Subject]permission.SubjectGrant{
        "guest": {Bits: map[string]permission.GrantBit{"ro": {Bits: permission.BitRead}}},
    },
})

checker.CheckFor("guest", "read_doc", `{}`)  // ResultAllow
checker.CheckFor("guest", "ctl_stop", `{}`)  // ResultAsk（断位 → MissingBit）
checker.VisibleFor("guest", "ctl_stop")      // false（不在 PATH）
```

## 中间件形态：装配自由，判定在中间件

判定既可以由内置门控（`tools/gateway`）完成，也可以作为**工具中间件**完成：装配
完全自由（任意 provider、任意顺序），只有 `Gate` 决定一次调用能否执行。

判定语义：

1. **工具自带簇属优先**：工具用 `ToolMeta{Kind, Groups, Bits}` 声明所属簇与所需位；
   主体在任一候选簇上持有所需位即视为可见，缺位 ⇒ 不在命名空间。
2. **未声明簇属时退回名字路由**：`Groups` 与 `Bits` 均为空 ⇒ 等价于
   `VisibleFor` + `CheckFor`。
3. **位挂载点最先判定**：`Gate.Enforcer`（`BitEnforcer`）在场时先问它；`ok=true`
   直接决定动作（`allow/deny/ask`），`ok=false` 表示不干预、继续往下判定。框架只
   调用接口，不含任何路径 / 命令解析逻辑。
4. **控制类簇属**：`Kind == control` 默认仅 `root` 可路由。
5. **授权主体 = session 下的 engine**：会话把 engine id 注入 context
   （`WithEngine`），授权表 `Subjects` 以 engine 为键。
6. **组默认 ask 的审批**：批准时把提权交给 `Elevator`（框架只调用接口）。
7. **拒绝 ⇒ 执行选择页面**：`Approval` 在场且未设 `Gate.DenyWithoutPrompt` 时，任何
   拒绝（不可见 / 策略拒绝 / 控制类 / enforcer 的 deny）都先呈现选择页面；通过只获得
   **单次操作的提权**（仅放行本次调用，不写 allow 缓存、不记提权），因此即使
   `full_access` 也不会被永久绕过。
8. **拒绝文案（英文）**：

   ```
   permission denied: cannot complete the invocation of tool "X"
   permission denied: cannot complete the invocation of tool "X": the tool is not in this engine's namespace
   ```

   两者分别满足 `errors.Is(ErrPermissionDenied)` / `errors.Is(ErrToolNotVisible)`。

### 审批请求：透出 ctx + ID/SessionID/Timeout/Risk

框架把呈现选择页面所需的信息填齐（`NewApprovalRequest`）：

| 字段 | 来源 | 说明 |
| --- | --- | --- |
| `ApprovalContext.Context` | 调用 ctx | 原样透传，harness 可读会话 id / 追踪器 / 截止时间 |
| `ID` | 框架自增 | 全局唯一，供回执 `ApprovalResponse.RequestID` 关联 |
| `SessionID` | `SessionIDFromContext(ctx)` | 用 `WithSessionID` 注入 |
| `Timeout` | `Gate.Timeout`，`<=0` 取 `DefaultApprovalTimeout` | 超时视作拒绝 |
| `Risk` | `RiskOf(name, meta)` | control/admin=high、write=medium、read=low，未声明 `Kind` 按名回退 |
| `Preview` | `FormatPreview(name, args)` | 截断到 80 字符的调用预览 |

`NewChannelApprovalHandler` 是 channel 版适配器：先挂好回执通道再投递请求，等待时
同时监听**请求超时**与**调用 ctx 取消**（两者都视作拒绝）。

### 提权由 harness 完成

框架只提供接口：`ApprovalHandler`（执行选择页面）与 `Elevator`（提权台账）。提权是否
持久、按什么范围复用、如何落盘与呈现，都由 harness 决定；框架自带的
`CheckerElevator` 只是默认实现（寄存在 `PermissionChecker` 上，等价旧行为）。
`PermissionChecker.SetElevationSource` 可整体接管提权读写。

```go
gate := &permission.Gate{
    Checker:   permission.NewPermissionChecker(cfg), // 路由组 + 位 + engine 授权
    Approval:  host.AskUser,                          // 执行选择页面（harness）
    Elevation: host.Ledger,                           // permission.Elevator（harness）
    Enforcer:  host.Sandbox,                          // permission.BitEnforcer（harness）
    Timeout:   90 * time.Second,                      // 透出到 ApprovalRequest.Timeout
    // DenyWithoutPrompt: true,                       // 可选：拒绝直接返回英文错误
}
registry := tool.NewRegistry(tool.WithMetaMiddleware(gate.Middleware()))

// 会话在每个回合把本会话的 session id / engine id 注入 ctx：
ctx := permission.WithSessionID(ctx, "sess_42")
ctx = permission.WithEngine(ctx, "sess_42/engine_1")
registry.Dispatch(ctx, tool.ToolCall{Name: "delete_file", ArgumentsJSON: `{}`})
```

注入的 `Elevator` 不得回调 `PermissionChecker`（判定在读锁内查询台账）。

完整可运行示例：[`../../example_Implement/11_permission_middleware`](../../example_Implement/11_permission_middleware/README.md)。

## 依赖与验证

- 调用网关：[`../gateway/README.md`](../gateway/README.md)
- 模型说明：[`../../docs/arch/15-tool-permission-model.md`](../../docs/arch/15-tool-permission-model.md)
- 验证：`go test ./tools/permission/...`
