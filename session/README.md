# session

`session` 是 Agent 之上的会话 ChatLoop：它把调用方提供的抽象 `Agent`、history、上下文组件和 telemetry 组装成可复用的 Chat/ChatStream 会话。它不创建 provider、账号池或产品工具，也不依赖具体 `agent` 包。

## 公共接口

| API | 用途 |
| --- | --- |
| `NewSession` | 从 `SessionComponents` 创建显式会话 |
| `Session.Chat` / `ChatStream` | 执行一次同步或流式对话（领回合令牌；同一会话串行，ctx 取消即返回） |
| `Session.History` | 读工作历史拷贝。**永不阻塞**：只取工作状态短临界区 |
| `Session.ReplaceHistory` | 就地替换工作历史：空闲当场生效，回合在飞排队到下一个检查点 |
| `Session.AppendHistory` / `SetSystemPrompt` | 落点规则同上（追加消息、替换 system prompt） |
| `Session.HistoryIfAvailable` | 观测面非阻塞读取（会话忙时返回最近发布的检查点快照） |
| `WithHistoryPublisher` | 循环历史检查点发布器（宿主可自行接收检查点快照） |
| `Session.Reset` | 显式清空 working history 与已注入的 durable history，开始新会话 |
| `NewReActLoop` | 创建底层 ReAct 执行循环 |
| `SessionComponents` / `ContextComponents` | 注入 history、请求装配、结果处理和 telemetry |

## 实现边界

- 会话只持有调用方注入的 `Agent` 和可选 history；`agent.Agent` 只是这一接口的一种实现，不隐式创建全局 provider。
- `ClearHistory` 仅清空当前 working view 以兼容底层 Loop；需要同时清除持久快照时使用可返回错误的 `Reset(ctx)`。
- ReActLoop 默认不主动压缩上下文；压缩必须通过显式 Compressor 或 ContextController 装配。

## 并发约束

一个 `Session` 同时只跑一轮 `Chat` / `ChatStream`：串行靠**回合闸门**（容量 1 的令牌通道），不靠「把会话锁征用整轮」。排队领令牌的调用方感知 ctx，取消即返回。多个 Session 可以并发执行；若它们共享同一个 `DurableHistory`，冲突解决与持久化一致性由该 history 实现负责。流式回调不得重入同一个 Session。

### 回合闸门 + 工作状态短临界区

旧模型（`Chat`/`ChatStream` 从进函数持锁到出函数）有两个后果：一次长文流式期间任何 `History()` 都要排在它后面；环内（工具 handler、`LoopHooks`、`ContextController`）再调历史方法就是同 goroutine 抢自己已持有的非重入锁 = 永久自锁（表现：会话永远「运行中」，取消 ctx 也退不出来）。

现在换成三条机制：

| 机制 | 内容 |
| --- | --- |
| 回合闸门 | `Chat`/`ChatStream` 领一枚容量 1 的令牌，跑完整轮再归还；整轮不持有任何会话锁 |
| 工作状态短临界区 | `history` / `promptBlocks` / `cfg` 由循环内部一把短锁保护，临界区里绝不调用模型、工具、回调、发布器或持久化 |
| 写历史的两种落点 | 空闲 → 当场落地；回合在飞 → 挂进检查点队列，由循环在下一个安全检查点落地（工具派发返回后、追加 tool 结果之前；以及每次模型请求之前） |

因此**环内与环外走的是同一套方法、同一份数据**：

| 路径 | 用哪个 | 语义 |
| --- | --- | --- |
| 执行面（与 `ChatStream` 同 goroutine：工具 handler、流式回调、`LoopHooks`） | `Session.History()` / `ReplaceHistory()` / `AppendHistory()` / `SetSystemPrompt()` | 读立即返回；写在本轮的下一个检查点落地（同回合的下一次请求就能读到） |
| 观测面（其它 goroutine：UI/详情/投影/落账） | `HistoryIfAvailable()` | 立刻返回：空闲 → 权威历史；运行中 → 最近一次发布的检查点快照 |

宿主不再需要判断「我此刻在不在环内」：上一版的 `InLoopFrom(ctx)` 环内把手连同它的守卫（回合序号 + 进行中标志）已删除，ctx 也不再需要透传进折叠路径。

下界：替换若会丢掉「assistant 已带 `tool_calls`、其结果尚未 append」的在飞单元 → `ErrInFlightToolCallDropped`，且历史不动。否则紧随其后 append 的 tool 消息成孤儿，provider 直接拒请求。安全检查点上这份状态与提交时刻一致，所以提交时的那次校验就是全部校验。自定义 `Loop` 未实现 `workingHistory` 时，替换报 `ErrLoopUnsupported`。

检查点发布点是循环里的三处：模型调用前（`ContextBeforeModel`）、assistant 落历史后（`ContextAfterAssistant`）、工具结果落历史后（`ContextAfterTool`）；排队命令落地时也会重新发布一次。因此运行中读到的快照最多滞后一个检查点（约一次模型调用或一批工具），而不是整轮结束。

`HistoryIfAvailable()` 返回 `(nil,false)` 只在「从未发布过检查点且会话锁被别处短暂持有」时出现，**不表示没有历史**，不要拿它覆盖已有缓存。

## 协作与验证

- 上游装配入口：[agent](../agent/README.md)
- 上下文原语：[seelectx](../seelectx/README.md)
- 验证：`go test ./session/... -count=1`（含 `-race`）