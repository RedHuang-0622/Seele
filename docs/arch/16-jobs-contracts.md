# jobs 契约（异步作业根能力）

> 状态：**已实现**（`jobs`、`jobs/builtin`）。本文是跨模块稳定契约：凡把"一次长任务"表达为
> **可派发 / 可观察 / 可终止**的作业，都走这套契约，而不是各自再长一套。

## 1. 边界（D1：管理侧通用、派发侧产品化）

```text
┌────────────────── Seele（无产品语义）──────────────────┐
│  jobs：契约 + Manager（作业表 / 状态机 / 四动作）+ jobs_manage │
│  tools · tools/permission · session · workplan · event（既有）│
└──────────────────────────────────────────────────────────┘
     ▲ 注册 Executor + 调用 Manager     边界门禁：jobs 不 import 任何产品包
┌────────────────── 产品层（如 seelex）────────────────────┐
│  派发侧工具：bash_bg / read_batch / fork_subagents          │
│  Executor 实现：process / inline / worker / seat            │
└──────────────────────────────────────────────────────────┘
```

判据：**管理动作对任何作业语义一致**（换产品也成立）⇒ 进 Seele；
**派发与具体命令/提示词语义绑定** ⇒ 留产品。因此 `jobs` 只提供
「契约 + Manager + `jobs_manage`」，**不提供任何派发工具与任何 Executor**。

## 2. 契约

- `Kind`：开放字符串。框架只解释 `KindProcess` / `KindInline`，其余（如 `worker` / `seat`）由产品注册。
- `State`：`running` / `done` / `failed` / `killed`；终态只由执行体判定。
- `Handle`：`a<seq>`，seq 单调，**永不落盘**。
- `Scope{Session, Subject}`：两个**并列字段**，不拼字符串（避免自造分隔符的转义与碰撞面）。
- `Spec{Kind, Scope, Node, Batch, Description, Payload, Index, Dedup, OutputPath, Handle}`
  （`Handle` 由 Manager 登记时回填，执行体经它认领自己的作业）。
- `Record`：只读投影（状态、退出码、归属、`OutputRef`（输出文件路径）、字节/行/游标、截断、终态摘要 ≤512B）。
- `Manager`：`Dispatch / Declare / Start / Observe / Peek / Fetch / Complete / Kill / Done / Snapshot / Reclaim / Events / ScopeOf / RetiredState / Close`。
- `Executor`：`Kind() Kind` + `Start(ctx, Spec, Sink) error`。
- `Sink`：`Note` / `SignalBytes` / `Exit` / `Complete`。

> `Exit` 是对设计稿三方法（`Note`/`SignalBytes`/`Complete`）的**必要补充**：
> `Complete(state, summary)` 装不下 `Record.ExitCode`，而"一律合成 124/137"会把
> I-2 要求的区分抹掉。
> `Spec.Handle` 同理：管理者在派发之后才产生句柄，执行体若要在执行侧维护一张按句柄索引的
> 侧表（进程树 / 取消口 / 命令原文 / Index），`Spec.Handle` 是它唯一能看到句柄的地方
> （登记时回填；调用方自填的值会被覆盖）。

### 2.1 门面化增补（登记-启动两段式 / 外部终态 / 只读增量 / 墓碑读面 / 输出归属）

第一版只有 `Dispatch`（登记即起执行体）。当**产品自己拥有执行体**（`process` / `inline`
这类"产品起进程、自己收尾"的作业）时，忠实的门面化需要下面五条；它们不改既有语义，只补
"产品拥有执行体"这条路的缺口：

| 增补 | 形状 | 解决的缺口 |
|---|---|---|
| 登记-启动两段式 | `Declare(ctx, Spec) (Handle, error)` + `Start(ctx, Handle) error` | `Dispatch` 会**立即**起执行体；产品要在"派发回执"里如实报告"执行体起不来"，必须先拿到句柄再自己起。`Declare` 只登记（且不要求 `Description`），`Start` 幂等地起已登记作业的执行体 |
| 外部终态入口 | `Complete(ctx, Handle, State, int, string) error` | 终态原本只能由执行体经 `Sink` 判定；产品自有的执行体在 `Sink` 之外，需要一个按句柄的终态入口（幂等；非终态入参归一为 `failed`；拒绝跨 `Scope`） |
| 只读增量 | `Peek(ctx, Handle, FetchBudget) (string, Record, error)` | `Fetch` 是"读增量 + 推进游标 + 终态即销项"的一体动作；两段式工具面（读 → 提交）与探针需要**不推进、不销项**的只读孪生 |
| 墓碑读面 | `RetiredState(Handle) (State, bool)` | 销项后只剩 `ErrRetired`，读不到终态字面量；重复 `done`/`fetch` 的幂等回执需要"它以什么状态结束" |
| 输出归属 | `Spec.OutputPath`（非空 = 产品自有文件，Manager 不建写句柄、只按偏移读；空 = Manager 自建 `<outputDir>/<handle>.log`，并在销项时删除） | Manager 原先**持有**输出文件写句柄，使"登记期间目录可被 `RemoveAll`"（Windows 上 `os.OpenFile` 不带 `FILE_SHARE_DELETE`）无法成立；且 Manager 从不删每个作业的输出文件 |

`Complete` 与 `Sink.Complete` 是一条终态迁移的两个入口（都汇入唯一的 `finalize`），重复调用幂等；
`Peek` 与 `Fetch` 共享同一条读取实现（只差"是否提交/销项"）。`Declare` 出来的作业若既未
`Start` 也未 `Complete`，`Kill` 会**直接**合成 `killed` 终态（没有执行体可取消，不必等一个
永远不会回报的 goroutine）。

## 3. 作用域与鉴权

| 字段 | 作用 | 参加鉴权/隔离 | 归属打点 |
|---|---|---|---|
| `Scope.Session` | 隔离 + 回收粒度（会话销毁即杀） | 是 | — |
| `Scope.Subject` | 主体分组（`emp_<role>`）+ 权限主体 | 是（组内可见） | — |
| `Spec.Node` | 编排节点 / 团队阶段 | 否 | 是 |
| `Spec.Batch` | 派发它的那次 chat 请求 | 否 | 是 |

两档语义（空字段 = 通配）：

| 调用 | 语义 |
|---|---|
| `Snapshot(Scope{Session})` | 该会话全部在册作业 |
| `Snapshot(Scope{Session, Subject})` | 该会话内该主体的作业 |
| `Reclaim(ctx, Scope{Session})` | 会话级回收 |
| `Reclaim(ctx, Scope{Session, Subject})` | 只回收该主体名下作业 |

调用方身份由 `WithScopeResolver`（或 `WithSessionResolver` + `WithSubjectResolver`）
从 ctx 解析；`Fetch` / `Kill` / `Done` 一律拒绝跨作用域访问（I-7）。
`Observe` 无 ctx，故**只读且不做鉴权**——需要读面隔离的产品自行设卡。

## 4. 不变式

| # | 内容 |
|---|---|
| I-1 | 收尾恰好一次：正常退出 / 硬超时 / 被杀 / 执行体 panic 四条路都必须落到一次终态迁移 |
| I-2 | 硬超时合成 `exit=124`、被杀合成 `exit=137`，并注记写进输出文件 |
| I-3 | 句柄 `a<seq>`，seq 单调（禁用 `len(table)` 推——驱逐后会重号）；去重键仅 running 期间生效 |
| I-4 | 记录/句柄只在内存，永不落盘 |
| I-5 | 回收 = 作用域销毁；`Close` 取消全部在途作业 |
| I-6 | 输出有界：超上限丢弃字节，但仍向写入方报"已消费"（不把基础设施限制伪装成命令失败） |
| I-7 | 取回/终止一律拒绝跨 `Scope` |

## 5. 事件与信号

- **事件流的构建留在产品侧**：`jobs` 不 import `event`、不建 `event.Recorder`、不发
  `event.Sink`，只给 `Events()`（变更信号）与 `Snapshot/Observe`（读面）。
  理由：`event.Sink` 的实现必须在**构造期**定下，而 `jobs.Manager` 的构造期拿不到
  「这条作业属于哪个会话的哪条事件流」——`Recorder` 是进程级单例、序号全局，事件库却是
  **按会话**追加的。这样发出来的事件 **append 不到会话事件流的尾部**，只能由产品事后
  **回填**会话归属，且全局序号在按会话排序下不成立。产品因此订阅 `Events()` 信号，用
  `Snapshot/Observe` 取到 `Record.Scope.Session` 之后**自行构建并追加**事件。
- `Events()` 是**变更信号口**：容量 1、latest-wins；派发 / 终态 / 新字节三类触发；
  **不推进游标、不进上下文**。
- **无 push 唤醒**：框架绝不把结果投递进忙会话。

## 6. 集成须知（产品侧）

1. 每个 `Kind` 注册一个 `Executor`；`Start` 必须尊重 ctx 取消，否则 `Kill` / 硬上限只能靠超时收场。
   （`Dispatch` = 登记 + 起执行体；若要"先拿句柄、再自己起执行体并如实报告起不来"，改用
   `Declare` + 自己起 + `Complete`。）
2. 派发侧工具在产品的工具面按产品权限模型声明 `ToolMeta`；`jobs_manage` 的元数据可用
   `builtin.WithMeta` 覆盖以对齐产品的路由组与位。
3. 输出缺省由 Manager 落在 `WithOutputDir`（缺省为私有临时目录）下，按 `a<seq>.log` 命名，
   销项时删除；增量读取走偏移游标，永不重放。`Spec.OutputPath` 非空时输出文件改由产品拥有
   （Manager 不建写句柄、只按偏移读，文件生命周期归产品）。
4. 产品级约束（如团队人数上限）必须**先于**框架的 `InFlight` 兜底生效，否则用户撞到的是兜底值。