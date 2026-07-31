# Seele 根能力与运行时边界

## 架构原则

> Seele 提供无产品语义的执行能力；Seelex 决定何时调用、调用什么、上下文放什么、费用如何归集。

Seele 根目录提供五个平行能力：`agent`、`workplan`、`accountpool`、`seelectx`、`tools`。`telemetry` 是可选的通用观测基础设施，不属于任何产品语义模块。

`agent` 是 LLM client 与 composition root：它负责把 client、账号解析、工具运行时和上下文能力装配起来，但不拥有工具 provider、Task、WorkPlan 产品 DSL 或上下文压缩策略。`tools` 与 `agent` 平行，工具实现迁移在根 `tools/` 下。

## 模块职责

| 模块 | Seele 提供 | 不提供 |
| --- | --- | --- |
| `agent` | LLM client、Chat/ChatStream 装配、可选依赖注入、运行时 facade | 产品工具、Task、长期 history owner、压缩触发策略 |
| `workplan` | Plan/Node/Edge、拓扑校验、并发调度、取消、分支、checkpoint、邻接表/矩阵/edge-list codec | Task 生命周期、产品 DSL、AgentFactory 调度特判 |
| `accountpool` | `sync.Map` 账号状态、channel semaphore、P2C 选择、lease、启停和统计 | 费用账本、远端中转、provider 产品策略 |
| `tools` | Function Calling 合约、Registry、权限/超时/middleware、MCP/microHub/Skills adapter | Agent 会话、文件/Git/工作区产品工具的默认注册 |
| `seelectx` | history owner 合约、request assembly、切片、规整、相关性、Placeholder、QuickChat、显式压缩/ContextController | 隐式 session history、固定压缩阈值、产品账本 |
| `telemetry` | Hook、Trace/Span、intent/effect 关联、Metric/Audit sink、查询/流、OTel adapter | 业务阻断策略、前端界面、产品数据解释 |

Task 与 TaskList 是 Seelex 上层产物。Plan 的子代理如果需要 Task，通过 Seelex 提供的 `TaskExecutor` adapter 实现 Node；Plan 核心不依赖 Task。

## 依赖方向

```text
                         Seelex
             +-----------+-----------+
             | Task / Context policy |
             | Plan DSL / work tools |
             +-----+---------+-------+
                   |         |
             +-----v---------v----------------+
             |          agent (装配根)        |
             | LLM client + Chat/ChatStream   |
             +--+-------------+----------+----+
                |             |          |
       +--------v--+  +-------v-----+  +-v---------+
       | accountpool|  |    tools    |  | seelectx  |
       | leases/P2C |  | FC/providers|  | Context   |
       +------------+  +-------------+  +----------+

       +------------------------------------------+
       | workplan Plan/Node/Edge + runtime        |
       | independent of agent/tools/task/context  |
       +------------------------------------------+

       +------------------------------------------+
       | telemetry (optional observer/sink layer) |
       +------------------------------------------+
```

禁止出现：`workplan -> agent`、`workplan -> seelectx`、`workplan -> task`、`tools -> agent`、`seelectx -> agent`、`accountpool -> agent`。循环依赖必须通过调用方接口或 `types` 中的最小 DTO 解耦。

## 主要数据流

### Chat/ReAct

1. Seelex 或调用方创建 `agent.Components`，注入 LLM client、`tools` runtime、`seelectx` assembler/history/controller 和可选 telemetry hook。
2. Engine/ReActLoop 读取 working history，调用 `RequestAssembler` 组合 system/plan/skill/task block。
3. LLM 请求和工具请求分别经 telemetry Hook 记录 Before intent 与 After effect；工具返回先经过可选 `ToolResultProcessor`。
4. 显式装配的 `ContextController` 可以在 `before_model`、`after_assistant`、`after_tool` 事件上决定替换 history 或调用 QuickChat 压缩；没有 controller 时 Loop 不做压缩。
5. Durable history、checkpoint、账本和产品上下文快照由 Seelex 持有。

### WorkPlan

```text
NodeFactory/NodeDecoder
        -> Plan.Build
        -> Validate
        -> Seal
        -> Runner
        -> Scheduler -> Executor -> Node.Run
        -> NodeResult / checkpoint
```

`workplan/codec` 支持：

- `nodes + edges[{from,to}]` 的 formal edge-list；
- `nodes + adjacency{from:[to...]}` 的邻接表；
- `nodes + order + matrix` 的 0/1 邻接矩阵。

codec 只处理拓扑和节点不透明载荷。`input`、`kind`、TaskID 或子代理角色由 Seelex 的 NodeDecoder 解释。

### AccountPool/LLM client

```text
ChatClient.Complete
  -> AcquireRequest (provider/pinned account/metadata)
  -> P2C 选择两个低负载候选中的较低者
  -> Lease 持有 semaphore
  -> HTTP completion
  -> Release
```

流式请求在同一个 lease 上完成整个读取生命周期，EOF、读取错误和 Close 都幂等释放；不会在 strategy 选择和 HTTP 发送之间二次选账号。

### Tools

调用方注册普通 Function Provider、MCP、microHub 或 Skills adapter 到 `tools.Registry`，再由 middleware 叠加可见性、权限、超时、重试和结果处理。Agent 只看到 `VisibleTools/Dispatch` 最小接口。

## 当前迁移状态

- 新入口 `agent.NewWithComponents` 是无副作用装配路径。
- `agent.New(Options)` 仍作为旧客户端构造路径保留，用于仓库旧示例和配置迁移；它不是新模块边界的推荐入口。
- 旧 `agent/core/tool`、`agent/gateway/tool`、产品 file/Git/Shell/editor/plan builtin 已删除。
- 旧 `workplan/dsl` 与 `runtime/serialize` 产品 DSL 已删除；调用方改用 `workplan/codec`。

