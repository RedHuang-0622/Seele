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
    mcpProvider *provider.MCPProvider    // MCP 工具适配器（延迟初始化，mcpMu 保护）
    mcpMu       sync.Mutex              // 保护 mcpProvider 读写，与 Shutdown 互斥
    opts        Options
    shutdown    chan struct{}            // 关闭信号，与 MCP() 协作防 nil dereference
    healthCancel context.CancelFunc     // 停止 health probe goroutine
}

// 构造
agent.New(Options) (*Agent, error)

// 会话
a.NewSession(prompt, loops) *session.Holder
a.QuickChat(ctx, prompt, input) (string, error)
a.QuickChatStream(ctx, prompt, input, onChunk) (string, error)

// Provider 访问
a.Hub()  *provider.HubProvider   // Skills / Retire / Restore
a.MCP()  *provider.MCPProvider   // Attach / Detach / Refresh（延迟创建，并发安全）
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
| MCP 延迟初始化 | `core/agent/agent.go MCP()` | 按需创建，减少非 MCP 场景的启动成本 |
| shutdown channel 机制 | `core/agent/agent.go` | MCP() 检测 shutdown 状态，避免 Shutdown 期间创建新 provider |
| health probe 可取消 | `core/agent/agent.go healthCancel` | Shutdown 时通过 context cancel 停止 goroutine |

---

## 六、已知问题

### Bug（需修复）

| # | 严重度 | 问题 | 位置 |
|---|--------|------|------|
| B1 | 🔴 | MCP `Attach()` 失败时 stdio 子进程泄漏（client 已创建但 Initialize 失败后未 Close） | `provider/mcp_provider.go:93-116` |
| B2 | 🔴 | `HubProvider.HasTool()` 对 `_` 前缀工具返回 false，导致 `tool_holder.Dispatch("_decide", ...)` 路由失败 — REPL 审批恢复路径（`resolveApproval` → `h.tools.Dispatch("_decide")`）无法找到提供者 | `provider/Hub_provider.go:107-118` |
| B3 | 🔴 | `Chat()` 中 `return *msg.Content` — 若 LLM 返回 tool_calls=[] 但 Content=nil 会 panic | `core/session/chat.go:49` |
| B4 | 🟡 | `chat.go` 压缩逻辑和主循环体在 Chat/ChatStream 中重复 ~80%（压缩检查块完全相同，主循环结构高度相似） | `core/session/chat.go` |
| B5 | 🟡 | `LoadAppConfig` 不对 LLM 字段设默认值（`LoadConfig` 设了，但 `LoadAppConfig` 只设 Hub 和 Registry） | `config/loader.go:69-92` |
| B6 | 🟡 | `bufio.Scanner` 与 `handleApproval` 中的第二个 scanner 共享底层 `in` reader，第二个 scanner 可能消耗主 scanner 的缓冲数据 | `sdk/cli/repl.go:73,179` |
| B7 | 🟡 | `CLIApprovalGate.Ask` goroutine 泄漏：ctx cancel 后 goroutine 仍阻塞 `fmt.Scanln` | `workplan/gate.go:197-201` |
| B8 | 🟡 | `SetMaxConcurrentWorkPlans` 无锁修改全局变量 `globalWorkPlanSem`，并发调用存在 data race | `workplan/plan.go:29-35` |
| B9 | 🟡 | `Skills()` Description 字段赋值为 `t.Method` 而非 `t.Description` | `provider/Hub_provider.go:203` |
| B10 | 🟡 | `Temperature=0` 被强制覆盖为 1.0，无法使用确定性输出（`Complete` 和 `doStreamRequest` 两处） | `llm/chat_client.go:66-68, 176-179` |
| B11 | 🔴 | `New()` 失败时 hub goroutine 泄漏 — `config.LoadConfig` 或 `NewHubProvider` 失败时，已启动的 hub 协程未被停止 | `core/agent/agent.go:117-138` |
| B12 | 🔴 | `primitiveFork` goroutine panic → 死锁 — `factory.NewAgent()` 返回 nil 时 `agent.Chat()` panic，goroutine 无 recover，`wg.Wait()` 永久阻塞 | `workplan/primitive.go:328-338` |
| B13 | 🟡 | `AgentHandler.Execute` 和 `handleDecide` goroutine 无 panic recovery — 工作流 panic 时 channel 空关闭，调用方收到 nil | `sdk/cluster/handler.go:119, 123-148` |
| B14 | 🟡 | `ChatStream` SSE 读取错误顺序 — `readErr` 在 line 被处理后检查，异常断开时可能处理截断的数据帧 | `llm/chat_client.go:339-363` |

### 设计改进（建议）

| # | 问题 | 位置 |
|---|------|------|
| D1 | `EngineFactory` 命名不当 — 实际创建的是 Session，应命名为 `SessionFactory` | `sdk/cluster/harness.go:74` |
| D2 | `approvePlanPrompt()` 死代码 — `prepareApprove` 内联了相同逻辑，未调用该方法 | `workplan/node.go:396` vs `workplan/primitive.go:88` |
| D3 | `chatCompletionRequest` 和 `chatCompletionStreamRequest` 仅差一个 `Stream bool` 字段 | `llm/chat_client.go:37,125` |
| D4 | `LoopOpt` 和 `NodeOpt` 是相同的 `func(*node)` 类型（类型别名不会增加类型安全） | `workplan/sugar.go:27,253` |
| D5 | `Skills()` 和 `Tools()` 的过滤逻辑重复（offline 检查、retired 过滤） | `provider/Hub_provider.go` |
| D6 | `mu2` 命名不明确，应改为 `indexMu` | `provider/Hub_provider.go:40` |
| D7 | `ToolCallTimeOut` → 应为 `ToolCallTimeout`（拼写错误） | `core/agent/agent.go:43` |
| D8 | `panic()` 在 SDK 库代码中 — 应返回 error 由调用方处理 | `sdk/cli/repl.go:37` |
| D9 | Hub 就绪检查用 `time.Sleep` 而非健康检查（`opts.HubStartupDelay` 粗暴等待） | `core/agent/agent.go:123` |
| D10 | 压缩 prompt 硬编码英文，不支持多语言 | `history/context_compress.go:17` |
| D11 | `LLMConfig` godoc 仍说 "对应 config.yaml 的 agent 块"，实际已改为 `llm:` | `types/model.go:82` |
| D12 | `maxConcurrentFork = 3` 硬编码，无配置入口 | `workplan/primitive.go:300` |

### 已修复

| # | 问题 | 状态 |
|---|------|------|
| ✅ | `MCP()` 无锁并发 — 新增 `mcpMu` + `shutdown` channel 防护 | 已修复 |
| ✅ | `Shutdown()` 无锁访问 `mcpProvider` — 新增 `mcpMu.Lock()` 保护 | 已修复 |
| ✅ | `ctx.Background()` health probe goroutine 不可停止 — 新增 `healthCancel` 并在 Shutdown 中调用 | 已修复 |
| ✅ | `buildToolCalls` 非连续索引导致零值 ToolCall — 改用 append + index check | 已修复 |
| ✅ | `parseApprovalQuestionID` 字符串解析 JSON → 改用 `json.Unmarshal` | 已修复 |
| ✅ | `ChatStream` tool_call 时 onChunk 静默丢弃 → 现在推送思考文本到 onChunk | 已修复 |
| ✅ | `resolveRoute` TOCTOU 竞态 — 单次 RLock 完成解析和查找 | 已修复 |
| ✅ | 废弃别名清理 — 删除 3 个方法 | 已修复 |
| ✅ | `AppConfig.LLM` yaml 标签 `agent` → `llm` | 已修复 |
| ✅ | `config.LoadConfig` 默认值 — 补 MaxTokens/Timeout/Temperature | 已修复 |
| ✅ | `tool_holder.Dispatch` 重试硬编码 — 改为 `DispatchRetries`/`DispatchRetryDelay` 字段可配置 | 已修复 |
| ✅ | `SubagentRef` dead code — 已删除 | 已修复 |
| ✅ | `sdk/api/` Deprecated 噪声 — 已清理 | 已修复 |

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

---

## 九、Bug 修复方案

### 🔴 严重（优先修复，预计 2-3 小时）

#### B1 — MCP Attach stdio 子进程泄漏

**文件**：`provider/mcp_provider.go:93-116`

**方案**：`Initialize` 失败时清理 client。

```go
// 当前（有问题）
c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
// ...
if _, err := c.Initialize(ctx, initReq); err != nil {
    return fmt.Errorf("MCPProvider.Attach: initialize %q: %w", cfg.Name, err)
}

// 修复后
c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
if err != nil {
    return fmt.Errorf("MCPProvider.Attach: create client %q: %w", cfg.Name, err)
}
if _, err := c.Initialize(ctx, initReq); err != nil {
    c.Close() // ← 新增：清理子进程
    return fmt.Errorf("MCPProvider.Attach: initialize %q: %w", cfg.Name, err)
}
```

**风险**：低，改动 2 行。

---

#### B2 — HubProvider.HasTool `_` 前缀路由失败

**文件**：`provider/Hub_provider.go:93-118`

**方案**：`_` 前缀工具加入 `toolIndex` 但不暴露给 LLM。

```go
func (p *HubProvider) Tools() []types.Tool {
    // ...
    for _, t := range all {
        // ...
        newIndex[t.Name] = struct{}{}   // ← 移到过滤之前，覆盖所有工具（含 _ 前缀）
        if strings.HasPrefix(t.Name, "_") {
            continue  // 仍然从返回列表中排除，LLM 不可见
        }
        result = append(result, ...)
    }
    // ...
}
```

**风险**：低，改动 3 行（移动一行位置），不影响 LLM 可见工具。

---

#### B3 — Chat() `*msg.Content` nil panic

**文件**：`core/session/chat.go:49`

**方案**：添加 nil guard。

```go
// 当前
if len(msg.ToolCalls) == 0 {
    return *msg.Content, nil
}

// 修复后
if len(msg.ToolCalls) == 0 {
    if msg.Content == nil {
        return "", fmt.Errorf("LLM returned empty content with no tool calls")
    }
    return *msg.Content, nil
}
```

**风险**：低，改动 3 行。

---

#### B11 — `New()` 失败时 hub goroutine 泄漏

**文件**：`core/agent/agent.go:107-155`

**方案**：失败路径显式停止 hub。

```go
func New(opts Options) (*Agent, error) {
    opts.withDefaults()
    // ...
    hub := hubbase.New(provider.NewHubRouter())
    go func() {
        if err := hub.ServeAsync(opts.HubAddr, 5); err != nil {
            opts.Logger.Errorf("hub exited: %v", err)
        }
    }()
    time.Sleep(opts.HubStartupDelay)

    llmCfg, err := config.LoadConfig(opts.LLMConfigPath)
    if err != nil {
        hub.Stop() // ← 新增：停止 hub
        return nil, fmt.Errorf("agent: load llm config %q: %w", opts.LLMConfigPath, err)
    }
    // ...
    hubProv, err := provider.NewHubProvider(hub, opts.ToolCallTimeOut)
    if err != nil {
        hub.Stop() // ← 新增：停止 hub
        return nil, fmt.Errorf("agent: new hub provider: %w", err)
    }
    // ...
}
```

**前提**：需确认 `hubbase.BaseHub` 有 `Stop()` 方法。若无，可用 context cancel + ServeAsync 接收 cancel。

**备选方案**：将 hub 启动移到 LLM 配置加载之后，只有配置成功才启动 hub。

**风险**：低，需确认 API。

---

#### B12 — primitiveFork panic → 死锁

**文件**：`workplan/primitive.go:328-338`

**方案**：nil 检查 + recover 双重防护。

```go
// 当前
agent := wp.factory.NewAgent(prompt)
out, err := agent.Chat(ctx, input)

// 修复后
agent := wp.factory.NewAgent(prompt)
if agent == nil {
    results[i] = branchResult{label: b.Label, err: fmt.Errorf("factory returned nil agent")}
    return
}
// 再加 recover 兜底
defer func() {
    if r := recover(); r != nil {
        results[i] = branchResult{label: b.Label, err: fmt.Errorf("branch panic: %v", r)}
    }
}()
out, err := agent.Chat(ctx, input)
```

**风险**：低，改动 8 行。

---

### 🟡 中等（第二优先级，预计 1-2 小时）

#### B4 — Chat/ChatStream 压缩逻辑重复

**文件**：`core/session/chat.go`

**方案**：提取 `compressIfNeeded` 方法。

```go
func (h *Holder) compressIfNeeded(ctx context.Context, loop int) {
    if h.lastCompressLoop >= 0 && loop-h.lastCompressLoop <= 1 {
        return
    }
    cfg := h.contextCfg
    if !history.NeedCompression(h.history, cfg.CompressThreshold) {
        return
    }
    compressed, err := history.CompressHistory(ctx, h.llm, h.history, cfg.MaxTokens)
    if err != nil {
        log.Printf("[session] %s compression failed: %v", h.sessionID, err)
        h.history = history.TrimHistory(h.history, cfg.MaxTokens)
    } else {
        h.history = compressed
    }
    h.lastCompressLoop = loop
}
```

Chat/ChatStream 中 `cfg` 块替换为一行：`h.compressIfNeeded(ctx, loop)`

**风险**：低，纯重构。

---

#### B5 — LoadAppConfig LLM 默认值

**文件**：`config/loader.go:69-92`

**方案**：对齐 `LoadConfig` 的默认值逻辑。

```go
func LoadAppConfig(path string) (types.AppConfig, error) {
    // ...
    // LLM 默认值（对齐 LoadConfig）
    if app.LLM.MaxTokens <= 0 {
        app.LLM.MaxTokens = 4096
    }
    if app.LLM.Timeout <= 0 {
        app.LLM.Timeout = 60
    }
    if app.LLM.Temperature == 0 {
        app.LLM.Temperature = 1.0
    }
    // Hub 默认值...
    // Registry 默认值...
}
```

**风险**：低，纯补全。

---

#### B6 — 双 Scanner 抢缓冲区

**文件**：`sdk/cli/repl.go:73,179`

**方案**：`handleApproval` 复用主 scanner，不创建新的。

```go
// 将 scanner 作为参数传入
func handleApproval(out io.Writer, scanner *bufio.Scanner, approvalJSON string) (string, error) {
    // 用传入的 scanner 读取，不再新建
    if !scanner.Scan() { ... }
    input := strings.TrimSpace(scanner.Text())
    // ...
}
```

**风险**：低，改动函数签名。

---

#### B7 — CLIApprovalGate goroutine 泄漏

**文件**：`workplan/gate.go:197-201`

**方案**：用 `select` 使 goroutine 可取消。

```go
// 当前
go func() {
    var s string
    fmt.Scanln(&s)
    inputCh <- strings.TrimSpace(s)
}()

// 修复后：无法中断 fmt.Scanln（Go 标准库限制）
// 方案：用 context 的 Done 做 select，但 Scanln 无 context 支持
// 实际修复：文档标注 + 允许 goroutine 自然退出（用户输入后自动清理）
// 治本方案：替换为带 timeout 的 select + bufio.Reader.ReadLine
reader := bufio.NewReader(os.Stdin)
go func() {
    s, _ := reader.ReadString('\n')
    inputCh <- strings.TrimSpace(s)
}()
```

**注意**：Go 的 `fmt.Scanln` 本身不可取消，只能换用 `bufio.Reader`。

---

#### B8 — SetMaxConcurrentWorkPlans data race

**文件**：`workplan/plan.go:29-35`

**方案**：加锁或使用 `sync.Once` 确保只调用一次。

```go
var (
    globalWorkPlanSem   chan struct{}
    globalWorkPlanSemMu sync.Mutex
)

func SetMaxConcurrentWorkPlans(n int) {
    globalWorkPlanSemMu.Lock()
    defer globalWorkPlanSemMu.Unlock()
    if n <= 0 {
        globalWorkPlanSem = nil
        return
    }
    globalWorkPlanSem = make(chan struct{}, n)
}
```

**风险**：极低，纯并发安全补丁。

---

#### B9 — Skills() Description = t.Method

**文件**：`provider/Hub_provider.go:203`

**方案**：使用 registry 中的 description 字段（如有）或留空。

```go
result = append(result, types.SkillInfo{
    Name:        t.Name,
    Method:      t.Method,
    Description: t.Description, // ← 当前是 t.Method
    Addr:        t.Addr,
})
```

**前提**：确认 `registry` 的 Tool 结构有 `Description` 字段。

---

#### B10 — Temperature=0 覆盖

**文件**：`llm/chat_client.go:66-68, 176-179`

**方案**：区分"未设置"(0) 和"显式设为 0"。当前 `LLMConfig.Temperature` 是 `float64`，无法区分。改法：

```go
// 方案 A：用指针
type LLMConfig struct {
    Temperature *float64 `yaml:"temperature"`
}
// 方案 B（更简单）：用特殊哨兵值 -1 表示未设置
// 在 LoadConfig 中：Temperature 未设置时默认 1.0
// 用户显式设置 0 → 尊重用户选择
```

**推荐方案 B**（改动最小）：将 `LoadConfig` 中的默认逻辑改为只在 yaml 未显式设置时干预，`chat_client.go` 中移除覆盖逻辑。

```go
// chat_client.go — 删除覆盖
// if temperature == 0 { temperature = 1.0 }  ← 删除这两行

// config/loader.go — 保留默认（只在未设置时生效）
```

---

#### B13 — AgentHandler goroutine 无 panic recovery

**文件**：`sdk/cluster/handler.go:119, 123-148`

**方案**：顶层 defer recover。

```go
go func() {
    defer close(ch)
    defer func() {
        if r := recover(); r != nil {
            ch <- errorResp(h.name, req.TaskId, "PANIC",
                fmt.Sprintf("workflow panic: %v", r))
        }
    }()
    // ... 原有逻辑
}()
```

`handleDecide` 同理。

**风险**：低，纯防御性编程。

---

#### B14 — SSE 读取错误顺序

**文件**：`llm/chat_client.go:339-363`

**方案**：先检查错误再处理数据。

```go
for {
    line, readErr := reader.ReadString('\n')
    if readErr != nil && readErr != io.EOF {
        return "", "", nil, fmt.Errorf("ChatClient stream: read SSE: %w", readErr)
    }
    line = strings.TrimRight(line, "\r\n")
    if line == "" && readErr == io.EOF {
        break
    }
    // ... 处理 line
    if readErr == io.EOF {
        break
    }
}
```

**风险**：低，改动循环结构。

---

### 📋 建议修复顺序

| 优先级 | Bug | 估算 | 理由 |
|--------|-----|------|------|
| P0 | B2 `_` 前缀路由 | 5 min | 影响 REPL 审批功能，一行修复 |
| P0 | B3 nil Content | 5 min | 一行 nil guard，生产可能触发 |
| P0 | B1 MCP 泄漏 | 10 min | 三行代码，资源泄漏 |
| P1 | B12 Fork 死锁 | 15 min | 可能死锁整个 WorkPlan |
| P1 | B11 Hub 泄漏 | 20 min | 需确认 API，影响 New() 重试场景 |
| P1 | B10 Temperature | 15 min | 功能缺陷，影响确定性输出 |
| P2 | B7 goroutine 泄漏 | 15 min | 需换 Scanln |
| P2 | B13 panic recovery | 10 min | 防御性，影响线上排查 |
| P2 | B8 data race | 5 min | 一行加锁 |
| P3 | B4/B5/B6/B9/B14 | 60 min | 代码质量改进，无直接危害 |

**总计**：5 个 🔴 严重 bug 约需 **1 小时**，全部 14 个约需 **3 小时**。
