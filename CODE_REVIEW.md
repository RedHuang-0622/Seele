# Seele 框架代码审查 —— 上下文控制 & WorkPlan 深度解析

> ⚠️ **本文档已过时**（2026-05-20）。下述内容基于 v0.1 旧架构编写：
> - 工具层仍为旧 4 方法 `ToolProvider`（Dispatch/HasTool），实际已重构为 1 方法 + Handler 策略
> - 编排层引用已删除的 `core/runtime.go`，实际已拆分为 `core/agent/` + `core/tool_holder/`
> - WorkPlan 仍为旧线性原语模型，实际底层已重构为 Graph + Edge + NodeRunner 图引擎
>
> **请以 [ARCHITECTURE.md](ARCHITECTURE.md) 和 [review.md](review.md) 为准。** 本文仅保留作为上下文控制机制的参考。
>
> 审查日期：2026-05-20 · 聚焦：上下文控制机制、WorkPlan 工作形式、各原语回调方法

---

## 目录

1. [整体架构类图](#1-整体架构类图)
2. [上下文控制详解](#2-上下文控制详解)
3. [WorkPlan 工作形式详解](#3-workplan-工作形式详解)
4. [各原语支持的回调方法汇总](#4-各原语支持的回调方法汇总)
5. [核心数据流图](#5-核心数据流图)
6. [接口与结构体依赖关系矩阵](#6-接口与结构体依赖关系矩阵)

---

## 1. 整体架构类图

### 1.1 全量类型关系图（Class Diagram）

```
┌─────────────────────────────────────────────────────────────────────────────────────┐
│                              SDK 组装层 (sdk/api)                                     │
│                                                                                       │
│  ┌──────────────────────┐           ┌──────────────────────────┐                     │
│  │       Engine          │           │       AgentPool           │                     │
│  │  (seele_api.go)       │           │  (seele_api.go)           │                     │
│  ├──────────────────────┤           ├──────────────────────────┤                     │
│  │ rt      *Runtime─────┼───┐       │ engine  *Engine           │                     │
│  │ hub     *BaseHub      │   │       │ agents  []*namedAgent     │                     │
│  │ hubProv *HubProvider  │   │       │ current int               │                     │
│  │ mcpProv *MCPProvider  │   │       ├──────────────────────────┤                     │
│  │ opts     Options      │   │       │ Add() / Switch()         │                     │
│  │ shutdown chan struct{}│   │       │ Chat() / ChatStream()    │                     │
│  ├──────────────────────┤   │       │ Current() / All()         │                     │
│  │ NewAgent() → *Agent   │   │       └──────────────────────────┘                     │
│  │ QuickChat()           │   │                                                         │
│  │ AttachMCPServer()     │   │                                                         │
│  │ Shutdown()            │   │                                                         │
│  └──────────┬────────────┘   │                                                         │
│             │ 持有并管理       │                                                         │
└─────────────┼────────────────┼────────────────────────────────────────────────────────┘
              │                │
              ▼                │
┌─────────────────────────────┼─────────────────────────────────────────────────────────┐
│                          core 编排层                                                    │
│              │                                                                         │
│              ▼                                                                         │
│  ┌──────────────────────────┐         1:N   ┌──────────────────────────┐              │
│  │        Runtime            │──────────────▶│     ToolProvider (接口)    │              │
│  │   (core/runtime.go)       │               │  (provider/tool_provider) │              │
│  ├──────────────────────────┤               ├──────────────────────────┤              │
│  │ llm       *ChatClient────┼───┐           │ ProviderName() string    │              │
│  │ providers []ToolProvider  │   │           │ Tools() []Tool           │              │
│  │ mu        sync.RWMutex    │   │           │ HasTool(name) bool       │              │
│  ├──────────────────────────┤   │           │ Dispatch(ctx,n,args)      │              │
│  │ Register(p) / Unregister()│   │           └──────────┬───────────────┘              │
│  │ NewAgent() → *Agent ─────┼─┐ │                ┌─────┴─────┐                        │
│  │ tools() (私有)            │ │ │                ▼           ▼                        │
│  │ dispatch() (私有)         │ │ │     ┌──────────────┐ ┌──────────────┐              │
│  └──────────────────────────┘ │ │     │ HubProvider  │ │ MCPProvider  │              │
│                               │ │     │(Hub_provider)│ │(mcp_provider)│              │
│                               │ │     └──────────────┘ └──────────────┘              │
│                               │ │                                                    │
│                               │ │  1:1 (唯一 LLM 客户端)                               │
│                               │ ▼                                                    │
│                               │ ┌──────────────────────────┐                          │
│                               │ │      ChatClient           │                          │
│                               │ │   (llm/chat_client.go)    │                          │
│                               │ ├──────────────────────────┤                          │
│                               │ │ Cfg    LLMConfig         │                          │
│                               │ │ Client *http.Client       │                          │
│                               │ ├──────────────────────────┤                          │
│                               │ │ Complete()    同步        │                          │
│                               │ │ CompleteStream() SSE 流   │                          │
│                               │ └──────────────────────────┘                          │
│                               │                                                        │
│                               │ 1:N (每 NewAgent 创建一个)                             │
│                               ▼                                                        │
│  ┌──────────────────────────────────────────────────────────────┐                     │
│  │                         Agent                                 │                     │
│  │                    (core/agent.go)                            │                     │
│  ├──────────────────────────────────────────────────────────────┤                     │
│  │ runtime          *Runtime          ← 回指 Runtime             │                     │
│  │ sessionID        string            ← 唯一会话 ID              │                     │
│  │ history          []types.Message   ← 对话历史                 │                     │
│  │ maxLoops         int (默认 8)       ← tool_call 循环上限       │                     │
│  │ contextCfg       ContextConfig     ← 上下文管理配置            │                     │
│  │ toolFilter       []string          ← 工具白名单               │                     │
│  │ lastCompressLoop int (-1=未压缩)    ← 压缩去重标记             │                     │
│  ├──────────────────────────────────────────────────────────────┤                     │
│  │ Chat(ctx, input) → (string, error)          ← 同步 ReAct 循环 │                     │
│  │ ChatStream(ctx, input, onChunk) → (string) ← 流式 ReAct 循环  │                     │
│  │ SetContextConfig(cfg)                       ← 按会话调整阈值   │                     │
│  │ SetMaxLoops(n) / SetToolFilter(names)       ← 运行时配置       │                     │
│  │ ClearHistory() / History()                  ← 历史管理         │                     │
│  │ dispatchToolCalls(ctx, tcs) (私有)           ← 并发工具调度     │                     │
│  └──────────┬───────────────────────────────────────────────────┘                     │
│             │ 持有 (值类型)                                                             │
│             ▼                                                                          │
│  ┌──────────────────────────────────────────────────────────────┐                     │
│  │                     ContextConfig                             │                     │
│  │               (history/context_limit.go)                      │                     │
│  ├──────────────────────────────────────────────────────────────┤                     │
│  │ MaxTokens          int  (默认 8192)   ← 历史硬上限            │                     │
│  │ CompressThreshold  int  (默认 6144)   ← 压缩触发阈值 (75%)     │                     │
│  │ MaxToolResultChars int  (默认 4000)   ← 单条工具结果截断长度   │                     │
│  ├──────────────────────────────────────────────────────────────┤                     │
│  │ Effective() → ContextConfig   ← 零值字段用默认值填充          │                     │
│  └──────────────────────────────────────────────────────────────┘                     │
│                                                                                        │
│  ┌──────────────────────────────────────────────────────────────┐                     │
│  │         history 包级函数 (无状态，纯函数)                       │                     │
│  ├──────────────────────────────────────────────────────────────┤                     │
│  │ EstimateTokens(text) → int                                    │                     │
│  │ EstimateMessageTokens(msg) → int                              │                     │
│  │ EstimateHistoryTokens(msgs) → int                             │                     │
│  │ NeedCompression(msgs, threshold) → bool                       │                     │
│  │ CompressHistory(ctx, client, history, maxTokens) → []Message  │                     │
│  │ TrimHistory(msgs, maxTokens) → []Message                      │                     │
│  │ TruncateToolResult(content, maxChars) → string                │                     │
│  └──────────────────────────────────────────────────────────────┘                     │
└──────────────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────────────────┐
│                           WorkPlan 工作流编排层 (workplan/)                             │
│                                                                                        │
│  ┌─────────────────────────┐       ┌──────────────────────────┐                       │
│  │      Agent (接口)        │       │    AgentFactory (接口)    │                       │
│  │   (workplan/plan.go)     │       │   (workplan/plan.go)     │                       │
│  ├─────────────────────────┤       ├──────────────────────────┤                       │
│  │ Chat(ctx, input) →string│       │ NewAgent(sysPrompt)→Agent│                       │
│  └─────────────────────────┘       └──────────────────────────┘                       │
│              ▲                                  ▲                                       │
│              │ 隐式满足                           │ 显式实现                              │
│              │                                  │                                       │
│  ┌───────────┴──────────────┐    ┌──────────────┴──────────────┐                       │
│  │   *core.Agent             │    │   EngineFactory (cluster)   │                       │
│  │   (Chat 方法签名一致)       │    │   rtAgentFactory (测试)     │                       │
│  └──────────────────────────┘    └─────────────────────────────┘                       │
│                                                                                        │
│  ┌─────────────────────────┐       ┌──────────────────────────┐                       │
│  │   ApprovalGate (接口)    │       │     ApprovalRequest       │                       │
│  │   (workplan/gate.go)     │       │     (workplan/gate.go)    │                       │
│  ├─────────────────────────┤       ├──────────────────────────┤                       │
│  │ Request(ctx, req)→string│       │ NodeID  string            │                       │
│  └──────────┬──────────────┘       │ Plan    string            │                       │
│       ┌─────┴─────┐                │ Options []string          │                       │
│       ▼           ▼                └──────────────────────────┘                       │
│  ┌──────────┐ ┌──────────────┐                                                         │
│  │CLIApproval│ │AutoApprove   │                                                         │
│  │Gate       │ │Gate (测试用)  │                                                         │
│  │(阻塞stdin)│ │(自动选首项)   │                                                         │
│  └──────────┘ └──────────────┘                                                         │
│                                                                                        │
│  ┌────────────────────────────────────────────────────────────────────┐               │
│  │                         WorkPlan                                   │               │
│  │                     (workplan/plan.go)                             │               │
│  ├────────────────────────────────────────────────────────────────────┤               │
│  │ nodes         []*node             ← 所有节点 (按构建顺序)           │               │
│  │ nodeIndex     map[string]*node    ← ID → node 快速查找             │               │
│  │ entryID       string              ← 入口节点 ID                    │               │
│  │ defaultPrompt string              ← 默认系统提示词                  │               │
│  │ factory       AgentFactory        ← 创建 Agent 的工厂 (外部注入)    │               │
│  │ gate          ApprovalGate        ← 人工确认 IO (外部注入)          │               │
│  │ vars          map[string]string   ← Emit 写入的命名变量            │               │
│  │ mu            sync.RWMutex        ← 保护 vars (Fork 并发写入)       │               │
│  ├────────────────────────────────────────────────────────────────────┤               │
│  │ 【执行引擎】Run(ctx) → *WorkPlanResult                             │               │
│  │                                                                     │               │
│  │ 【私有原语层 —— 所有执行逻辑】                                       │               │
│  │ primitiveRunNode()    ← 节点分发器                                  │               │
│  │ primitiveAuto()       ← 单 Agent 完整 ReAct 循环                    │               │
│  │ primitiveApprove()    ← 两阶段确认 (计划→人审→执行)                  │               │
│  │ primitiveLoop()       ← 带 Signal 的迭代循环                        │               │
│  │ primitiveFork()       ← 多 Agent 并发 + 结果汇合                     │               │
│  │ primitiveEmit()       ← 写命名变量                                  │               │
│  │ primitiveNext()       ← 下一节点路由 (If/Switch/Loop 专用逻辑)       │               │
│  │ primitiveNewAgent()   ← 创建节点专属 Agent (注入 prompt+toolFilter)  │               │
│  │ primitiveRenderInput()← 渲染 {{.PrevResult}} / {{.Vars.key}} 模板   │               │
│  │ primitiveAddNode()    ← 内部注册节点 (维护链表 next)                 │               │
│  │                                                                     │               │
│  │ 【公有语法糖层 —— 只构造 node，零执行逻辑】                            │               │
│  │ Auto() / Approve() / Gate() / Checkpoint() / Emit()                │               │
│  │ If() / Switch() / Loop() → *Signal                                 │               │
│  │ Fork() / Pipeline() / Retry() → *Signal                            │               │
│  └──────────────────────────────┬─────────────────────────────────────┘               │
│                                 │ 持有 []*node                                        │
│                                 ▼                                                     │
│  ┌────────────────────────────────────────────────────────────────────┐               │
│  │                    node (私有结构体)                                 │               │
│  │                 (workplan/node.go)                                  │               │
│  ├────────────────────────────────────────────────────────────────────┤               │
│  │ id, kind                                             ← 标识 + 类型  │               │
│  │ systemPrompt, input, toolFilter, next                ← 执行配置     │               │
│  │ ── kindApprove ──                                                   │               │
│  │   approveOptions []string                            ← 人机选项     │               │
│  │ ── kindIf ──                                                        │               │
│  │   ifCond func(string)bool, ifTrueID, ifFalseID       ← 二选一分支   │               │
│  │ ── kindSwitch ──                                                    │               │
│  │   switchCases []SwitchCase                           ← 多路分支     │               │
│  │ ── kindLoop ──                                                      │               │
│  │   loopBodyID, loopUntil func(string)bool,            ← 循环配置     │               │
│  │   loopMaxIter, loopSignal *Signal, loopExhaustedID                  │               │
│  │ ── kindFork ──                                                      │               │
│  │   forkBranches []ForkBranch, joinID                  ← 并发配置     │               │
│  │ ── kindEmit ──                                                      │               │
│  │   emitKey string                                     ← 变量名       │               │
│  │ ── 运行时状态 ──                                                     │               │
│  │   checkpoint *checkpointState                        ← 快照         │               │
│  │   joinResults []string                               ← Fork 汇合    │               │
│  └────────────────────────────────────────────────────────────────────┘               │
│                                                                                        │
│  ┌──────────────────────┐  ┌──────────────────────┐  ┌──────────────────────┐         │
│  │       Signal          │  │      SwitchCase       │  │      ForkBranch       │         │
│  │  (workplan/node.go)   │  │  (workplan/node.go)   │  │  (workplan/node.go)   │         │
│  ├──────────────────────┤  ├──────────────────────┤  ├──────────────────────┤         │
│  │ value  string (JSON)  │  │ Match func(string)bool│  │ Label        string   │         │
│  │ iter   int            │  │ NextID string         │  │ SystemPrompt string   │         │
│  │ cbs    []func(string) │  └──────────────────────┘  │ Input        string   │         │
│  │ done   chan struct{}  │                            │ EntryNodeID  string   │         │
│  │ closeOnce sync.Once   │                            └──────────────────────┘         │
│  ├──────────────────────┤                                                                │
│  │ Get() → string        │  ← 无阻塞读当前值                                             │
│  │ GetString() → string  │  ← 去 JSON 引号的纯文本                                       │
│  │ Iter() → int          │  ← 当前迭代次数                                               │
│  │ OnUpdate(cb)          │  ← 注册回调，每次迭代触发                                       │
│  │ Wait() → string       │  ← 阻塞直到 loop 结束                                         │
│  │ set(raw, iter) (私有)  │  ← 引擎调用，触发所有 OnUpdate                                 │
│  │ close() (私有)         │  ← 引擎调用，解除 Wait 阻塞                                    │
│  └──────────────────────┘                                                                │
│                                                                                        │
│  ┌──────────────────────┐  ┌──────────────────────────────────────────┐               │
│  │    NodeResult         │  │          WorkPlanResult                  │               │
│  │  (workplan/node.go)   │  │       (workplan/node.go)                 │               │
│  ├──────────────────────┤  ├──────────────────────────────────────────┤               │
│  │ NodeID, Kind, Output  │  │ NodeResults  []*NodeResult               │               │
│  │ Skipped, Aborted      │  │ Vars         map[string]string           │               │
│  │ StartedAt, EndedAt    │  │ Checkpoints  map[string]string           │               │
│  │ Err        error      │  │ Aborted      bool                        │               │
│  └──────────────────────┘  │ AbortReason  string                       │               │
│                            │ TotalElapsed time.Duration                │               │
│                            ├──────────────────────────────────────────┤               │
│                            │ FinalOutput() → string    ← 最后成功节点输出│               │
│                            │ FinalOutputString() → string ← 纯文本解包  │               │
│                            └──────────────────────────────────────────┘               │
└──────────────────────────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────────────────────────┐
│                              类型层 (types/model.go)                                   │
│                                                                                        │
│  Message ──────── role + content(*string) + reasoningContent + tool_calls +            │
│                    tool_call_id + name                                                  │
│    └─ ToolCall ── id + type + function                                                 │
│         └─ ToolCallFunction ── name + arguments(JSON string)                           │
│  Tool ─────────── type + function                                                      │
│    └─ ToolFunction ── name + description + parameters(map)                             │
│  SkillInfo ────── Name + Description + Method + Addr                                   │
│  LLMConfig ───── BaseURL + APIKey + Model + MaxTokens + Timeout + Temperature          │
│  HubConfig ───── Addr + StartupDelayMs                                                 │
│  RegistryConfig ─ Path                                                                 │
│  AppConfig ───── LLM + Hub + Registry                                                  │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

### 1.2 关键接口继承/实现关系

```
                    ┌──────────────┐
                    │ ToolProvider  │  (provider/tool_provider.go)
                    │    interface   │
                    └──────┬───────┘
              ┌────────────┴────────────┐
              ▼                         ▼
     ┌────────────────┐      ┌────────────────┐
     │  HubProvider   │      │  MCPProvider   │
     │  (microHub)    │      │  (MCP 协议)    │
     └────────────────┘      └────────────────┘

                    ┌──────────────┐
                    │    Agent     │  (workplan/plan.go interface)
                    │  interface   │
                    └──────┬───────┘
                           │ 隐式实现 (Chat 签名一致)
                           ▼
                  ┌────────────────┐
                  │  *core.Agent   │  (core/agent.go)
                  └────────────────┘

                    ┌──────────────┐
                    │ AgentFactory │  (workplan/plan.go interface)
                    │  interface   │
                    └──────┬───────┘
              ┌────────────┴────────────┐
              ▼                         ▼
     ┌────────────────┐      ┌────────────────────┐
     │ EngineFactory   │      │  rtAgentFactory    │
     │ (sdk/cluster)   │      │  (测试用)           │
     │ 适配 Engine     │      │ 适配 Runtime       │
     └────────────────┘      └────────────────────┘

                    ┌──────────────┐
                    │ ApprovalGate │  (workplan/gate.go interface)
                    │  interface   │
                    └──────┬───────┘
              ┌────────────┴────────────┐
              ▼                         ▼
     ┌────────────────┐      ┌────────────────┐
     │ CLIApprovalGate │      │ AutoApproveGate│
     │ (stdin 阻塞)    │      │ (自动确认)      │
     └────────────────┘      └────────────────┘
```

---

## 2. 上下文控制详解

### 2.1 三层防御体系

Seele 的上下文控制采用 **逐级降级** 的三层防御体系：

```
Token 使用量增长
  │
  ├─ 第1层: Tool 结果截断 ──────────────────────────────────────────
  │   时机: 每次 dispatch 追加 tool 消息时
  │   机制: TruncateToolResult(content, MaxToolResultChars=4000)
  │   策略: 优先在换行处截断，保留完整行
  │   位置: core/agent.go:307 dispatchToolCalls()
  │
  ├─ 第2层: LLM 智能压缩 ──────────────────────────────────────────
  │   触发: NeedCompression(msgs, CompressThreshold=6144) == true
  │   去重: 相邻轮次不重复压缩 (lastCompressLoop 标记)
  │   机制: CompressHistory() → LLM 生成摘要 → 替换旧消息
  │   保留: system 消息全部保留 + 最近 4 条消息保留
  │   位置: core/agent.go:134-144 Chat() 每轮循环前
  │
  └─ 第3层: 硬截断保底 ────────────────────────────────────────────
      触发: 压缩后仍超 MaxTokens=8192，或压缩 LLM 调用失败
      机制: TrimHistory() → 丢弃最旧的非 system 消息
      保底: 若 system 消息自身超限 → 截断最长单条消息内容
      位置: core/agent.go:138 TrimHistory fallback
```

### 2.2 ContextConfig 配置结构

```go
// history/context_limit.go
type ContextConfig struct {
    MaxTokens          int  // 硬上限，历史总 token 数不可超过此值 (默认 8192)
    CompressThreshold  int  // 压缩触发阈值，超过后触发 LLM 压缩 (默认 6144, 75%)
    MaxToolResultChars int  // 单条工具调用结果最大字符数 (默认 4000)
}
```

**设计要点:**
- 零值字段通过 `Effective()` 方法自动填充默认值
- 可按 Agent 粒度独立配置 (`agent.SetContextConfig(cfg)`)
- `CompressThreshold < MaxTokens` 形成"提前预警"机制，给压缩留出操作空间

### 2.3 Token 估算机制

```
EstimateTokens(text)
  │
  └─ 公式: (len(text) + 2) / 3
     原理: 英文平均 1 token ≈ 4 字符 → /3 较保守
     注意: 中文 UTF-8 每字 3 字节 ≈ 1.5-2 token，此公式可能低估

EstimateMessageTokens(msg) = 10 (JSON 骨架) +
    EstimateTokens(content) +
    EstimateTokens(reasoningContent) +
    Σ EstimateTokens(tc.Function.Name + tc.Function.Arguments) +
    EstimateTokens(toolCallID) +
    EstimateTokens(name)

EstimateHistoryTokens(msgs) = Σ EstimateMessageTokens(msg)
```

### 2.4 压缩流程 (CompressHistory)

```
CompressHistory(ctx, client, history, maxTokens)
  │
  ├─ Step 1: splitSystem(history)
  │    → 分离 system 消息 (永久保留) 与 非 system 消息
  │
  ├─ Step 2: 拆分非 system 消息
  │    compressible = msgs[0 : len-keepRecent]  ← 旧消息，将被压缩
  │    keep         = msgs[len-keepRecent : ]   ← 最近 4 条，保留原文
  │
  ├─ Step 3: buildCompressInput(compressible)
  │    将每条消息序列化为可读文本:
  │      user      → "User: content"
  │      assistant  → "Assistant: content" 或 "Called tool(name, args)"
  │      tool       → "Result from tool(name): content" (超 800 字截断)
  │
  ├─ Step 4: callCompressLLM(ctx, client, input)
  │    调用 LLM (临时覆盖 MaxTokens=300, Temperature=0.3, Tools=nil):
  │      system: "Summarize the execution history briefly..."
  │      user:   buildCompressInput 的输出
  │    确保不触发 tool_call (Tools=nil)
  │
  ├─ Step 5: 组装新 history
  │    [system...] + [{"role":"user", "content": "[Context summary: ...]"}] + [...keep]
  │
  └─ Fallback: 组装后若仍超 MaxTokens → TrimHistory() 硬截断
```

### 2.5 硬截断流程 (TrimHistory)

```
TrimHistory(history, maxTokens)
  │
  ├─ 始终保留所有 system 消息
  │
  ├─ stripLeadingOrphanTools(msgs)
  │    移除头部的孤立 tool 消息 (其对应的 assistant tool_calls 已被丢弃)
  │
  ├─ 从最旧的非 system 消息开始丢弃，直到总 token ≤ maxTokens
  │
  └─ 若 system 消息自身就超过 maxTokens:
       截断最长单条消息的 content (保底中的保底)
```

### 2.6 Agent.Chat 中上下文控制的完整调用链

```
Agent.Chat(ctx, input)
  │
  │  追加 user 消息 → history
  │
  ├─ for loop := 0; loop < maxLoops; loop++:
  │    │
  │    ├── [上下文控制切入点] ──────────────────────────
  │    │   if lastCompressLoop < 0 || loop - lastCompressLoop > 1:
  │    │     // ↑ 跳过相邻轮次的冗余检查，压缩后 history 已大幅缩减
  │    │     if NeedCompression(history, CompressThreshold):
  │    │       CompressHistory() → 成功: 替换 history
  │    │                       → 失败: TrimHistory() 硬截断保底
  │    │       lastCompressLoop = loop
  │    │
  │    ├── llm.Complete(ctx, history, tools)
  │    │
  │    ├── 追加 assistant 消息 → history
  │    │
  │    ├── 若无 tool_calls → 返回 content
  │    │
  │    └── dispatchToolCalls():
  │         │
  │         ├ 并发 dispatch (信号量 max=5)
  │         ├ 瞬时错误重试 (最多 3 次, 间隔 2s)
  │         │
  │         └ [上下文控制] TruncateToolResult(result, MaxToolResultChars)
  │              → 追加 tool 消息 → history
  │
  └─ 达到 maxLoops → 返回错误 (防止无限循环)
```

---

## 3. WorkPlan 工作形式详解

### 3.1 架构设计原则：糖与执行分离

```
┌─────────────────────────────────────────────────────────┐
│                    sugar.go (公有层)                      │
│                                                          │
│  Auto() / Approve() / Gate() / Checkpoint() / Emit()    │
│  If() / Switch() / Loop() / Fork() / Pipeline() / Retry()│
│                                                          │
│  职责: 只构造 node 结构体 + 调用 primitiveAddNode()       │
│        零执行逻辑，支持链式调用 (全部返回 *WorkPlan)       │
│                                                          │
├─────────────────────────────────────────────────────────┤
│                    plan.go (私有层)                       │
│                                                          │
│  primitiveRunNode()    ← 按 NodeKind 分发               │
│  primitiveAuto()       ← 完整 ReAct 循环                │
│  primitiveApprove()    ← 两阶段确认 (计划→人审→执行)     │
│  primitiveLoop()       ← Signal 响应式循环              │
│  primitiveFork()       ← 并发 + 汇合                    │
│  primitiveEmit()       ← 写命名变量                     │
│  primitiveNext()       ← DAG 路由 (If/Switch/Loop)      │
│  primitiveNewAgent()   ← 工厂创建 + 配置注入            │
│  primitiveRenderInput()← 模板渲染                       │
│  primitiveAddNode()    ← 链表 next 自动推导 + 注册      │
│                                                          │
│  职责: 全部执行逻辑，外部不可见                            │
└─────────────────────────────────────────────────────────┘
```

### 3.2 NodeKind 九种原语一览

| 原语 | NodeKind | 是否执行 Agent | 作用 | 输出 |
|------|----------|:---:|------|------|
| **Auto** | `kindAuto` | ✓ | 单 Agent 完整 ReAct 循环，自主决策执行 | Agent 响应 JSON |
| **Approve** | `kindApprove` | ✓✓ | 两阶段：Agent 生成计划 → 人确认 → Agent 执行 | 执行结果 JSON |
| **If** | `kindIf` | ✗ | 二选一条件路由 (纯路由，透传 prevJSON) | 透传输入 |
| **Switch** | `kindSwitch` | ✗ | 多路条件路由 (纯路由，透传 prevJSON) | 透传输入 |
| **Loop** | `kindLoop` | ✓ | 带 Signal 的迭代循环，每次迭代执行 body 节点 | 最终迭代结果 |
| **Fork** | `kindFork` | ✓✓✓ | 多 Agent 并发执行 + 结果汇合为 JSON Object | `{"label":result,...}` |
| **Join** | `kindJoin` | ✗ | Fork 的汇合占位节点 (在 primitiveFork 中处理) | 透传输入 |
| **Checkpoint** | `kindCheckpoint` | ✗ | 快照当前输出到 result.Checkpoints | 透传输入 |
| **Emit** | `kindEmit` | ✗ | 将当前输出写入命名变量 `wp.vars[key]` | 透传输入 |

**特殊说明：**
- **Approve** 执行两次 Agent Chat：第一次无工具生成计划，第二次真正执行（Agent 是新建的独立实例）
- **Fork** 每个分支创建独立 Agent（通过 `factory.NewAgent(prompt)`），各自拥有独立 history
- **Loop** 的循环体是另一个已注册的 Auto 节点，通过 `primitiveAuto(bodyNode)` 递归执行

### 3.3 WorkPlan.Run() 执行引擎主循环

```
Run(ctx)
  │
  ├─ [全局信号量] globalWorkPlanSem ← 限制同时运行的 WorkPlan 数
  │
  ├─ 初始化 vars = make(map[string]string)
  │
  ├─ prevJSON = `""`            ← 初始输出 (空 JSON string)
  │  currentID = entryID        ← 从入口节点开始
  │
  └─ while currentID != "":
       │
       ├─ check ctx.Done()      ← 取消检查
       │
       ├─ node = nodeIndex[currentID]
       │
       ├─ nr = primitiveRunNode(ctx, node, prevJSON, result)
       │    │
       │    ├─ input = primitiveRenderInput(node.input, prevJSON)
       │    │    → 替换 {{.PrevResult}} 和 {{.Vars.key}}
       │    │
       │    └─ switch node.kind:
       │         ├ kindAuto       → primitiveAuto()
       │         ├ kindApprove    → primitiveApprove()
       │         ├ kindIf/Switch  → 透传 prevJSON (纯路由)
       │         ├ kindLoop       → primitiveLoop()
       │         ├ kindFork       → primitiveFork()
       │         ├ kindJoin       → 透传 (Fork 已处理汇合)
       │         ├ kindCheckpoint → 快照 prevJSON
       │         └ kindEmit       → primitiveEmit(key, prevJSON)
       │
       ├─ 若 nr.Aborted → 终止整个 WorkPlan
       │
       ├─ 若 !nr.Skipped && nr.Output != "" → prevJSON = nr.Output
       │
       └─ currentID = primitiveNext(node, prevJSON)
            │
            ├ kindIf     → ifCond(prev) ? ifTrueID : ifFalseID
            ├ kindSwitch → 顺序匹配 cases, 走 Default 或无匹配结束
            ├ kindLoop   → loopSignal 耗尽? exhaustedID : next
            └ default    → node.next
```

### 3.4 各原语执行细节

#### 3.4.1 primitiveAuto —— 最核心原语

```
primitiveAuto(ctx, node, input) → JSON string
  │
  ├─ agent = primitiveNewAgent(node)
  │    → factory.NewAgent(prompt 或 defaultPrompt)
  │    → 若有 toolFilter → agent.SetToolFilter(names)
  │
  └─ out = agent.Chat(ctx, input)
       → 内部完成完整的 ReAct 循环 (可能多轮 tool_call)
       → 返回纯文本，包装为合法 JSON (toJSON)
```

#### 3.4.2 primitiveApprove —— 两阶段人工确认

```
primitiveApprove(ctx, node, input) → (output, skipped, aborted, error)
  │
  ├─ Phase 1: 生成计划
  │    planAgent = primitiveNewAgent(node)  ← 新建 Agent
  │    planOut = planAgent.Chat(ctx,
  │      "请分析以下任务，列出执行步骤和将调用的工具，【不要实际执行】，只输出计划...")
  │
  ├─ Phase 2: 等人确认
  │    choice = gate.Request(ctx, ApprovalRequest{
  │        NodeID:  node.id,
  │        Plan:    planOut,         ← Agent 生成的计划文本
  │        Options: node.approveOptions, ← ["执行","跳过","终止"]
  │    })
  │    │
  │    ├─ "跳过"/"skip" → return (skipped=true)
  │    ├─ "终止"/"abort"/"" → return (aborted=true)
  │    └─ 其他 → 继续执行
  │
  └─ Phase 3: 真正执行
       execAgent = primitiveNewAgent(node) ← 新建独立 Agent (不共享 planAgent 的 history)
       out = execAgent.Chat(ctx, input)    ← 真正调用工具
       return (toJSON(out), skipped=false, aborted=false)
```

**注意：** planAgent 和 execAgent 是两个独立 Agent 实例，历史不共享。计划阶段的 tool_call 结果（实际上没有，因为 plan prompt 要求不执行）不会影响执行阶段。

#### 3.4.3 primitiveLoop —— Signal 响应式循环

```
primitiveLoop(ctx, node, initJSON) → JSON string
  │
  ├─ sig = node.loopSignal (或新建)
  │
  ├─ current = initJSON  ← 初始值来自上一节点输出
  │
  └─ for iter := 0; ; iter++:
       │
       ├─ check ctx.Done()
       │
       ├─ bodyNode = nodeIndex[node.loopBodyID]
       │   input = primitiveRenderInput(bodyNode.input, current)
       │   out = primitiveAuto(ctx, bodyNode, input)  ← 执行循环体
       │
       ├─ sig.set(out, iter+1)  ← 更新 Signal (触发所有 OnUpdate 回调)
       │   current = out
       │
       ├─ if loopUntil(fromJSON(out)) → break  ← 满足退出条件
       │
       ├─ if loopMaxIter > 0 && iter+1 >= loopMaxIter → break  ← 达到最大迭代
       │
       └─ 回到循环头
            │
       sig.close()  ← defer 解除所有 Wait() 阻塞
       return sig.Get()
```

**关键行为:**
- 循环体内发生的 tool_call 轮次不影响 loop 的迭代计数
- 每次迭代的输入 = 上次迭代的输出 (通过 `{{.PrevResult}}` 传递)
- 迭代间没有内置退避 (区别于 Agent 内的 dispatch 重试)，外部可通过 OnUpdate 自行控制

#### 3.4.4 primitiveFork —— 多 Agent 并发

```
primitiveFork(ctx, node, prevJSON) → JSON object string
  │
  ├─ 信号量控制并发: maxConcurrentFork = 3
  │
  └─ for each ForkBranch (并发 goroutine):
       │
       ├─ input = primitiveRenderInput(branch.Input, prevJSON)
       ├─ agent = factory.NewAgent(branch.SystemPrompt 或 defaultPrompt)
       │    → 若有 toolFilter → agent.SetToolFilter(names)
       ├─ out = agent.Chat(ctx, input)  ← 完整 ReAct 循环
       └─ result = {label: branch.Label, out: toJSON(out)}
            │
       wg.Wait()  ← 等所有分支完成
       │
       汇合: json.Marshal({"label1": result1, "label2": result2, ...})
         → 作为 JSON Object 传给下一节点
```

**与 Auto 内并发 tool_call 的区别：**
| 维度 | Auto 内 tool_call 并发 | Fork 并发 |
|------|----------------------|-----------|
| 粒度 | 工具调用级 | Agent 级 |
| Agent 数量 | 1 个 | N 个 (每个分支独立) |
| 历史 | 共享 | 各自独立 |
| 推理 | 同一轮 LLM 输出多个 tool_call | 各自独立推理 |
| 汇合 | 追加为 tool 消息 | JSON Object |

### 3.5 模板渲染系统

```
primitiveRenderInput(tmpl, prevJSON) → string
  │
  ├─ {{.PrevResult}} → fromJSON(prevJSON)
  │    → 若 prevJSON 是 JSON string → 去引号，返回纯文本
  │    → 若 prevJSON 是 object/array → 返回原始 JSON
  │
  └─ {{.Vars.key}} → fromJSON(vars[key])
       → 同上规则，从 Emit 写入的命名变量取值
```

### 3.6 节点 next 自动推导

```
primitiveAddNode(node) → *WorkPlan
  │
  ├─ 若是第一个节点 → entryID = node.id
  │
  ├─ 上一个节点 prev 的 next 为空
  │   && prev 不是 If/Switch (条件节点有自己的分支逻辑)
  │   → prev.next = node.id   ← 自动串联
  │
  └─ 注册: nodes.append(node), nodeIndex[node.id] = node
```

**If/Switch 节点不参与自动串联**，因为它们的下一跳由条件结果决定。

---

## 4. 各原语支持的回调方法汇总

### 4.1 回调全景矩阵

| 回调类型 | 注册时机 | 触发时机 | 回调签名 | 用途 |
|----------|----------|----------|----------|------|
| **Signal.OnUpdate** | 构建期 (Run 前) | Loop 每次迭代后 | `func(jsonValue string)` | 监控循环进度、流式推送中间结果 |
| **Signal.Wait** | 构建期或并行 goroutine | Loop 结束后 | `() → string` (阻塞) | 等待循环完成取最终值 |
| **ApprovalGate.Request** | WorkPlan 构建时注入 | Approve 节点执行中 | `func(ctx, ApprovalRequest) → (string, error)` | 人工确认 IO |
| **NodeOpt / LoopOpt** | 节点构建时 | 节点添加时 (applyOpts) | `func(*node)` | 配置节点参数 |
| **ifCond (If 节点)** | 构建时 (WithCond) | If 节点路由时 | `func(string) bool` | 二选一条件判断 |
| **switchCases[].Match** | 构建时 (Case(...)) | Switch 节点路由时 | `func(string) bool` | 多路条件匹配 |
| **loopUntil (Loop 节点)** | 构建时 (Until(...)) | Loop 每次迭代后 | `func(string) bool` | 循环退出条件 |
| **ChatStream onChunk** | ChatStream 调用时 | 每个 SSE text delta | `func(delta string)` | 流式推送文本 |
| **ToolProvider 系列** | 注册到 Runtime 时 | dispatch 时 | ProviderName/Tools/HasTool/Dispatch | 工具调用 |
| **registryRouter.Execute** | Engine 初始化时 | Hub gRPC 收到请求时 | `func(*pb.ToolRequest) → ([]DispatchTarget, error)` | gRPC 请求路由 |

### 4.2 Signal 回调详解（最重要的回调机制）

```go
// 完整用法示例
wp := workplan.New(factory, nil, "系统提示词")

// 1. 注册 Loop 节点，拿到活引用
sig := wp.Loop("retry", "body_node",
    workplan.Until(workplan.Contains("成功")),
    workplan.MaxIter(5),
    workplan.OnExhausted("fallback"),
)

// 2. 注册回调 —— 每个迭代结果产生时立即触发
sig.OnUpdate(func(jsonValue string) {
    // jsonValue 始终是合法 JSON:
    //   - LLM 输出 JSON → 原始 JSON
    //   - LLM 输出纯文本 → JSON string (带引号)
    log.Printf("[进度] 第 %d 轮: %s", sig.Iter(), jsonValue)
})

// 3. 在另一个 goroutine 中阻塞等待循环完成
go func() {
    final := sig.Wait()       // 阻塞直到 Loop 结束
    log.Printf("[完成] 最终结果: %s", final)
}()

// 4. 非阻塞读取当前值
currentJSON := sig.Get()      // "" 或 JSON 字符串 (无阻塞)
currentText := sig.GetString() // 去引号的纯文本 (无阻塞)
```

**Signal 内部机制：**

```
sig.set(raw, iter)
  │
  ├─ normalized = toJSON(raw)   ← 规范化为合法 JSON
  │
  ├─ mu.Lock()
  │   sig.value = normalized
  │   sig.iter = iter
  │   copy cbs → local (持锁时间最短)
  │   mu.Unlock()
  │
  └─ for each cb in cbs:
       cb(normalized)            ← 同步调用，在 set 的 goroutine 中执行
       (注意: 长时间回调会阻塞 Loop 迭代)

sig.close()
  → close(done)                  ← 解除所有 Wait() 阻塞
  → sync.Once 保证只关一次
```

### 4.3 ApprovalGate 回调详解（人工确认 IO 抽象）

```go
// 接口定义 (workplan/gate.go)
type ApprovalGate interface {
    Request(ctx context.Context, req ApprovalRequest) (string, error)
}

// ApprovalRequest 携带完整上下文
type ApprovalRequest struct {
    NodeID  string   `json:"node_id"` // 哪个节点在请求确认
    Plan    string   `json:"plan"`    // Agent 生成的计划 (文本或 JSON)
    Options []string `json:"options"` // 可选项列表
}
```

**两种内置实现：**

| 实现 | 行为 | 适用场景 |
|------|------|----------|
| `CLIApprovalGate` | 格式化打印计划，`fmt.Scanln` 阻塞等待用户输入 | 命令行交互 |
| `AutoApproveGate` | 自动选第一个选项，打印确认日志 | 测试/自动化 |

**扩展点：** WebSocket/HTTP 实现只需实现 `ApprovalGate` 接口，WorkPlan 执行引擎完全不感知 IO 细节。

### 4.4 函数选项模式回调 (NodeOpt / LoopOpt)

这些不是运行时回调，而是**构建期配置注入**，采用函数选项 (Functional Options) 模式：

```go
// NodeOpt: 适用于所有节点
type NodeOpt func(*node)

WithPrompt(prompt string) NodeOpt          // 节点级 system prompt 覆盖
WithTools(tools ...string) NodeOpt         // 工具白名单
WithNext(id string) NodeOpt                // 显式指定下一跳

// LoopOpt: 仅适用于 Loop 节点
type LoopOpt func(*node)

Until(cond func(string) bool) LoopOpt      // 循环退出条件
MaxIter(max int) LoopOpt                   // 最大迭代次数
OnExhausted(nodeID string) LoopOpt         // 耗尽后跳转节点

// 应用方式 (sugar.go:applyOpts)
func applyOpts(n *node, opts []NodeOpt) {
    for _, o := range opts {
        o(n)  // 每个 opt 函数直接修改 node 字段
    }
}
```

### 4.5 条件谓词回调 (If / Switch / Loop)

所有这些回调的共同签名模式：**输入上一节点纯文本 → 输出 bool**

```go
// If 条件 (构建时传入)
wp.If("check", func(result string) bool {
    return strings.Contains(result, "成功")
}, "ok_path", "fail_path")

// Switch 匹配 (构建时传入)
wp.Switch("route",
    Case(func(s string) bool { return strings.Contains(s, "超时") }, "retry"),
    Case(Contains("成功"), "notify"),           // 快捷函数
    Default("escalate"),
)

// Loop 退出条件 (构建时传入)
wp.Loop("poll", "poll_body",
    Until(func(result string) bool {           // 返回 true 时退出
        return strings.Contains(result, "完成")
    }),
    MaxIter(10),
)

// 快捷条件函数 (sugar.go)
Contains(substr string) func(string) bool     // 输出包含子串
NotContains(substr string) func(string) bool  // 输出不包含子串
```

---

## 5. 核心数据流图

### 5.1 ReAct 对话循环 + 上下文控制 (完整数据流)

```
                        ┌──────────────────────┐
                        │      用户输入          │
                        └──────────┬───────────┘
                                   │
                                   ▼
┌──────────────────────────────────────────────────────────────────────┐
│                        Agent.Chat(ctx, input)                        │
│                                                                      │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 1: 追加 user 消息                                      │    │
│  │  history ← append(history, {role:"user", content:input})     │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                                   ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 2: 上下文控制 (每轮循环前)                              │    │
│  │                                                               │    │
│  │  NeedCompression(history, 6144)?                              │    │
│  │    │                                                          │    │
│  │    ├─ YES ──▶ CompressHistory(ctx, llm, history, 8192)       │    │
│  │    │           │                                              │    │
│  │    │           ├─ splitSystem() → system + non-system         │    │
│  │    │           ├─ buildCompressInput(old_msgs)                │    │
│  │    │           ├─ callCompressLLM() → summary                 │    │
│  │    │           └─ result = system + [summary] + keep(4)      │    │
│  │    │                                                          │    │
│  │    └─ 压缩失败 ──▶ TrimHistory(history, 8192) 硬截断          │    │
│  │                                                               │    │
│  │  去重: lastCompressLoop 标记，相邻轮次不重复压缩               │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                                   ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 3: 获取可用工具                                        │    │
│  │  tools = runtime.tools()  ← 实时聚合所有 Provider            │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                                   ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 4: LLM 推理                                            │    │
│  │  msg = llm.Complete(ctx, history, tools)                     │    │
│  │    │                                                          │    │
│  │    ├─ POST /v1/chat/completions                              │    │
│  │    └─ 返回: {content, tool_calls, reasoning_content}         │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                                   ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 5: 追加 assistant 消息                                 │    │
│  │  history ← append(history, {role:"assistant", ...})          │    │
│  │                                                               │    │
│  │  if len(tool_calls) == 0:                                    │    │
│  │    return content  ← 结束，返回纯文本                         │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                         有 tool_calls                                │
│                                   ▼                                  │
│  ┌─────────────────────────────────────────────────────────────┐    │
│  │  Step 6: dispatchToolCalls (最多重试 3 次)                   │    │
│  │                                                               │    │
│  │  ┌──────────────────────────────────────────────────────┐    │    │
│  │  │  并发执行 (信号量 max=5):                              │    │    │
│  │  │                                                         │    │    │
│  │  │  runtime.dispatch(ctx, toolName, argsJSON)              │    │    │
│  │  │    │                                                    │    │    │
│  │  │    ├─ HubProvider.Dispatch()                            │    │    │
│  │  │    │    └─ hub.Dispatch(gRPC) → tool result             │    │    │
│  │  │    │                                                     │    │    │
│  │  │    └─ MCPProvider.Dispatch()                            │    │    │
│  │  │         └─ client.CallTool(MCP) → tool result          │    │    │
│  │  │                                                         │    │    │
│  │  │  错误分类:                                               │    │    │
│  │  │    ErrToolUnavailable → transient (不注入 history)      │    │    │
│  │  │    其他 error          → {"error":"..."} 注入 history   │    │    │
│  │  └──────────────────────────────────────────────────────┘    │    │
│  │                                                               │    │
│  │  ┌──────────────────────────────────────────────────────┐    │    │
│  │  │  追加 tool 结果消息 (每个结果经 TruncateToolResult 截断)│    │    │
│  │  │  history ← append(history, {role:"tool", ...})         │    │    │
│  │  └──────────────────────────────────────────────────────┘    │    │
│  └─────────────────────────────────────────────────────────────┘    │
│                                   │                                  │
│                                   ▼                                  │
│                         回到 Step 2 (下一轮 loop)                    │
│                                                                      │
│  loop++  →  达到 maxLoops  →  return error (防止无限循环)           │
└──────────────────────────────────────────────────────────────────────┘
```

### 5.2 WorkPlan 执行数据流

```
                        WorkPlan.Run(ctx)
                              │
                              ▼
                    ┌──────────────────┐
                    │  全局信号量获取    │
                    │  (可配, 默认不限)  │
                    └────────┬─────────┘
                             │
                             ▼
               prevJSON = `""` (空 JSON string)
               currentID = entryID
                             │
              ┌──────────────┴──────────────┐
              │       节点执行循环            │
              │                              │
              ▼                              │
    ┌─────────────────────┐                 │
    │ primitiveRunNode()   │                 │
    │                      │                 │
    │ input = RenderInput( │                 │
    │   node.input,        │                 │
    │   prevJSON           │                 │
    │ )                    │                 │
    │                      │                 │
    │ ┌─ {{.PrevResult}}   │                 │
    │ └─ {{.Vars.key}}     │                 │
    └─────────┬────────────┘                 │
              │                              │
    ┌─────────▼────────────┐                 │
    │  按 NodeKind 分发     │                 │
    └─────────┬────────────┘                 │
              │                              │
    ┌─────────┼──────────────────────────┐   │
    │         │                          │   │
    ▼         ▼         ▼        ▼       ▼   │
  Auto    Approve   If/Switch  Loop    Fork  │
    │         │         │        │       │   │
    │    ┌────┴────┐    │    ┌───┴───┐   │   │
    │ 计划→人审→执行 │    │  loopBody │   │   │
    │    │    │    │    │   →Auto   │   │   │
    │    ▼    ▼    ▼    │   sig.set │   │   │
    │  skip abort exec  │   until?  │   │   │
    │                   │    │      │   │   │
    │                   │    ▼      │   │   │
    │                   │  next     │   │   │
    │                   │           │   │   │
    └─────────┬─────────┴───────────┴───┘   │
              │                              │
              ▼                              │
    ┌─────────────────────┐                 │
    │  更新 prevJSON       │                 │
    │  记录 NodeResult     │                 │
    │  若有 Checkpoint/Emit│                 │
    └─────────┬────────────┘                 │
              │                              │
              ▼                              │
    ┌─────────────────────┐                 │
    │  primitiveNext()     │                 │
    │  If/Switch/Loop 专用  │                 │
    │  其他走 node.next    │                 │
    └─────────┬────────────┘                 │
              │                              │
              ▼                              │
       currentID = nextID                    │
       currentID == "" → 结束 ──────────────┘
              │
              ▼
    ┌──────────────────────┐
    │  WorkPlanResult      │
    │  .NodeResults[]      │
    │  .Vars{}             │
    │  .Checkpoints{}      │
    │  .TotalElapsed       │
    └──────────────────────┘
```

### 5.3 Loop + Signal 回调数据流

```
构建期                          执行期 (Run)
───────                         ──────────────

sig := wp.Loop("poll", ...)     primitiveLoop(ctx, node, initJSON)
  │                                   │
  │ 创建 *Signal                       ├─ for iter := 0; ; iter++:
  │ 注册到 node.loopSignal              │    │
  │                                   │    ├─ out = primitiveAuto(bodyNode)
sig.OnUpdate(func(json){             │    │
  handleProgress(json)               │    ├─ sig.set(out, iter+1)
})  ──────────────────────────────────────┤    │   ├─ normalize → toJSON(out)
  │                                   │    │   ├─ 更新 value, iter
  │                                   │    │   └─ 触发所有 OnUpdate 回调
  │                                   │    │       │
  │   ┌───────────────────────────────┼────┼───────┘
  │   │                               │    │
  │   ▼                               │    ├─ until(fromJSON(out))?
  │ 回调执行:                          │    │   ├─ true → break
  │   handleProgress(jsonValue)       │    │   └─ false → 继续
  │   log.Printf(...)                 │    │
  │   更新 UI/进度条                  │    ├─ iter >= maxIter?
  │   发送 WebSocket 消息              │    │   └─ true → break
  │                                   │    │
sig.Wait() ── 阻塞 ──────────────────┤    └─ sig.close()
  │                                   │         │
  │                                   │    close(done)
  │   ┌───────────────────────────────┼────────┘
  │   │                               │
  │   ▼                               │
  │ Wait() 解除阻塞                    │
  │ return sig.Get()  ← 最终值        │
```

### 5.4 上下文压缩触发时机

```
Agent.Chat() 循环
  │
  ├─ loop=0 ─── lastCompressLoop=-1 → 检查压缩
  │              history 通常 < 6144 → 不触发
  │
  ├─ loop=1 ─── 大量 tool 结果追加
  │              history 可能 > 6144 → 触发压缩!
  │              lastCompressLoop = 1
  │
  ├─ loop=2 ─── loop - lastCompressLoop = 1 → 跳过检查!
  │              (压缩后 history 已缩减，单轮通常不会再次超标)
  │
  ├─ loop=3 ─── loop - lastCompressLoop = 2 → 恢复检查
  │              history 可能再次超标 → 触发压缩!
  │              lastCompressLoop = 3
  │
  └─ ...
```

---

## 6. 接口与结构体依赖关系矩阵

### 6.1 包间依赖 (Import Graph)

```
                    ┌──────────────┐
                    │ sdk/api      │  Engine (组装一切)
                    └──┬───┬───┬──┘
                       │   │   │
          ┌────────────┼───┤   └────────────┐
          │            │   │                │
          ▼            │   │                ▼
   ┌──────────┐        │   │        ┌──────────────┐
   │ config   │        │   │        │ sdk/cluster  │
   │ loader   │        │   │        │ handler.go   │
   └──────────┘        │   │        │ harness.go   │
                       │   │        │ registry.go  │
          ┌────────────┘   │        └──────┬───────┘
          │                │               │
          ▼                │               │
   ┌──────────────┐        │               │
   │ core/        │────────┘               │
   │ agent.go    │                         │
   │ runtime.go  │                         │
   └──┬───┬───┬──┘                         │
      │   │   │                            │
      ▼   │   ▼                            │
┌────────┐│┌──────────┐                    │
│ llm/   │││ history/ │                    │
│ chat_  │││ context_ │                    │
│ client │││ compress │                    │
│ .go    │││ limit.go │                    │
└────────┘│└──────────┘                    │
          │                                │
          ▼                                │
   ┌──────────────┐                        │
   │ provider/    │                        │
   │ tool_provider│                        │
   │ Hub_provider │                        │
   │ mcp_provider │                        │
   └──────┬───────┘                        │
          │                                │
          ▼                                │
   ┌──────────────┐                        │
   │ types/       │◄───────────────────────┘
   │ model.go     │  (所有包都依赖 types)
   └──────────────┘

   ┌──────────────┐
   │ workplan/    │  ← 零依赖! 只定义 Agent/AgentFactory 接口
   │ plan.go     │     不 import core/types/llm/provider
   │ node.go     │     通过依赖注入与上层解耦
   │ sugar.go    │
   │ gate.go     │
   └──────────────┘
```

**关键设计决策：** `workplan` 包不依赖任何其他 Seele 包。它定义 `Agent` 和 `AgentFactory` 接口，由上层 (`sdk/cluster`, `sdk/api`) 注入实现。这是依赖反转原则 (DIP) 的典型应用。

### 6.2 结构体/接口依赖关系总表

| 调用方 | 被调用方 | 关系类型 | 说明 |
|--------|---------|---------|------|
| `Engine` | `Runtime` | 持有 (ptr) | Engine 组装并持有 Runtime |
| `Engine` | `HubProvider` | 持有 (ptr) | 直接持有用于 Skills/Retire/Restore |
| `Engine` | `MCPProvider` | 持有 (ptr, 延迟初始化) | 首次 AttachMCPServer 时创建 |
| `Runtime` | `ToolProvider` | 持有切片 (接口) | 按注册顺序遍历 |
| `Runtime` | `ChatClient` | 持有 (ptr) | 1:1 的 LLM 客户端 |
| `Runtime` | `Agent` | 工厂创建 | NewAgent() 每次创建新实例 |
| `Agent` | `Runtime` | 持有 (ptr, 回指) | 调用 `runtime.dispatch()` 和 `runtime.tools()` |
| `Agent` | `ContextConfig` | 持有 (值类型) | 上下文管理配置 |
| `Agent` | `llm.ChatClient` | 间接 (通过 Runtime) | 调用 Complete/CompleteStream |
| `WorkPlan` | `Agent` (接口) | 间接 (通过 AgentFactory) | 每个节点通过 `factory.NewAgent()` 创建 |
| `WorkPlan` | `AgentFactory` | 持有 (接口, 注入) | 外部注入, WorkPlan 不关心实现 |
| `WorkPlan` | `ApprovalGate` | 持有 (接口, 注入) | 外部注入, WorkPlan 不关心 IO |
| `WorkPlan` | `node` | 持有切片 (私有类型) | 节点定义和执行 |
| `WorkPlan` | `Signal` | 创建并返回 | Loop 的活引用 |
| `node` | `Signal` | 持有 (ptr, kind=Loop) | 循环状态 |
| `node` | `SwitchCase` | 持有切片 (kind=Switch) | 分支匹配 |
| `node` | `ForkBranch` | 持有切片 (kind=Fork) | 并发分支定义 |
| `node` | `checkpointState` | 持有 (ptr, kind=Checkpoint) | 快照状态 |
| `CLIApprovalGate` | `ApprovalGate` | 实现 (接口) | 命令行人工确认 |
| `AutoApproveGate` | `ApprovalGate` | 实现 (接口) | 自动确认 (测试) |
| `EngineFactory` | `AgentFactory` | 实现 (接口) | 适配 Engine 为 AgentFactory |
| `core.Agent` | `workplan.Agent` | 隐式实现 (接口) | Chat 方法签名一致 |
| `HubProvider` | `ToolProvider` | 实现 (接口) | microHub 工具源 |
| `MCPProvider` | `ToolProvider` | 实现 (接口) | MCP 工具源 |

---

*本文档聚焦于 Seele 框架的上下文控制机制、WorkPlan 工作形式、各原语回调方法，提供了完整的类图、数据流图和依赖关系分析。*
