# session

`session` 是 Agent 之上的会话 ChatLoop：它把调用方提供的抽象 `Agent`、history、上下文组件和 telemetry 组装成可复用的 Chat/ChatStream 会话。它不创建 provider、账号池或产品工具，也不依赖具体 `agent` 包。

## 公共接口

| API | 用途 |
| --- | --- |
| `NewSession` | 从 `SessionComponents` 创建显式会话 |
| `Session.Chat` / `ChatStream` | 执行一次同步或流式对话 |
| `Session.HistoryIfAvailable` | 观测面非阻塞读取历史（会话忙时返回最近发布的检查点快照） |
| `WithHistoryPublisher` | 循环历史检查点发布器（宿主可自行接收检查点快照） |
| `Session.Reset` | 显式清空 working history 与已注入的 durable history，开始新会话 |
| `NewReActLoop` | 创建底层 ReAct 执行循环 |
| `SessionComponents` / `ContextComponents` | 注入 history、请求装配、结果处理和 telemetry |

## 实现边界

- 会话只持有调用方注入的 `Agent` 和可选 history；`agent.Agent` 只是这一接口的一种实现，不隐式创建全局 provider。
- `ClearHistory` 仅清空当前 working view 以兼容底层 Loop；需要同时清除持久快照时使用可返回错误的 `Reset(ctx)`。
- ReActLoop 默认不主动压缩上下文；压缩必须通过显式 Compressor 或 ContextController 装配。

## 并发约束

一个 `Session` 会串行执行 `Chat` 与 `ChatStream`，以保护同一份 working history。多个 Session 可以并发执行；若它们共享同一个 `DurableHistory`，冲突解决与持久化一致性由该 history 实现负责。流式回调不得重入同一个 Session。

### 执行面与观测面

`Chat` / `ChatStream` 从进函数持到出函数持有同一把会话锁，`History()` 用的是
同一把锁。这意味着**一次长文流式（可达数十秒）期间，任何 `History()` 调用都会
排在它后面**。宿主若在观测路径（UI 详情、表格投影、落账读写）用 `History()`，
表现就是"界面卡住不动"，而不是报错。

因此读取分两类，调用方必须区分：

| 路径 | 用哪个 | 语义 |
| --- | --- | --- |
| 执行面（与 `ChatStream` 同 goroutine，如流式回调内） | `History()` | 阻塞等待，拿到的一定是最新历史 |
| 观测面（其它 goroutine：UI/详情/投影/落账） | `HistoryIfAvailable()` | 立刻返回：空闲 → 权威历史；运行中 → 最近一次发布的检查点快照 |

检查点发布点是循环里的三处：模型调用前（`ContextBeforeModel`）、assistant 落
历史后（`ContextAfterAssistant`）、工具结果落历史后（`ContextAfterTool`）。
因此运行中读到的快照最多滞后一个检查点（约一次模型调用或一批工具），而不是
整轮结束。

**为什么不能用 `TryLock` 了事**：`ChatStream` 的锁覆盖整个 ReAct 循环（多轮
模型调用 + 工具分发），所以运行期间 `TryLock` 永远失败——只靠它填不出可用
数据，观测面会一直空着。发布检查点才是"既不等锁、又有数据"的做法。

`HistoryIfAvailable()` 返回 `(nil,false)` 只在"从未发布过检查点且锁被别处
短暂持有"时出现，**不表示没有历史**，不要拿它覆盖已有缓存。

## 协作与验证

- 上游装配入口：[agent](../agent/README.md)
- 上下文原语：[seelectx](../seelectx/README.md)
- 验证：`go test ./session/... -count=1`
