# 11_permission_middleware

场景：**装配自由 + 判定交给中间件 + 授权绑定到 session 下的 engine + 拒绝时给出执行选择页面（提权由 harness 完成）**。完全离线，无需 LLM 或凭据。

## 分层

| 层 | 职责 |
| --- | --- |
| 框架层 `tools/permission` | 判定引擎与**接口**：`Gate` 中间件、`ApprovalHandler`、`Elevator`、`BitEnforcer`、英文拒绝错误。不定义提权策略。 |
| harness 层（宿主） | **执行选择页面**（UI）、**提权台账**（`Elevator` 实现）、**位挂载点**（`BitEnforcer` 实现：沙箱 / 路径）。 |

## 要点

1. **工具自带簇属**：`ToolMeta{Kind, Groups, Bits}` 声明「属于哪个簇、需要哪些位」。
2. **装配自由**：任意 provider 按任意顺序注册进 `tools.Registry`，注册期不做门槛。
3. **判定在中间件**：`Gate.Middleware()` 作为 `MetaMiddleware` 包住每个工具；授权表 `Subjects` 以 session 下的 engine 为键（`permission.WithEngine` 注入）。
4. **位挂载点最先判定**：`Gate.Enforcer` 是 harness 提供的沙箱 / 路径挂载点，`ok=true` 时直接决定动作（`allow/deny/ask`），`ok=false` 表示不干预、回退到位判定。框架只调用接口，不含任何路径 / 命令解析逻辑。
5. **拒绝 ⇒ 执行选择页面**：`Approval` 在场且未设 `DenyWithoutPrompt` 时，任何拒绝（不可见 / 策略拒绝 / 控制类 / enforcer 的 deny）都会先呈现选择页面；通过审批只获得**单次操作的提权**（仅放行本次调用，不写 allow 缓存、不记提权），因此即使是全权（`full_access`）也不会被永久绕过。
6. **审批请求信息齐备**：`ApprovalContext.Context` 原样透传调用 ctx，`ApprovalRequest` 带 `ID`（全局唯一）、`SessionID`（`WithSessionID` 注入）、`Timeout`（`Gate.Timeout`，缺省 `DefaultApprovalTimeout`）、`Risk`（由簇属推断：control/admin=high、write=medium、read=low）、`Preview`。

## 拒绝文案（英文）

```
permission denied: cannot complete the invocation of tool "ctl_stop": the tool is not in this engine's namespace
permission denied: cannot complete the invocation of tool "write_doc"
```

两者共享同一英文前缀；`errors.Is(err, tools.ErrToolNotVisible)` 与
`errors.Is(err, tools.ErrPermissionDenied)` 仍能区分两类拒绝。

## 运行

```powershell
go run ./example_Implement/11_permission_middleware
```

预期输出（节选；`id=appr-…` 每次不同）：

```
A. 判定为 manual + harness 位挂载点；harness 提供执行选择页面（仅 delete_file 批准）：
  sess_42/engine_1 read_doc     -> OK   {"ok":true,"tool":"read_doc"}
  sess_42/engine_1 write_doc    -> OK   {"ok":true,"tool":"write_doc"}
  -- delete_file 不可见（缺 ops 位）；批准后只放行本次调用 --
    [选择页面] id=appr-…-1 session=sess_42 risk=high timeout=2m0s preview=delete_file({})
  sess_42/engine_1 delete_file  -> OK   {"ok":true,"tool":"delete_file"}
    [选择页面] id=appr-…-2 session=sess_42 risk=high timeout=2m0s preview=delete_file({})
  sess_42/engine_1 delete_file  -> OK   {"ok":true,"tool":"delete_file"}
  -- engine_2 位齐但 sandbox 拒绝（/etc/protected）；enforcer 的 deny 同样走选择页面 --
    [选择页面] id=appr-…-3 session=sess_42 risk=high timeout=2m0s preview=delete_file({"path":"/etc/protected"})
  sess_42/engine_2 delete_file  -> OK   {"ok":true,"tool":"delete_file"}
  -- 控制类簇属默认仅 root；选择页面被拒 ⇒ 英文错误 --
    [选择页面] id=appr-…-4 session=sess_42 risk=high timeout=2m0s preview=ctl_stop({})
  sess_42/engine_1 ctl_stop     -> DENIED(不可见) permission denied: cannot complete the invocation of tool "ctl_stop": the tool is not in this engine's namespace
    [选择页面] id=appr-…-5 session=sess_42 risk=low timeout=2m0s preview=read_doc({})
                 read_doc     -> DENIED(不可见) permission denied: cannot complete the invocation of tool "read_doc": the tool is not in this engine's namespace
  执行选择页面呈现次数 = 5

B. 判定为 full_access（全权）；控制类被拒时仍给出执行选择页面：
  sess_42/engine_1 ctl_stop     -> OK   {"ok":true,"tool":"ctl_stop"}
  执行选择页面呈现次数 = 1（仅控制类那次）

C. harness 持有提权台账（permission.Elevator）：
    [harness] 记录提权：tool=delete_file scope=session
  执行选择页面呈现次数 = 1；harness 记录提权 = 1 次
```

要点：

- A 组两次 `delete_file` 各询问一次 —— 单次提权**不持久**。
- A 组 `engine_2` 位齐却仍被 sandbox 拒绝 —— enforcer 的 deny 也走选择页面。
- B 组即使 `full_access`，控制类调用仍先给选择页面；普通工具直接放行。
- C 组 `Scope=session` 的提权由 **harness 台账**持有，第二次调用直接复用、不再询问。

## 接进真实会话

```go
// harness 层：执行选择页面 + 提权台账 + 位挂载点（实现框架定义的接口）
gate := &permission.Gate{
    Checker:   permission.NewPermissionChecker(cfg), // 授权表以 engine 为键
    Approval:  host.AskUser,                          // 执行选择页面
    Elevation: host.Ledger,                           // permission.Elevator
    Enforcer:  host.Sandbox,                          // permission.BitEnforcer
    Timeout:   90 * time.Second,                      // 透出到 ApprovalRequest.Timeout
}

// 框架层：装配自由，判定只在中间件完成
registry := tool.NewRegistry(tool.WithMetaMiddleware(gate.Middleware()))
runtime, _ := bridge.NewRegistryRuntime(registry) // tools.Registry -> agent.ToolRuntime
engine, _ := agent.NewWithComponents(agent.Components{Completer: llm, Tools: runtime})
sess, _ := session.NewSession(session.SessionComponents{Agent: engine})

// 每个回合把本会话的 engine id / session id 注入 ctx，再交给 Session.Chat：
ctx = permission.WithSessionID(ctx, "sess_42")
ctx = permission.WithEngine(ctx, "sess_42/engine_1")
sess.Chat(ctx, "...")
```

`PermissionChecker.SetElevationSource(ledger)` 让 harness 同时接管**判定时的提权读取**；
不注入时框架使用自带的默认台账 `CheckerElevator`。

> 注入的 `Elevator` 不得回调 `PermissionChecker`（判定在读锁内查询台账）。
