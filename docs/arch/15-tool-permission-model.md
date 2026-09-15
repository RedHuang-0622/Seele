# 15 - 工具权限模型（Linux 式 rwx + sudo）

本文记录 `tools` 包树从扁平 `allow/ask/deny` 规则升格为 Linux 式权限模型的跨
模块设计与迁移方式。它跨 `tools`、`tools/permission`、`tools/gateway`、
`tools/holder` 多个 package，因此归档在 `docs/arch/`；各包的公开 API 细节以对应
`README.md` 为准。

## 1. 动机与边界

目标：产品层（Seelex）不再维护任何旁路的“工具可见性硬编码名单”，只用框架表达
「谁能用哪些工具、以什么位、是否需要 sudo 口令」。为此把权限拆成四维：

- **主体（Subject）**：不透明标识，`""` 表示匿名 / 进程级。
- **路由组（PermissionGroup）**：工具名 glob → 组，组声明所需位与默认动作。
- **位（Bits）**：`r=4 / w=2 / x=1`，与 Linux rwx 对齐。
- **sudo / 提权（Scope）**：被拒后的升级原语，`once | tool | session | args`。

非目标：框架不实现产品配置加载（YAML 留 Seelex）、不实现 UI / 审批弹窗 /
会话归属、不实现 OS 级沙箱或路径校验。位与沙箱的挂载点只暴露一个 `BitEnforcer`
钩子，框架内不出现任何路径或命令解析逻辑。

## 2. 分层与依赖方向

```
tools (root)          工具合约 + ToolMeta/ToolKind/位常量 + ErrToolNotVisible/ErrPermissionDenied
  └─ tools/permission 决策模型：Subject、PermissionGroup、SubjectGrant、PermissionChecker、BitEnforcer
  └─ tools/gateway    装配点：主体解析、位挂载、错误归类、控制类门控、可见性过滤
  └─ tools/holder     Provider 聚合 + Entry/Entries 元数据只读访问
```

关键约束：**`tools` 根包不依赖 `tools/permission`**。主体解析器
（`SubjectResolver`）与位挂载点（`BitEnforcer`）因此安装在 `DefaultGateway`
上，而不是 `Registry`；这样 `Registry` 的既有签名与依赖面都保持不变，挂载点也
落在真正执行门控的网关层。`tools/permission` 反向依赖 `tools` 根包，仅为复用
位常量与 `ToolMeta` 类型。

## 3. 元数据面（R2）

`ToolEntry` 新增**可选指针** `Meta *ToolMeta`：

```go
type ToolMeta struct {
    Kind       ToolKind // read | write | control | admin
    Groups     []string
    Bits       uint8    // 所需位
    Visibility []string // 可见主体；空 = 全员
    Resource   string
    Signal     string   // 仅 control：term|stop|chld|pause
}
```

零值安全：未声明 `Meta` 的旧 provider 得到 `nil`，行为与历史逐字节一致。
`Registry.Snapshot` 会深拷贝 `Meta`（含 `Groups`/`Visibility` 切片），避免外部
改动污染快照。

`Middleware` 原先只拿到工具名，读不到元数据。新增
`MetaMiddleware(name, meta, next)` 与 `Registry.WithMetaMiddleware(...)`：重建
快照时 `chainMeta` 在普通中间件之外再包一层，把 `entry.Meta`（nil 时为零值）
传给中间件。`Groups` 用于中间件按簇路由；权限判定用的是配置侧的
`PermissionGroup.Match`（名字 glob），两者概念一致但来源不同。

## 4. 判定顺序（R3 / R4）

`PermissionChecker.CheckFor(subject, tool, args)` 的求值：

1. `full_access` → `allow`。
2. allow 缓存命中 → `allow`。
3. **route**：在 `Groups` 中取最后一个命中 `Match` 的组（LMRW）。
4. **bits**：若配置了 `Subjects`，令 `required = group.Mode`；主体对
   `group.Name` 的 `GrantBit.Bits` 需 `Bits & required == required` 且资源匹配；
   否则取 `MissingBit`（零值 = `ask`）。
5. **rules**：最后套用 `Rules`（永远最后、最细，覆盖组默认）。

`Check(tool, args)` 是 `CheckFor(SubjectAnonymous, tool, args)` 的薄封装：当
`Groups`/`Subjects`/`MissingBit` 均为零值时退化为纯 Rules 旧行为。

`VisibleFor(subject, tool)` 复用同一判定，只取“位是否齐”这一维：断位 → false
（“不在 PATH”），从而不出现在 `VisibleTools` 列表。

## 5. 两类“不可用”（R7）

| 情形 | 错误 | 类比 |
| --- | --- | --- |
| 断位（缺组所需位） | `tools.ErrToolNotVisible` | 不在 PATH |
| 位齐但策略拒绝 | `tools.ErrPermissionDenied` | EPERM |

`DefaultGateway.checkPermission` 先判可见性（断位 / 控制类非 root →
`ErrToolNotVisible`），再取 `CheckFor` 动作（`deny` / 审批被拒 →
`ErrPermissionDenied`），两者都可用 `errors.Is` 辨别。

## 6. 提权与审计（R5）

`ApprovalResponse` 新增可选 `Scope`（空 = `once`）。审批通过时
`DefaultGateway.approve` 调用 `PermissionChecker.GrantElevation(subject, scope,
tool, args)`：`once` 不持久化，`session` 仅在同一 subject+group 复用，`tool` 同
工具、`args` 同工具+参数。每次提权都回调 `ElevationAuditor`，事件含主体、组、
工具、范围与时间。
提权一旦记录，`CheckFor` 会把它作为覆盖组默认 `ask` 的依据：同一 scope 内命中的
工具不再重复弹审批（Rules 仍最后生效，显式 `deny` 不被绕过）。

## 7. 控制类与信号（R6）

`ToolMeta.Kind == control` 的工具默认仅 `SubjectRoot`（`"root"`）可路由；对其它
主体表现为不可路由（`ErrToolNotVisible`）。`Signal`（`term|stop|chld|pause`）
不被框架解释，通过 `DefaultGateway.ToolMeta(name)` 透出给上层 loop。

## 8. 位与沙箱挂载点（R8）

```go
type BitEnforcer interface {
    Enforce(ctx, subject, meta, toolName, argsJSON) (Action, bool)
}
```

`DefaultGateway.SetBitEnforcer` 与 `permission.Gate.Enforcer` 都可安装；`ok=false` 表示
框架不干预。框架只调用该钩子，不含任何路径 / 命令解析逻辑。产品用它在 `ProjectScope`
里做资源校验。两条路径都把调用 ctx 与解析后的主体透传给钩子，判定顺序一致：
enforcer → 控制类簇属 → 判定器。

## 9. 迁移指南：Seelex v0.2.0 → v0.3.0

1. **Check → CheckFor**：安装主体解析器，把 `pc.Check(name, args)` 换成
   `pc.CheckFor(subject, name, args)`：

   ```go
   gw.SetSubjectResolver(func(ctx context.Context) permission.Subject {
       return permission.Subject(currentUser(ctx))
   })
   ```

2. **硬编码名单 → Groups + Subjects**：把产品里“哪些工具可见”的旁路名单改写为
   `PermissionConfig.Groups`（名字 glob + `Mode` + `Default`）与
   `SubjectGrant`，删除产品侧的可见性判断代码。

3. **控制类工具**：为停机 / 挂起类工具声明
   `Meta: &tools.ToolMeta{Kind: tools.ToolKindControl, Signal: tools.SignalTerm}`；
   门控默认仅 root 可路由，`Signal` 由上层 loop 读取。

4. **错误处理**：用 `errors.Is(err, tools.ErrToolNotVisible)` 与
   `errors.Is(err, tools.ErrPermissionDenied)` 区分“不可见”和“被拒”。

兼容性：`PermissionConfig{Mode, Rules}` 字面量、`Check`、`NewPermissionChecker`、
`AddAllowRule`、`DefaultApproveOptions`、`ApprovalRequest` 字段、
`WithCallTimeout/WithMiddleware/WithDispatchRetries` 行为均保持不变。

## 10. 装配面：中间件判定与 engine 授权

判定除内置门控（`tools/gateway`）之外，还可以作为**工具中间件**完成：装配完全自由
（任意 provider、任意顺序），只有中间件决定一次调用能否执行。

- 主体 = session 下的 engine：会话把 engine id 注入 context（`permission.WithEngine`），
  授权表 `Subjects` 以 engine 为键。
- 拒绝 ⇒ 执行选择页面：拒绝（不可见 / 策略拒绝 / 控制类）先呈现 `ApprovalHandler`
  的执行选择页面；通过只获得**单次操作的提权**（仅放行本次调用，不写 allow 缓存、
  不记提权），因此即使判定为 `full_access` 也不会被永久绕过。
- 提权由 harness 完成：框架只提供 `ApprovalHandler`（选择页面）与 `Elevator`
  （提权台账）两个接口；`CheckerElevator` 是默认实现，`SetElevationSource` 可整体
  接管提权读写。
- 位挂载点接入中间件：`Gate.Enforcer`（`BitEnforcer`）最先参与 `Gate.evaluate`，
  `ok=true` 直接决定动作、`ok=false` 回退到位判定；与内置门控同一顺序、同一语义。
- 审批请求信息齐备：`ApprovalContext.Context` 原样透传调用 ctx；`ApprovalRequest`
  带 `ID`（全局唯一）、`SessionID`（`WithSessionID` 注入）、`Timeout`（`Gate.Timeout`，
  缺省 `DefaultApprovalTimeout`）、`Risk`（由簇属推断）、`Preview`（截断预览）。
  中间件与网关共用 `NewApprovalRequest`，两条路径字段一致。
- 工具自带簇属：`ToolMeta{Kind, Groups, Bits}` 声明所属簇与所需位；
  `PermissionChecker.DecideForMeta` 让工具声明优先于名字路由，未声明时退回名字
  路由（与旧路径一致）。内置门控也已改用该入口，两条路径语义一致。
- 拒绝文案（英文，可直接透出）：

  ```
  permission denied: cannot complete the invocation of tool "X"
  permission denied: cannot complete the invocation of tool "X": the tool is not in this engine's namespace
  ```

  两者分别 `errors.Is(tools.ErrPermissionDenied)` / `errors.Is(tools.ErrToolNotVisible)`。
- 可运行示例：`example_Implement/11_permission_middleware`。

## 11. 验证

- `go test ./tools/...`、`go vet ./tools/...`
- 判定矩阵：`tools/permission/checker_model_test.go`
  （`{main, sub} × {ro, rw, ctl, adm}`）
- 两类错误区分、可见性过滤、控制类与位钩子：`tools/gateway/gateway_permission_test.go`
- 元数据面：`tools/meta_test.go`
- 权限链路冒烟（假 LLM 全链路，含真实 API 可选分支）：
  `cmd/smoke/permission_smoke_test.go`（`go test ./cmd/smoke -run Permission -v -count=1`）
- 判定热路径基准与 pprof 热点：`tools/permission/bench_test.go`、
  `tools/gateway/bench_test.go`（`go test -bench . -benchmem -run '^$' ./tools/permission ./tools/gateway`）
