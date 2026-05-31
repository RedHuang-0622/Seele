# Seele 框架架构 Review

> 最后更新：2026-06-01

---

## 一、包结构总览

```
core/
├── agent/                    ← 编排层（Agent = 真正的 AI Agent）
│   ├── agent.go              ← Agent struct, New(), Shutdown(), Hub()/MCP() getter
│   ├── session.go            ← NewSession(), QuickChat(), DirectDispatch(), Tools()
│   └── pool.go               ← Pool（多会话管理）
│
├── session/                  ← 单次对话会话（Holder = 会话持有者）
│   ├── interface.go          ← ToolDispatcher, ApprovalCallback
│   ├── session.go            ← Holder struct, 历史/配置管理
│   ├── chat.go               ← Chat() / ChatStream() ReAct 循环
│   └── dispatch.go           ← dispatchToolCalls() + resolveApproval()
│
└── tool_holder/              ← 工具注册与调度（Holder = 工具持有者）
    ├── holder.go             ← Holder struct, New()
    ├── provider.go           ← Register() / Unregister()
    └── tools.go              ← Tools() / Dispatch()（含瞬时重试）

provider/                     ← ToolProvider 接口 + 两个具体实现
├── tool_provider.go          ← ToolProvider 接口定义 + ErrToolUnavailable
├── Hub_provider.go           ← HubProvider: 封装 microHub gRPC 工具
├── mcp_provider.go           ← MCPProvider: 封装 MCP 协议工具（stdio/sse）
└── hub_router.go             ← hubRouter: microHub 的路由器实现

types/model.go                ← 纯数据结构（零内部依赖）
llm/chat_client.go            ← OpenAI 兼容 HTTP 客户端（纯 stdlib）
history/                      ← 上下文管理
├── context_compress.go       ← LLM 压缩 + 硬截断
└── context_limit.go          ← Token 估算 + 结果截断 + 配置
config/loader.go              ← YAML 配置加载

workplan/                     ← 声明式工作流引擎（零内部依赖）
├── plan.go                   ← WorkPlan 定义 + Run/Resume
├── node.go                   ← Node, Question, ChoiceOption
├── primitive.go              ← 9 种执行原语
├── sugar.go                  ← 声明式 DSL（Auto/If/Loop/Fork...）
├── gate.go                   ← ApprovalGate 接口 + 3 种实现
└── validate.go               ← 拓扑校验（DFS 三色环检测）

sdk/
├── api/seele_api.go          ← 类型别名层（Engine = agent.Agent）
├── cli/repl.go               ← 交互式 REPL + 审批 UI
├── cli/prompt_loader.go      ← 系统提示词热加载（fsnotify）
└── cluster/                  ← 多 Agent 部署框架
    ├── harness.go            ← 启动框架（Run() = 一站式部署）
    └── handler.go            ← gRPC Handle + 审批暂停/恢复
```

---

## 二、分层架构

```
┌─────────────────────────────────────────┐
│           应用层 (model_agent/cmd)        │
│      quick_start / work_flow / cli       │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────┴──────────────────────┐
│           SDK 层 (sdk/)                  │
│  sdk/api   — 类型别名                    │
│  sdk/cli   — REPL + 审批交互             │
│  sdk/cluster — 多 Agent 部署框架          │
└──────────────────┬──────────────────────┘
                   │
┌──────────────────┴──────────────────────┐
│         编排层 (core/agent/)             │
│  Agent — 持有 LLM + tool_holder + Hub   │
│  Pool  — 多会话管理                      │
└──────┬──────────────┬───────────────────┘
       │              │
       ▼              ▼
┌─────────────┐ ┌──────────────┐
│  会话层      │ │  工具层       │
│core/session/ │ │core/tool_    │
│             │ │  holder/     │
│ Holder —    │ │ Holder —     │
│ ReAct 循环  │ │ 路由+重试     │
└──────┬──────┘ └──────┬───────┘
       │               │
       ▼               ▼
┌─────────────┐ ┌──────────────┐
│   LLM 层     │ │  Provider 层 │
│  llm/       │ │  provider/   │
│ ChatClient  │ │ HubProvider  │
│             │ │ MCPProvider  │
└──────┬──────┘ └──────┬───────┘
       │               │
       ▼               ▼
┌─────────────────────────────────────────┐
│        基础设施层                         │
│  types/ — 纯数据结构（零内部依赖）         │
│  history/ — 上下文压缩                    │
│  config/ — YAML 配置                     │
└─────────────────────────────────────────┘
```

**依赖方向：单向向下。零循环依赖。**

---

## 三、核心类型

### 3.1 编排层 Agent

```go
// core/agent/agent.go
type Agent struct {
    llm         *llm.ChatClient          // LLM 客户端（Agent 直接持有，所有 session 共享）
    tools       *tool_holder.Holder      // 工具注册中心
    hub         *hubbase.BaseHub         // microHub gRPC 服务
    hubProvider *provider.HubProvider    // Hub 工具适配器
    mcpProvider *provider.MCPProvider    // MCP 工具适配器（延迟初始化）
}

// 构造
agent.New(Options) (*Agent, error)

// 会话
a.NewSession(prompt, loops) *session.Holder
a.QuickChat(ctx, prompt, input) (string, error)
a.QuickChatStream(ctx, prompt, input, onChunk) (string, error)

// Provider 访问
a.Hub()  *provider.HubProvider   // Skills / Retire / Restore
a.MCP()  *provider.MCPProvider   // Attach / Detach / Refresh（延迟创建）
a.Tools() *tool_holder.Holder    // 工具注册中心
```

### 3.2 会话层 Holder

```go
// core/session/session.go
type Holder struct {
    llm        types.ChatCompleter    // LLM 推理接口
    tools      ToolDispatcher         // 工具调度接口
    sessionID  string
    history    []types.Message
    maxLoops   int                    // ReAct 循环上限
    contextCfg history.ContextConfig
    OnApproval ApprovalCallback       // 审批回调（nil = 旧行为）
}

// 构造
session.New(llm, tools, prompt, loops) *Holder

// 核心方法
h.Chat(ctx, input) (string, error)
h.ChatStream(ctx, input, onChunk) (string, error)

// 接口
type ToolDispatcher interface {
    Tools() []types.Tool
    Dispatch(ctx, name, argsJSON string) (string, error)
}
```

### 3.3 工具层 Holder

```go
// core/tool_holder/holder.go
type Holder struct {
    providers         []provider.ToolProvider
    DispatchRetries   int           // 默认 3
    DispatchRetryDelay time.Duration // 默认 2s
}

// 实现 session.ToolDispatcher
h.Tools() []types.Tool          // 聚合所有 provider
h.Dispatch(ctx, name, argsJSON) // 路由 + 瞬时重试
```

### 3.4 Provider 层

```go
// provider/tool_provider.go
type ToolProvider interface {
    ProviderName() string
    Tools() []types.Tool
    Dispatch(ctx, name, argsJSON string) (string, error)
    HasTool(name string) bool
}

// HubProvider  — 封装 microHub service_registry
// MCPProvider  — 封装 MCP 协议（stdio/sse）
// hubRouter    — 实现 hubbase.HubHandler（gRPC 路由）
```

---

## 四、数据流

### 4.1 普通 ReAct 循环

```
用户输入 → Agent.NewSession() → session.Holder
  → Chat(input)
    → llm.Complete(history, tools)          // ③ LLM 推理
    → if tool_calls:
        → dispatchToolCalls()
          → tools.Dispatch(name, args)       // ④ 工具路由（含重试）
          → tool result → 注入 history
        → loop continue
    → if text reply:
        → return to user                     // ⑤ 最终回复
```

### 4.2 审批流程（LLM 完全无感知）

```
dispatchToolCalls()
  → tools.Dispatch() 返回 {"status":"awaiting_approval",...}
  → parseApprovalQuestionID() 检测到审批
  → if OnApproval != nil:
      → resolveApproval():
          ① OnApproval(ctx, json) → choice key    // ⑥ 用户决策
          ② tools.Dispatch("_decide", {qID, choice})  // ⑦ 恢复工作流
          ③ if 嵌套审批 → goto ①
          ④ return 最终业务结果
      → 最终结果注入 history
  → LLM 继续推理（不知道审批发生过）
```

### 4.3 WorkPlan 暂停/恢复

```
AgentHandler.Execute(req)
  → WorkPlan.Run()
    → Approve 节点 → Plan Agent 生成计划 → pauseSnapshot
    → 返回 PausedWorkPlan
  → sendQuestion() → 构建 awaiting_approval JSON
  → 返回给 caller

AgentHandler.Execute(_decide)
  → handleDecide({question_id, choice})
  → WorkPlan.SetDecision(choice) → Resume()
    → executeApprove() → 继续后续节点
```

---

## 五、关键设计决策

| 决策 | 位置 | 理由 |
|------|------|------|
| LLM 由 Agent 直接持有 | `core/agent/agent.go` | Runtime 不再做中间人，消除不必要的委托 |
| 三层命名区分 | `Agent` / `session.Holder` / `tool_holder.Holder` | 避免一个 "Agent" 包揽三种含义 |
| 两个接口独立注入 | `session.Holder{llm, tools}` | 测试时可独立 mock LLM 和工具 |
| 瞬时重试下沉到 tool_holder | `core/tool_holder/tools.go` | Agent 不关心重试细节，换 mock 也不丢语义 |
| hubRouter 下沉到 provider | `provider/hub_router.go` | gRPC 协议细节不泄漏到编排层 |
| `_` 前缀工具对 LLM 不可见 | `provider/Hub_provider.go` | 框架内部工具不应被 LLM 自主调用 |
| 审批结果不入 LLM context | `core/session/dispatch.go` | 避免浪费 token，LLM 只看到最终结果 |
| 无循环依赖 | `workplan/` 定义自己的 Agent 接口 | WorkPlan 不依赖 core，core 不依赖 WorkPlan |
| Prompt 文件热加载 | `sdk/cli/prompt_loader.go` | fsnotify 监听，修改即生效 |

---

## 六、已知问题

### Bug（需修复）

| # | 严重度 | 问题 | 位置 |
|---|--------|------|------|
| B1 | 🔴 | `MCP()` 无锁并发 + `Shutdown()` 无锁访问 → nil dereference | `core/agent/agent.go:153-166` |
| B2 | 🔴 | `ctx.Background()` 起 health probe goroutine，Shutdown 无法停止 | `core/agent/agent.go:112` |
| B3 | 🔴 | `buildToolCalls` 非连续索引导致零值 ToolCall 注入 history | `llm/chat_client.go:302-310` |
| B4 | 🔴 | `parseApprovalQuestionID` 用字符串操作解析 JSON，空白字符导致误判 | `core/session/dispatch.go:124` |
| B5 | 🔴 | `ChatStream` 中 tool_call 返回时 onChunk 接收的内容被静默丢弃 | `core/session/chat.go:82-112` |
| B6 | 🟡 | `chat.go` 压缩逻辑和主循环体在 Chat/ChatStream 中重复 ~80% | `core/session/chat.go` |
| B7 | 🟡 | MCP `Attach()` 失败时 stdio 子进程泄漏 | `provider/mcp_provider.go:93-114` |
| B8 | 🟡 | `ChatStream` 中 `*msg.Content` 可能 nil panic | `core/session/chat.go:49` |
| B9 | 🟡 | `LoadAppConfig` 不对 LLM 字段设默认值 | `config/loader.go:69-92` |
| B10 | 🟡 | `HasTool` 对 `_` 前缀工具返回 false，但 Dispatch 中 `tool_holder` 会提前拦截 | `provider/Hub_provider.go:93` vs `tool_holder/tools.go:49` |
| B11 | 🟡 | `bufio.Scanner` 与 `handleApproval` 中的第二个 scanner 抢 `in` 缓冲区 | `sdk/cli/repl.go:73,179` |
| B12 | 🟡 | `CLIApprovalGate.Ask` goroutine 泄漏：ctx cancel 后 goroutine 仍阻塞 Scanln | `workplan/gate.go:197-209` |
| B13 | 🟡 | `SetMaxConcurrentWorkPlans` 无锁修改全局变量 | `workplan/plan.go:29-35` |
| B14 | 🟡 | `Skills()` Description 字段赋值为 `t.Method` | `provider/Hub_provider.go:202` |
| B15 | 🟡 | `Temperature=0` 被强制覆盖为 1.0，无法使用确定性输出 | `llm/chat_client.go:66,177` |

### 设计改进（建议）

| # | 问题 | 位置 |
|---|------|------|
| D1 | `EngineFactory` 命名不当 — 实际是 SessionFactory | `sdk/cluster/harness.go:74` |
| D2 | `approvePlanPrompt()` 死代码 — `prepareApprove` 内联了相同逻辑 | `workplan/node.go:396` |
| D3 | `chatCompletionRequest` 和 `chatCompletionStreamRequest` 仅差一个字段 | `llm/chat_client.go:37,125` |
| D4 | `LoopOpt` 和 `NodeOpt` 是相同的 `func(*node)` | `workplan/sugar.go:27,253` |
| D5 | `Skills()` 和 `Tools()` 的过滤逻辑重复 | `provider/Hub_provider.go` |
| D6 | `mu2` 命名不明确 | `provider/Hub_provider.go:40` |
| D7 | `ToolCallTimeOut` → 应为 `ToolCallTimeout` | `core/agent/agent.go` |
| D8 | `panic()` 在 SDK 库代码中 | `sdk/cli/repl.go:37` |
| D9 | Hub 就绪检查用 `time.Sleep` 而非健康检查 | `core/agent/agent.go:121` |
| D10 | 压缩 prompt 硬编码英文 | `history/context_compress.go:17` |
| D11 | `LLMConfig` godoc 仍说 "对应 config.yaml 的 agent 块" | `types/model.go:74` |
| D12 | `maxConcurrentFork = 3` 硬编码 | `workplan/primitive.go:300` |

### 已修复

| # | 问题 | 状态 |
|---|------|------|
| ✅ | `resolveRoute` TOCTOU 竞态 | 单次 RLock |
| ✅ | 废弃别名清理 | 删除 3 个方法 |
| ✅ | `AppConfig.LLM` yaml 标签 | `agent` → `llm` |
| ✅ | `config.LoadConfig` 默认值 | 补 MaxTokens/Timeout/Temperature |
| ✅ | `tool_holder.Dispatch` 重试硬编码 | 字段可配置 |
| ✅ | `SubagentRef` dead code | 已删除 |
| ✅ | `sdk/api/` Deprecated 噪声 | 已清理 |

---

## 七、依赖图

```
                    ┌──────────┐
                    │  types   │  ← 零内部依赖
                    └────┬─────┘
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
     ┌─────────┐   ┌──────────┐   ┌──────────┐
     │   llm   │   │ history  │   │ provider │
     └────┬────┘   └────┬─────┘   └────┬─────┘
          │             │              │
          └─────────────┼──────────────┘
                        ▼
              ┌──────────────────┐
              │ core/tool_holder │  ← 纯工具层
              └────────┬─────────┘
                       │
              ┌────────┴─────────┐
              ▼                  ▼
     ┌──────────────┐   ┌────────────────┐
     │ core/session │   │ core/agent     │  ← 编排层
     └──────┬───────┘   └───────┬────────┘
            │                   │
            └─────────┬─────────┘
                      ▼
              ┌──────────────┐
              │   sdk/*      │  ← SDK 层
              └──────────────┘

   ┌──────────┐
   │ workplan │  ← 独立岛（零内部依赖，自定 Agent 接口）
   └──────────┘
```

---

## 八、文件统计

| 包 | 文件数 | 代码行（估算） | 职责 |
|----|--------|---------------|------|
| `core/agent/` | 3 | ~350 | 编排 |
| `core/session/` | 4 | ~530 | 会话 |
| `core/tool_holder/` | 3 | ~140 | 工具注册 |
| `provider/` | 4 | ~750 | 工具适配 |
| `types/` | 1 | ~95 | 类型定义 |
| `llm/` | 1 | ~370 | HTTP 客户端 |
| `history/` | 2 | ~360 | 上下文管理 |
| `config/` | 1 | ~80 | 配置加载 |
| `workplan/` | 6 | ~2000 | 工作流引擎 |
| `sdk/api/` | 1 | ~35 | 类型别名 |
| `sdk/cli/` | 2 | ~300 | REPL |
| `sdk/cluster/` | 2 | ~420 | 部署框架 |

**总计：~5400 行核心框架代码**（不含测试、示例、工具实现）
