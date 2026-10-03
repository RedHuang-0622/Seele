# Seele Release Notes

---

## v0.3.3 (2026-10-03) —— `jobs`：异步作业根能力（契约 + Manager + `jobs_manage`）

> **主题：把「一次长任务」升格为框架一等对象——派发 / 观察 / 取回 / 终止 / 销项共用一份契约与同一张表；框架只给契约与管理面，派发工具与执行体留给产品**

分工判据：**管理侧通用、派发侧产品化**。「观察 / 取回 / 终止 / 销项」对任何作业语义一致 ⇒ 进框架；
与具体命令和提示词语义绑定的派发工具（`bash_bg` / `read_batch` / `fork_subagents`）留在产品。
因此 `jobs` **不含**任何派发工具、**不含**任何执行体，也**不 import 任何产品包**（边界门禁）。

### 🏗 01 — `jobs`：契约

- `Kind`（开放字符串；框架只解释 `process` / `inline`）、`State`（`running` / `done` / `failed` / `killed`）、
  `Handle`（`a<seq>`，seq 单调；禁用 `len(table)` 推——驱逐后会重号）
- `Scope{Session, Subject}`：**两个并列字段，不拼字符串**——`Session` 是隔离与回收粒度、`Subject` 是主体
  分组（`emp_<role>`）；两者都参加鉴权，`Spec.Node` / `Spec.Batch` 只做归属打点（照抄会话键与批次印章的分工）
- `Spec{Kind, Scope, Node, Batch, Description, Payload, Index, Dedup, OutputPath, Handle}`（`Handle` 登记时回填）；`Record` 是只读投影
  （状态 / 退出码 / 归属 / `OutputRef`（输出文件路径）/ 字节-行-游标 / 截断 / 终态摘要 ≤512 B）
- `Manager`：`Dispatch / Declare / Start / Observe / Peek / Fetch / Complete / Kill / Done / Snapshot /
  Reclaim / Events / ScopeOf / RetiredState / Close`
- `Executor`：`Kind() Kind` + `Start(ctx, Spec, Sink) error`；
  `Sink`：`Note / SignalBytes / Exit / Complete`
  （`Exit` 是对文档三方法形态的必要补充：`Complete(state, summary)` 装不下 `Record.ExitCode`，
  而「一律合成 124/137」会把 I-2 要求的区分抹掉）

### 🏗 02 — `jobs.Manager`：表 + 状态机 + 四动作

- 收尾**恰好一次**：正常退出 / 硬超时 / 被杀 / 执行体 panic 四条路都必须落到一次终态迁移；
  执行体未自行收敛时由 Manager 合成终态，不留「永远 running 的假行」
- 硬超时合成 `exit=124`、被杀合成 `exit=137`，并以注记写进输出文件（否则模型只看到「突然结束」）
- 输出有界：超上限丢弃字节但仍向写入方报「已消费」（基础设施限制不得伪装成命令失败）；
  增量走文件偏移游标，读取在 UTF-8 边界回退，不把多字节字符劈成两半
- 作用域两档：`Snapshot(Scope{Session})` 与 `Snapshot(Scope{Session, Subject})`、`Reclaim` 同理；
  取回 / 终止 / 销项一律**拒绝跨作用域**（照抄既有 `run.sessionID != sessionID` 的判据）
- `Events()` 是**变更信号口**：容量 1、latest-wins，不推进游标、不进上下文；**事件流的构建留在产品侧**（框架不 import `event`：能力只能在构造期定下实现，拿不到会话，append 不到尾部，只能回填）

### 🏗 03 — `jobs/builtin`：`jobs_manage`

- 产品中立的通用管理工具：`observe`（不带 handle = 列本作用域在册作业）、`fetch`（消费式增量）、
  `kill`、`done`（幂等；在跑作业拒收——终态只由执行体判定）
- 出参是中性 JSON 投影，产品据此渲染自己的工作表格口径；`ToolMeta` 可用 `WithMeta` 覆盖以对齐产品权限模型

### 🏗 04 — 门面化增补：登记-启动两段式 / 外部终态 / 只读增量 / 墓碑读面 / 输出归属

第一版只有 `Dispatch`（登记即起执行体）。当**产品自己拥有执行体**（产品起进程、自己收尾）时，
忠实的门面化需要下面五条；它们不改既有语义，只补"产品拥有执行体"这条路的缺口：

- `Declare(ctx, Spec) (Handle, error)` + `Start(ctx, Handle) error`：登记与启动拆开。
  `Dispatch` 会**立即**起执行体，产品便无法在"派发回执"里如实报告"执行体起不来"（那会变成一个
  永远 running 的假行）；`Declare` 只登记（且**不要求** `Description`），`Start` 幂等地起已登记作业的执行体
- `Complete(ctx, Handle, State, int, string) error`：**外部终态入口**（`Sink.Complete` 的孪生）。
  终态原本只能由执行体经 `Sink` 判定；产品自有的执行体在 `Sink` 之外，需要一个按句柄的终态入口。
  与 `Sink.Complete` 汇入唯一的 `finalize`，重复调用幂等；非终态入参归一为 `failed`；拒绝跨 `Scope`
- `Peek(ctx, Handle, FetchBudget) (string, Record, error)`：**只读增量**（`Fetch` 的孪生）。
  `Fetch` 是"读增量 + 推进游标 + 终态即销项"的一体动作；两段式工具面（读 → 提交）与探针需要
  **不推进、不销项**的只读读取。两者共享同一条读取实现，只差"是否提交/销项"
- `RetiredState(Handle) (State, bool)`：销项墓碑的**字面量读面**。销项后只剩 `ErrRetired` 读不到
  终态；重复 `done`/`fetch` 的幂等回执需要说明"它以什么状态结束"
- `Spec.OutputPath` + `Record.OutputRef` + 销项删文件：**输出归属**。缺省 Manager 自建
  `<outputDir>/<handle>.log`（现于销项/驱逐/Close 时删除，`Close` 亦回收自建目录）；`Spec.OutputPath`
  非空时改由产品拥有该文件——Manager 不建写句柄、只按偏移读，于是"登记期间目录可被 `RemoveAll`"
  （Windows 上 `os.OpenFile` 不带 `FILE_SHARE_DELETE`）成立，文件生命周期归产品。`Record.OutputRef`
  两种形态下都如实报出路径
- `Spec.Handle`（登记时回填）：执行体拿到**自己的句柄**。管理者在派发之后才产生句柄，执行体若要在
  执行侧维护一张按句柄索引的侧表（进程树 / 取消口 / 命令原文 / Index），`Spec.Handle` 是它唯一
  能看到句柄的地方（调用方自填的值会被覆盖）
- `Declare` 出来但既未 `Start` 也未 `Complete` 的作业，`Kill` 会**直接**合成 `killed` 终态
  （没有执行体可取消，不必等一个永远不会回报的 goroutine）

### ✅ 验证

- `go build ./...`、`go vet ./jobs/...` 干净；`go test ./jobs/... -count=1`、`-race` 全绿
- `jobs/manager_test.go`：往返取回并销项、增量消费、去重折叠、跨作用域拒绝、两档 Snapshot/Reclaim、
  硬上限 124、被杀 137、panic 收尾、`done` 幂等且拒在跑、在途上限、未知 kind / 空描述拒绝、输出封顶不改终态；
  增补面另有：`Declare` 不起执行体且允许空描述、`Start` 幂等、`Complete` 外部终态与幂等、`Complete` 拒跨作用域、
  未启动作业 `Kill` 直接终态、`Peek` 不推进游标不销项、`Spec.Handle` 标识自身、外部 `OutputPath` 归产品所有、
  Manager 自有输出于销项后删除
- `jobs/builtin/manage_test.go`：工具形态与元数据、observe → fetch → 销项全链、越权与未知 op / handle 拒绝、未知字段拒绝
- 契约细节见 `docs/arch/16-jobs-contracts.md`（§2.1 门面化增补）；包内说明见 `jobs/README.md`

---


## Unreleased — `session`：回合闸门 + 工作状态短临界区（删除环内历史把手 `InLoop`）

> **主题：把「整轮持有会话锁」从根上换掉——回合准入改用容量 1 的令牌通道，工作状态收敛成短临界区，环内环外走同一套历史方法**

背景：`Chat`/`ChatStream` 从进函数持锁到出函数，工具 `Dispatch` 与全部 `LoopHooks`、`ContextController` 回调都在同一 goroutine、同一把锁内。宿主在这条路径上调 `Session.History()/ReplaceHistory()/AppendHistory()/SetSystemPrompt()` = 同 goroutine 抢自己已持有的非重入锁 = 永久自锁（Seelex 的 `compact_context` 就踩在这上面，且它顺手攥着宿主的端口锁，把故障面从「这一轮没回复」放大到整进程）。上一版给引擎注入「环内把手」绕开它，那是绕症状；这一版换掉准入方式本身。

### 🏗️ 01 — `session/turn.go`：回合闸门与检查点队列

- 回合闸门：`Session.turn` 是容量 1 的令牌通道（`turn.go` 的 `acquireTurn`/`releaseTurn`，惰性初始化）。`Chat`/`ChatStream` 先领令牌（排队时感知 ctx，取消即返回，不僵在锁上），跑完整轮后归还：同一会话仍然串行，但**没有任何会话锁被整轮持有**。
- 工作状态短临界区：`ReActLoop.stateMu` 只护 `history` / `promptBlocks` / `cfg`；临界区里绝不调用模型、工具、回调、发布器或持久化。
- 写命令两种落点：空闲当场落地；回合在飞挂进 `pending` 队列，由循环在安全检查点落地——工具派发返回后（追加 tool 结果之前）与每次模型请求之前；回合收口（含提前 return）时把剩余命令一次落地。替换命令会清掉更早排队的命令（替换本来就覆盖整份历史）。
- `ErrInFlightToolCallDropped`：替换若丢掉在飞 `tool_call` 单元（结果尚未 append）则拒收且历史不动。提交时校验；安全检查点上这份状态与提交时刻一致，所以那一次校验就是全部校验。
- `ErrLoopUnsupported`：自定义 `Loop` 未实现 `workingHistory` 时替换明确报错，不回退成「追加两条」这种近似。

### 🏗️ 02 — 删除环内把手，历史方法归一

- 删除 `session/inloop.go`：`InLoopFrom` / `InLoop` / `enterInLoop` / `exitInLoop` / `ErrNotInLoop` / `ErrInLoopExpired` / `ErrInLoopInFlightDropped` 与 `inLoopSeq`、`inLoopActive` 两枚守卫原子量一并删除。
- `Session.History()` 永不阻塞（只取短临界区）；新增 `Session.ReplaceHistory([]types.Message) error`；`AppendHistory` / `SetSystemPrompt` 与替换同一条路由（`ReActLoop.ReplaceHistory` 由无返回值改为返回 `error`）。
- `Session.Chat` / `ChatStream` 换用回合闸门；`ExportTrace` 与写入 `lastTrace` 仍走 `Session.mu`（短临界区）。

### ✅ 验证

- `session/turn_test.go`：环内读历史立即返回；环内替换排队并在同回合的下一次请求生效（终态 4 条：帧 + 在飞 assistant + tool 结果 + 收尾 assistant）；丢掉在飞尾部被拒且历史不动；空闲替换立即生效；环内 `AppendHistory`/`SetSystemPrompt` 不自锁；闸门串行且 ctx 取消即返回。
- `go build ./...`、`go vet ./session/...` 干净；`go test ./session/ -count=1` 与 `-race` 全绿。

---
## v0.3.0 (2026-09-16) — 工具权限模型（Linux 式 rwx + sudo）

> **主题：`tools/permission` 从扁平 `allow/ask/deny` 升格为「主体 × 路由组 × 位 + sudo」；`tools` 元数据面与 `tools/gateway` 挂载点同步扩展，全部加法、零破坏**

目标：产品层（Seelex）不再维护任何旁路的「工具可见性硬编码名单」，只用框架即可表达「谁能用哪些工具、以什么位、是否需要 sudo 口令」。

### 🏗️ 01 — `tools`：可选元数据面

- `ToolEntry` 新增可选指针字段 `Meta *ToolMeta`；未声明时为零（`nil`），旧 provider 行为不变。
- 新增 `ToolMeta{Kind, Groups, Bits, Visibility, Resource, Signal}`、`ToolKind`（`read/write/control/admin`）、位常量 `BitRead=4/BitWrite=2/BitExecute=1`、控制信号 `term/stop/chld/pause`。
- 新增 `MetaMiddleware` + `WithMetaMiddleware`：中间件可按 `meta.Kind/Groups` 路由，无需产品名单。
- `Registry.Snapshot` 深拷贝 `Meta`；新增哨兵错误 `ErrToolNotVisible`、`ErrPermissionDenied`。

### 🏗️ 02 — `tools/permission`：Linux 式权限模型

- `Subject`（不透明标识，`""` = 匿名）、`SubjectRoot`。
- `PermissionGroup{Name, Match, Mode, Default, Resource}` 成为一等路由维度。
- `SubjectGrant` / `GrantBit{Bits, Resources}` / `SudoMode{none/password/nopasswd}`。
- `PermissionConfig` 新增 `Groups`、`Subjects`、`MissingBit`（零值 = `ask`）。
- `PermissionRule` 新增可选 `Bits *uint8`。
- `PermissionChecker.CheckFor(subject, tool, args)`（主体维度）与 `VisibleFor(subject, tool)`（断位 = 不在 PATH）；`Check` 保留为 `CheckFor("", ...)` 的薄封装。
- 求值顺序：route（组，LMRW）→ 判位（断位走 `MissingBit`）→ `Rules`（最后、最细，覆盖组默认）。
- `ApprovalResponse.Scope`（`once/tool/session/args`，空 = `once`）与 `ElevationEvent` / `ElevationAuditor` / `SetElevationAuditor` / `GrantElevation`：每次提权必发审计，`session` 仅同 subject+group 复用。

### 🏗️ 03 — `tools/gateway` / `tools/holder`：装配点

- `DefaultGateway.SetSubjectResolver` / `SetBitEnforcer`（R1 / R8 挂载点；框架内不含任何路径解析逻辑）。
- `checkPermission` 改为返回可 `errors.Is` 辨别的两类错误（断位 ⇒ `ErrToolNotVisible`，位齐被拒 ⇒ `ErrPermissionDenied`）。
- `VisibleTools` 叠加主体可见性：断位工具与非 root 的控制类工具不再返回。
- 控制类工具（`Meta.Kind == control`）默认仅 root 可路由；`ToolMeta(name)` 把 `Signal` 透出给上层 loop。
- `assessRisk` 优先读 `ToolMeta.Kind`，无 Meta 时回退旧 switch。
- `holder.Holder` 新增只读 `Entry` / `Entries` 访问器（供网关读取元数据）。

### 🏗️ 04 —— 中间件判定与「执行选择页面」

- 新增 `permission.Gate` / `Gate.Middleware` / `Gate.Decide`：判定集中为 `tools.MetaMiddleware`，装配完全自由；授权主体是 session 下的 engine（`Engine` / `WithEngine` / `EngineResolver`）。
- 新增英文拒绝错误 `DenialError` / `InvisibilityError`：统一前缀 `permission denied: cannot complete the invocation of tool "X"`，断位追加 `: the tool is not in this engine's namespace`；可 `errors.Is` 区分 `ErrPermissionDenied` / `ErrToolNotVisible`。
- 拒绝 ⇒ 执行选择页面：未设 `Gate.DenyWithoutPrompt` 时，任何拒绝（不可见 / 策略拒绝 / 控制类 / enforcer 的 deny）先呈现 `ApprovalHandler`；通过只获得**单次操作的提权**（不写 allow 缓存、不记提权），`full_access` 亦然。
- 提权由 harness 完成：新增 `Elevator` 接口与默认实现 `CheckerElevator`；`SetElevationSource` / `Elevator()` / `Elevated` 可整体接管提权读写。
- `Gate.Enforcer`：`BitEnforcer` 接入 `Gate.evaluate`，最先判定（`ok=false` 表示不干预、回退到位判定），与 `DefaultGateway` 同顺序、同语义。
- 审批请求信息齐备：`ApprovalContext.Context` 原样透传调用 ctx；`NewApprovalRequest` 填齐 `ID` / `SessionID`（`WithSessionID`）/ `Timeout`（`Gate.Timeout`，缺省 `DefaultApprovalTimeout`）/ `Risk`（`RiskOf`）/ `Preview`（`FormatPreview`）；`DefaultGateway` 与中间件共用该构造函数（纯填充，字段无增删）。
- `NewChannelApprovalHandler`：先挂回执通道再投递请求，等待时同时监听请求超时与 ctx 取消（两者都视作拒绝）。
- 新增离线示例 `example_Implement/11_permission_middleware`：拒绝→选择页面、`full_access` 仍询问、harness 持有提权台账三段演示。
- 验证：`tools/permission` 与 `tools/gateway` 新增 14 项测试（8 个测试函数）（enforcer 优先级 / ctx 透传 / 请求字段 / channel 适配器边界），`go test ./tools/... -count=1` 与 `-race` 全绿。

### ✅ 兼容性

- 无破坏性变更：`PermissionConfig{Mode, Rules}` 字面量、`Check`、`NewPermissionChecker`、`AddAllowRule`、`DefaultApproveOptions`、`ApprovalRequest` 字段、`WithCallTimeout/WithMiddleware/WithDispatchRetries` 均原样保留且行为不变；`Check` 旧测试一行未改仍全绿。
- 所有新类型零值安全（缺省 = 现有行为）。

### 📊 验证

- `go build ./...`、`go test ./tools/...`、`go vet ./tools/...` 全绿。
- 新增 table-driven 测试：判定矩阵 `{main,sub}×{ro,rw,ctl,adm}`、只写 Groups 表达默认动作、`errors.Is` 两类错误、`Scope=once|session` + 审计回调、`VisibleTools` 断位过滤、`MetaMiddleware` 路由、元数据快照隔离。

### ⚠️ 已知事项

- CHANGELOG 中原有一条更早的 `## v0.3.0 (2026-06-14) — 架构重构` 条目（与本次改动无关），编号与本次冲突，待维护者裁决是否重编号。
- 迁移指南见 [`docs/arch/15-tool-permission-model.md`](docs/arch/15-tool-permission-model.md) 第 9 节（Seelex v0.2.0 → v0.3.0）。

---

## v0.2.0 (2026-09-14) — 多模态附件与限流装配层

> **主题：`limits` 准入装配层 + `types` 附件载体（`FilePart` / `FileKind` / `FileID`）+ 非 2xx 可分类错误**

### 🏗️ 01 — `limits`：限流与并发的路数装配层

把「现在允不允许发、发了算多少额度」从 `agent/core/api` 里已标 Deprecated 的 `Account.MaxRPM` 抽出来，做成可装饰任意 `types.ChatCompleter` 的独立层。与 `accountpool` 的分工保持清晰：账号池回答「用哪个账号」，`limits` 回答「现在允不允许发、发了算多少额度」。参数、装配与边界语义见 [`limits/README.md`](limits/README.md)。

- `Params` / `RetryPolicy` / `DefaultParams()`：YAML/JSON tag 齐全的参数模型（RPM、TPM、加权在途、排队超时、重试）；`0 = 不限`，`Normalize()` 只补无法表达的派生值（桶容量），推荐值只在 `DefaultParams()` 里给。
- `Assembly` 与 `Wrap` / `WrapFor` / `WrapAll` / `WrapComplete`：按 key（账号、角色、节点）装配 gate；`WrapComplete` 接受只保证 `Complete` 的窄接口（如 seelex 的 `agent.Completer`），具体实现具备流式能力时仍走真流式。
- `Gate` / `Permit` / `Stats`：准入顺序为「加权在途槽位 → 请求速率桶 → 令牌速率桶」，三者共享同一 deadline；先占槽位再扣桶，速率等待失败退还槽位；`Release()` 幂等，`Settle(usage)` 用真实用量结算估算差额。
- 失败一律返回可 `errors.Is` 匹配的哨兵错误（`ErrConcurrencyExceeded`、`ErrRateLimited`，以及包裹两者的 `ErrQueueTimeout`），语义都是「稍后可以重试」。
- `SetParams` / `SetRate` / `SetConcurrency` / `SetBurst` / `SetKeyParams` / `SetEnabled` / `Snapshot`：参数全运行时可改，不打断在途请求；`SetParams` 是全局覆盖。
- `Cost` / `CostEstimator` / `DefaultEstimator`：图片按 512×512 tile 折算 token（尺寸未知走兜底值，绝不返回 0），`Cost.Images` 只计加权在途权重。

### 🏗️ 02 — `types`：消息级多模态附件

- `ImagePart` → `FilePart`，新增 `FileKind`（`image` / `document`）与 `FilePart.FileID`；`Message.Images` → `Message.Files`、`WithImages` → `WithFiles`。
- 纯文本消息的 JSON 形状逐字节不变（`"content":"…"`），只有携带附件才展开为 content parts 数组：OpenAI 形态走 `image_url` 或 `file.file_data`，Anthropic 策略按 kind 走 `image` 或 `document`。
- Files API 引用按端点实测走**扁平** `{"type":"file","file_id":"file-api-..."}`（与内联载荷互斥）；回读时保留 `file_id`——旧行为直接丢弃，重发历史会静默少一张附件。
- 反解新增 OpenAI `file`、Responses `input_file`（含顶层 `file_data` / `file_url`）与 Anthropic `document`；`Validate()` 在请求前拦下「种类与 MIME 打架」「既无字节又无地址」「内联与 file_id 同时存在」。

### ✨ 03 — 非 2xx 成为可分类错误

- 新增 `types.HTTPStatusError`（含 `Retry-After` 解析；错误文本仍是历史形状 `HTTP %d: …`，日志与既有测试不受影响）。
- `agent/core/api` 的同步与流式两处不再把非 2xx 交给 `ParseResponse`（那会把 429 当成协议解析失败），重试层因此能拿到状态码与 `Retry-After`。
- 新增可单测的重试分类：`StatusOf` / `RetryAfterOf` / `IsRetryable` / `RetryDelay`，默认尊重 provider 的 `Retry-After`。

### 💥 破坏性变更

| 旧 | 新 |
| --- | --- |
| `types.ImagePart` | `types.FilePart`（新增 `Kind`、`FileID`） |
| `types.Message.Images` | `types.Message.Files` |
| `Message.WithImages` | `Message.WithFiles` |

### ⚠️ 已知缺口

- 文档 / PDF 附件只有单测覆盖：可用账号均为 openai 兼容端点，尚无 Anthropic / Gemini / 官方 OpenAI 账号做真机验证。
- 各家的数值型限额（PDF 页数与字节、图片像素上限）与 MIME 白名单尚未编码。
- 流式按估算计费（Seele 现有 SSE 处理不暴露 usage）；需要精确计费的产品应改用 `Gate.Acquire` 自行准入与 `Permit.Settle`。
- Anthropic 侧引用 `file_id` 需 `anthropic-beta: files-api-2025-04-14` 头，该策略暂未发送，故该分支跳过而不是发空 source。
- `limits` 目前是可选层，尚未接入产品装配点。

### 📊 变更统计

34 文件，+5860 / −13；新增 `limits` 包（18 个文件）。验证：`go build ./...`、`go vet ./...` 通过，`go test ./limits/... ./types/... ./agent/core/api/... -count=1`（含 `-race`）全绿。

---

## v0.3.0 (2026-06-14) — 架构重构

> **主题：策略模式工具层 + WorkPlan 图引擎 + SchemaOf**

### 🏗️ 01 — 工具层策略模式重构

ToolProvider 接口从 4 方法精简为 1 方法，引入 Handler 策略接口：

```
旧: ToolProvider{Tools, Dispatch, HasTool, ProviderName}
新: ToolProvider{Tools} → ToolEntry{Definition + ToolHandler}
```

- 新增 `ToolHandler` 策略接口（1 方法 `Execute`）
- 三种实现：`HubToolHandler`（gRPC）、`MCPToolHandler`（stdio/SSE）、`InlineToolHandler`（Go 函数）
- 新增 `InlineProvider` — 纯 Go 函数工具管理（零网络开销）
- `tool_holder` 改用 `map[string]ToolEntry` O(1) 分发，替代 O(n) 遍历
- `_` 前缀工具隔离下沉到 `tool_holder.Tools()` 统一处理

新增文件：`hub_handler.go`、`mcp_handler.go`、`inline_handler.go`、`inline_provider.go`

### 🏗️ 02 — WorkPlan 图引擎抽象

线性链表 → Graph + Edge + NodeRunner 图引擎：

- 新增 `Graph`、`Edge`（一等公民，含 Condition/Priority/Label）、`NodeRunner` 接口、`ExecutionContext`
- 6 种 Runner：`autoRunner`、`loopRunner`、`forkRunner`、`approveRunner`、`checkpointRunner`、`emitRunner`、`controlRunner`
- `resolve()` 统一路由：无条件边优先 → 条件边按 Priority 匹配
- Sugar 层完整适配：所有方法内部委托 `graph.AddNode`/`AddEdge`
- 外部 API 零变化

新增文件：`graph.go`、`runner.go`

### ✨ SchemaOf — struct → JSON Schema 自动生成

- `SchemaOf(v)` 用反射从 Go struct 自动生成 `map[string]interface{}` JSON Schema
- 支持 5 个标签：`json`（属性名/omitempty）、`desc`（description）、`enum`（枚举约束）、`default`（默认值）
- 类型自动映射 + 嵌套递归 + 指针解引用

新增文件：`schema.go`、`schema_test.go`（10/10 PASS）

### 🗑️ 示例重写

删除旧示例，新增 4 个结构化示例 + 共享配置：

```
01_hello_seele → 02_inline_tools → 03_workplan → 04_mcp
```

### 🐛 Bug 修复

- `NodeResult.Output` 从未赋值 → `FinalOutput()` 永远空（race test 发现）
- `Shutdown()` 未停止 hub gRPC（资源泄漏）
- `New()` 失败时 hub goroutine 泄漏 → 启动顺序重排
- 35 个文件 `sukasukasuka123` → `RedHuang-0622` 用户名迁移

### 📊 变更统计

53 文件，+4095 / -1586 行

---

## v0.1.0 (2026-06-06) — 首个里程碑版本

> **主题：架构定型 + 资源池升级**

经过 35 次迭代，Seele 框架的核心架构已稳定。本版本完成最后一批并发安全修复，并将底层资源池依赖升级至 `RedHuang-0622/TemplatePoolByGO v0.1.8`。

### 🏗️ 核心架构

| 层 | 包 | 职责 |
|----|-----|------|
| 编排 | `core/agent/` | Agent 生命周期、LLM+工具组装 |
| 会话 | `core/session/` | ReAct 循环、上下文管理、审批流转 |
| 工具 | `core/tool_holder/` | 多 Provider 聚合、瞬时重试 |
| Provider | `provider/` | HubProvider (gRPC)、MCPProvider (stdio/sse) |
| LLM | `llm/` | OpenAI 兼容 HTTP 客户端（stdlib） |
| 工作流 | `workplan/` | 声明式 DAG 引擎（9 种原语） |
| 部署 | `sdk/cluster/` | 多 Agent gRPC 服务化 |

### ✨ 核心能力

- **ReAct Agent**：Chat / ChatStream，支持多轮对话 + tool_call 并发
- **工具生态**：microHub (gRPC) + MCP (stdio/sse) 双 Provider，运行时热插拔
- **WorkPlan 工作流**：Auto / Approve / If / Switch / Loop / Fork / Checkpoint / Emit，声明式 DSL
- **人工审批**：Q-K-V 两段式协议，CLI / 网络 / 自动三种 Gate 实现
- **上下文管理**：LLM 压缩 + 硬截断 + Token 估算，防止上下文溢出
- **REPL**：交互式终端，支持审批 UI、Prompt 热加载 (fsnotify)
- **流式输出**：SSE 分帧解析，tool_call 思考文本实时推送
- **多层并发**：tool_call 并发 (max 5)、Fork Agent 并发 (max 3)

### 🔧 本次修复 (f8972a4)

| 问题 | 修复 |
|------|------|
| `MCP()` 无锁并发 → nil dereference | 新增 `mcpMu` + `shutdown` channel |
| health probe goroutine 不可停止 | 新增 `healthCancel`，Shutdown 时 cancel |
| `buildToolCalls` 零值 ToolCall 注入 history | 改用 append + index check |
| `parseApprovalQuestionID` 字符串解析 JSON | 改用 `json.Unmarshal` |
| `ChatStream` tool_call 时 onChunk 丢弃 | 现在推送思考文本到 onChunk |

### 📦 依赖升级

```
github.com/RedHuang-0622/microHub     v0.1.4 → github.com/RedHuang-0622/microHub     v0.1.5
github.com/RedHuang-0622/TemplatePoolByGO v0.1.7 → github.com/RedHuang-0622/TemplatePoolByGO v0.1.8
```

**TemplatePoolByGO v0.1.8 关键变更：**

| 变更 | 影响 |
|------|------|
| `ReconnectOnGet` 默认 `true` → `false` | 热路径不再默认 Ping+重连，需显式开启 |
| `MonitorInterval` 实际生效 | 新增定期扩容/缩容 goroutine |
| `bufferSize < 1` 防死锁 | `IdleBufferFactor=0` 时强制设为 1 |
| `Get()` 竞态修复 | Enqueue→Remove 窗口内资源不再丢失 |
| `expand` 重试参数化 | `MaxRetries`/`RetryInterval` 配置生效 |
| `shrink` 两阶段驱逐 | 优先关闭超龄连接 (SurviveTime) |
| `validateAndReturn` 实现 Ping+重连 | `ReconnectOnGet=true` 时真正生效 |

### ⚠️ 已知问题

3 个 🔴 严重 Bug（详见 [review.md](review.md)）：
- MCP `Attach()` 失败时 stdio 子进程泄漏
- `HubProvider.HasTool()` 对 `_` 前缀工具返回 false（影响 REPL 审批恢复）
- `Chat()` `*msg.Content` nil panic

### 📁 文件统计

| 组件 | 文件 | 代码行 |
|------|------|--------|
| `core/agent/` | 3 | ~350 |
| `core/session/` | 4 | ~530 |
| `core/tool_holder/` | 3 | ~140 |
| `provider/` | 4 | ~750 |
| `llm/` | 1 | ~370 |
| `history/` | 2 | ~360 |
| `workplan/` | 6 | ~2000 |
| `sdk/` | 5 | ~755 |
| `types/` `config/` | 3 | ~175 |
| **总计** | **~31** | **~5400** |

### 🔗 相关文档

- [架构文档](ARCHITECTURE.md)
- [代码审查](review.md)
- [使用指南](README.md)

---

> 首个 release 之后的版本记录将以此格式更新。
