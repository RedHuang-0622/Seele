# Seele 框架代码审查

> 审查日期：2026-05-18 · 覆盖范围：除 `ops_tools/*` `test/*` 外的全部 Go 源码

---

## 目录

1. [架构总览](#1-架构总览)
2. [类图（类型关系全景）](#2-类图类型关系全景)
3. [数据流图](#3-数据流图)
4. [分层设计详解](#4-分层设计详解)
5. [请求风暴与Token风暴防护](#5-请求风暴与token风暴防护)
6. [设计模式与关键决策](#6-设计模式与关键决策)
7. [发现的问题](#7-发现的问题)
8. [架构亮点](#8-架构亮点)

---

## 1. 架构总览

Seele 是一个 Go 语言 AI Agent 框架，核心能力是 **LLM ReAct 循环驱动 + 多源工具接入 + 声明式工作流编排**。

### 1.1 包结构

```
Seele/
├── types/model.go           所有共享类型定义 (Message / Tool / LLMConfig / SkillInfo)
├── llm/chat_client.go        OpenAI 兼容 HTTP 客户端 (同步 + SSE 流)
├── config/loader.go          YAML 配置加载 (LLMConfig / AppConfig)
├── history/
│   ├── context_limit.go      Token 估算 + 截断 + 硬截断 + ContextConfig
│   └── context_compress.go   LLM 驱动的上下文压缩
├── provider/
│   ├── tool_provider.go      ToolProvider 插件接口
│   ├── Hub_provider.go       microHub gRPC 工具源适配器
│   └── mcp_provider.go       MCP 协议工具源适配器
├── core/
│   ├── runtime.go            Provider 注册/路由/Agent 工厂
│   └── agent.go              ReAct 循环 + 流式 + 上下文压缩调度
├── workplan/
│   ├── node.go               节点/信号/结果类型定义
│   ├── plan.go               私有原语执行引擎
│   ├── sugar.go              公有链式 API 语法糖
│   └── gate.go               人工确认 IO 抽象
├── sdk/
│   ├── cli/repl.go           交互式 REPL
│   ├── api/seele_api.go      Engine 组装工厂
│   └── cluster/              多 Agent 集群 Harness + Registry
└── util/                     命令执行 + Viper YAML 读取
```

### 1.2 分层结构

```
┌──────────────────────────────────────────────────────────────────┐
│  SDK 入口层                                                       │
│  sdk/cli/repl.go        交互式 REPL                               │
│  sdk/api/seele_api.go   Engine 组装工厂                           │
│  sdk/cluster/           多 Agent 集群 Harness + Registry           │
├──────────────────────────────────────────────────────────────────┤
│  工作流编排层                                                     │
│  workplan/plan.go       私有原语执行引擎                           │
│  workplan/sugar.go      公有链式 API 语法糖                        │
│  workplan/node.go       节点/信号/结果类型定义                      │
│  workplan/gate.go       人工确认 IO 抽象                          │
├──────────────────────────────────────────────────────────────────┤
│  Agent 对话层                                                     │
│  core/agent.go          ReAct 循环 + 流式 + 上下文压缩调度          │
│  core/runtime.go        注册/路由/Agent 工厂                       │
├──────────────────────────────────────────────────────────────────┤
│  上下文管理层                                                     │
│  history/context_compress.go  LLM 驱动的上下文压缩                 │
│  history/context_limit.go     Token 估算 + 截断 + 硬截断           │
├──────────────────────────────────────────────────────────────────┤
│  Provider 实现层                                                  │
│  provider/tool_provider.go  ToolProvider 插件接口                  │
│  provider/Hub_provider.go   microHub gRPC 工具源适配器             │
│  provider/mcp_provider.go   MCP 协议工具源适配器                    │
├──────────────────────────────────────────────────────────────────┤
│  基础设施层                                                       │
│  llm/chat_client.go      OpenAI 兼容 HTTP 客户端 (同步+SSE流)      │
│  types/model.go          所有共享类型定义                           │
│  config/loader.go        YAML 配置加载                             │
│  util/                   命令执行 + Viper YAML 读取                 │
└──────────────────────────────────────────────────────────────────┘
```

### 1.3 模块职责矩阵

| 模块 | 知道 LLM? | 知道 ToolProvider? | 知道 gRPC/MCP? | 职责 |
|------|----------|-------------------|----------------|------|
| `core/agent.go` | ✓ | ✗ | ✗ | 对话循环 + 历史管理 |
| `core/runtime.go` | ✓ | ✓ | ✗ | Provider 注册 + dispatch 路由 |
| `history/context_compress.go` | ✓ | ✗ | ✗ | LLM 驱动历史压缩 |
| `history/context_limit.go` | ✗ | ✗ | ✗ | 纯函数 token 估算 + 截断 |
| `provider/tool_provider.go` | ✗ | ✓ (接口定义) | ✗ | 抽象契约 |
| `provider/Hub_provider.go` | ✗ | ✓ (实现) | ✓ | microHub gRPC 适配 |
| `provider/mcp_provider.go` | ✗ | ✓ (实现) | ✓ | MCP 协议适配 |
| `workplan/*` | ✗ | ✗ | ✗ | 工作流编排 |
| `sdk/api/seele_api.go` | ✓ | ✓ | ✓ | 全局组装 |

---

## 2. 类图（类型关系全景）

```
                          ┌─────────────────────┐
                          │    ToolProvider      │  (provider/tool_provider.go)
                          │       接口           │
                          ├─────────────────────┤
                          │ ProviderName() string│
                          │ Tools() []Tool       │
                          │ HasTool(name) bool   │
                          │ Dispatch(ctx,n,args) │
                          └──────┬──────┬───────┘
                ┌────────────────┘      └────────────────┐
                ▼                                        ▼
   ┌──────────────────────┐              ┌──────────────────────┐
   │     HubProvider      │              │     MCPProvider      │
   │(provider/Hub_provider│              │(provider/mcp_provider│
   │      .go)            │              │      .go)            │
   ├──────────────────────┤              ├──────────────────────┤
   │ hub     *BaseHub     │              │ servers map[string]  │
   │ retired set          │              │   *mcpServerConn     │
   │ toolIndex cache      │              ├──────────────────────┤
   │ toolCallTimeout      │              │ Attach/Detach()      │
   ├──────────────────────┤              │ RefreshTools()       │
   │ Retire/Restore()     │              │ fetchTools()         │
   │ Skills() []SkillInfo │              │ resolveRoute()       │
   │ buildParameters()    │              └──────────────────────┘
   │ allTransportErrors() │
   └──────────────────────┘
                                        ┌──────────────────────┐
   ┌──────────────────────┐             │     ChatClient       │
   │      Runtime         │             │  (llm/chat_client.go)│
   │   (core/runtime.go)  │             ├──────────────────────┤
   ├──────────────────────┤  1:1       │ Cfg     LLMConfig     │
   │ llm     *ChatClient ─┼────────────▶│ Client  *http.Client  │
   │ providers []ToolProv │             ├──────────────────────┤
   │ mu     sync.RWMutex  │             │ Complete()    同步    │
   ├──────────────────────┤             │ CompleteStream()      │
   │ Register/Unregister()│             │ doStreamRequest()    │
   │ NewAgent() *Agent ───┼─┐           │ processFrame()       │
   │ tools() (私有)       │ │           │ buildToolCalls()     │
   │ dispatch() (私有)    │ │           └──────────────────────┘
   └──────────────────────┘ │
                            │ 创建
                            ▼
   ┌─────────────────────────────────────────────────┐
   │                   Agent                         │
   │              (core/agent.go)                    │
   ├─────────────────────────────────────────────────┤
   │ runtime          *Runtime                       │
   │ sessionID        string                         │
   │ history          []Message                      │
   │ maxLoops         int (默认 4)                    │
   │ contextCfg       history.ContextConfig          │  ← 可配置
   │ lastCompressLoop int                            │  ← 去重压缩
   ├─────────────────────────────────────────────────┤
   │ Chat(ctx, input) → (string, error)              │
   │ ChatStream(ctx, input, onChunk) → (string, err) │
   │ SetContextConfig(cfg) / ContextConfig()         │
   │ SetMaxLoops(n) / MaxLoops()                    │
   │ ClearHistory() / History()                     │
   │ ForceAppendHistory(msg)                         │
   └─────────────────────────────────────────────────┘

   ┌─────────────────────────────────────────────────┐
   │              ContextConfig                       │
   │        (history/context_limit.go)               │
   ├─────────────────────────────────────────────────┤
   │ MaxTokens          int  (默认 8192)              │
   │ CompressThreshold  int  (默认 6144, 75%)         │
   │ MaxToolResultChars int  (默认 4000)              │
   ├─────────────────────────────────────────────────┤
   │ DefaultContextConfig() → ContextConfig           │
   │ Effective() → ContextConfig (零值→默认值)         │
   └─────────────────────────────────────────────────┘

   ┌──────────────────────┐
   │     WorkPlan         │
   │  (workplan/plan.go)  │
   ├──────────────────────┤
   │ nodes     []*node    │
   │ nodeIndex map        │
   │ entryID   string     │
   │ factory   AgentFact  │──── 注入，由 Runtime/Engine 适配
   │ gate      ApprovalGate
   │ maxConcurrentFork int│──── 可配置 (默认 3)
   │ vars      map        │
   ├──────────────────────┤
   │ Run() → Result       │
   │  └─ primitiveAuto()  │──── 调用 agent.Chat()
   │  └─ primitiveFork()  │──── 信号量限制并发 (可配置)
   │  └─ primitiveLoop()  │──── Signal 响应式 + 迭代退避
   │  └─ primitiveApprove()─── gate.Request()
   │ SetMaxConcurrentFork(n) │
   └──────────────────────┘
           │ 实现
           ▼
   ┌──────────────────────┐
   │    AgentFactory      │  (workplan 接口)
   │    Agent             │  (workplan 接口)
   └──────────────────────┘
           ▲ 适配自
   ┌──────┴───────────────┐
   │ Engine (seele_api.go)│  → EngineFactory 适配器
   │ Runtime              │  → rtAgentFactory (测试)
   └──────────────────────┘

全局并发控制 (workplan/plan.go):
  SetMaxConcurrentWorkPlans(n)  — Run() 入口信号量，限制所有 WorkPlan 并发数

消息类型 (types/model.go):
  Message ──────── role + content(*string) + tool_calls + tool_call_id + name + reasoning
    └─ ToolCall ── id + type + function
         └─ ToolCallFunction ── name + arguments(JSON string)
  Tool ─────────── type + function
    └─ ToolFunction ── name + description + parameters(map)
```

---

## 3. 数据流图

### 3.1 启动流程

```
main() / agent.Run()
  │
  ├─ registry.Init(path)              加载 registry.yaml
  ├─ registry.ProbeAllOnStartup()     所有工具健康检查
  ├─ registry.StartHealthProbe()      后台 15s 周期探测
  │
  ├─ hubbase.New(router)              创建 microHub
  ├─ hub.ServeAsync(addr)             启动 gRPC Server (后台)
  │
  ├─ LoadConfig(path)                 读取 config.yaml → LLMConfig
  │    └─ os.ReadFile + yaml.Unmarshal(AppConfig)
  │
  ├─ core.NewRuntime(llmCfg)          创建 Runtime
  │    └─ llm.NewChatClient(llmCfg)  初始化 HTTP 客户端
  │
  ├─ provider.NewHubProvider(hub)    创建 Hub 工具源
  ├─ rt.Register(hubProv)            注册到 Runtime
  │
  └─ [可选] AttachMCPServer(ctx, cfg)
       ├─ ensureMCPProvider()         延迟创建 MCPProvider
       └─ mcpProv.Attach(ctx, cfg)
            ├─ NewStdioMCPClient / NewSSEMCPClient
            ├─ client.Initialize()   MCP 握手
            ├─ client.ListTools()    拉取工具列表
            └─ 转为 Tool 格式
```

### 3.2 ReAct 对话循环（核心热路径）

```
用户: "帮我 ping 127.0.0.1"
  │
  ▼
Agent.Chat(ctx, input)
  │
  ├─ 1. 追加 user message → history
  │
  ├─ 2. 上下文检查 (每轮循环前，跳过相邻轮次冗余检查)
  │    ├─ lastCompressLoop 保护: 相邻两轮不重复压缩
  │    ├─ NeedCompression(history, threshold)?
  │    │    ├─ YES → CompressHistory(ctx, llm, history, maxTokens)
  │    │    │         ├─ splitSystem()          分离 system + 非 system
  │    │    │         ├─ 保留最后 4 条 + 所有 system
  │    │    │         ├─ buildCompressInput()   序列化可压缩部分
  │    │    │         ├─ callCompressLLM()      LLM (nil tools, 300 tokens)
  │    │    │         └─ 组装: system + [摘要] + 保留消息
  │    │    └─ NO  → 继续
  │    └─ fallback: TrimHistory() 硬截断 (MaxTokens=8192)
  │
  ├─ 3. tools = rt.tools()         ← 每次实时聚合，支持热更新
  │
  ├─ 4. msg = llm.Complete(ctx, history, tools)
  │    │  POST /v1/chat/completions
  │    │
  │    ├─ 返回 tool_calls → dispatch 分支
  │    └─ 返回纯文本    → 结束循环
  │
  ├─ 5. 追加 assistant message → history
  │
  ├─ 6. [若有 tool_calls] dispatch 循环 (最多 3 次重试, 间隔 2s)
  │    │
  │    │  ┌─────────────────────────────────────────────┐
  │    │  │  并发 dispatch (信号量 max=5)                 │
  │    │  │                                              │
  │    │  │  rt.dispatch(ctx, name, argsJSON)            │
  │    │  │    ├─ HubProvider.Dispatch()                 │
  │    │  │    │    ├─ registry.SelectToolByName()       │
  │    │  │    │    ├─ 检查 retired / JSON valid         │
  │    │  │    │    ├─ pb_api.Request().Build()          │
  │    │  │    │    ├─ hub.Dispatch(ctx, req)            │
  │    │  │    │    └─ allTransportErrors()?             │
  │    │  │    │       ├─ YES → ErrToolUnavailable       │
  │    │  │    │       └─ NO  → business error/text      │
  │    │  │    │                                        │
  │    │  │    └─ MCPProvider.Dispatch()                │
  │    │  │         ├─ resolveRoute(name)               │
  │    │  │         ├─ client.CallTool(ctx, req)        │
  │    │  │         └─ extractMCPContent(result)        │
  │    │  │                                              │
  │    │  │  错误分类：                                  │
  │    │  │    ErrToolUnavailable → transient (不注入历史)│
  │    │  │    其他 error         → {"error":"..."}      │
  │    │  └─────────────────────────────────────────────┘
  │    │
  │    │  有 transient → sleep 2s, 重试整轮 dispatch
  │    │  全成功       → 跳出重试
  │    │
  │    ▼
  │    追加 tool 消息 → history (每条经 TruncateToolResult 截断)
  │
  └─ 7. 回到步骤 2 (直到无 tool_calls 或达到 maxLoops)
```

### 3.3 流式响应 (ChatStream)

与 Chat 核心逻辑相同，区别在 LLM 调用层：

```
Agent.ChatStream(ctx, input, onChunk)
  │
  ├─ tool_call 轮次 → llm.CompleteStream()  静默累积 chunks
  │    └─ 确认无 tool_calls 后才推送 onChunk → 防止 JSON 碎片泄露
  │
  └─ 最终文本轮次 → 实时推送 onChunk(delta)
```

### 3.4 WorkPlan 执行流

```
WorkPlan.Run(ctx)
  │
  │  ┌── 全局信号量获取 (SetMaxConcurrentWorkPlans 控制) ──┐
  │  │  超限 → 阻塞等待或随 ctx 取消                        │
  │  └──────────────────────────────────────────────────┘
  │
  │  遍历节点链表:
  │
  ├─ Auto:      agent.Chat(ctx, input)              单 Agent, 完整 ReAct
  │
  ├─ Approve:   ① Agent 生成计划
  │             ② gate.Request() 阻塞等人确认
  │             ③ Agent 真正执行 或 跳过/终止
  │
  ├─ If:        cond(prevJSON) → trueID / falseID
  ├─ Switch:    顺序匹配 cases → 跳转对应 nextID
  │
  ├─ Loop:      for iter < maxIter:
  │              ├─ primitiveAuto(bodyNode)
  │              ├─ sig.set(result, iter)  → 触发 OnUpdate 回调
  │              ├─ until(result)? → break
  │              └─ 迭代间退避: min(100ms×iter, 2s)   ← 防紧循环
  │
  ├─ Fork:      并发 N 个 agent.Chat()  (信号量限制, 默认 3)
  │             汇合: {"label1": result1, "label2": ...}
  │
  ├─ Checkpoint: 快照当前输出到 result.Checkpoints
  │
  └─ Emit:      当前值写入 wp.vars[key], 后续节点 {{.Vars.key}} 引用
```

### 3.5 上下文压缩流程

```
历史 > CompressThreshold (默认 6144 tokens)
  │
  ├─ splitSystem(msgs) → system + rest
  │   rest 拆分为: compressible (旧) + keep (最近 4 条)
  │
  ├─ buildCompressInput(compressible)
  │   user → "User: ..."
  │   assistant(有 tool_calls) → "Called tool(args)"
  │   tool → "Result from tool: ..." (超 800 字截断)
  │
  ├─ callCompressLLM(ctx, client, input)
  │   ├─ 临时覆盖 cfg.MaxTokens=300, Temperature=0.3
  │   ├─ system: "Summarize the execution history..."
  │   └─ Complete(ctx, messages, nil)  ← nil tools, 确保不触发工具调用
  │
  └─ 组装: system + "[Context summary: ...]" + keep
     若仍超 MaxTokens → TrimHistory 硬截断保底
```

### 3.6 瞬时错误重试与历史保护

```
Dispatch 返回错误
  │
  ├─ errors.Is(err, ErrToolUnavailable) → transient = true
  │   → 不注入 history
  │   → 等 2s, tools 刷新, 重试整轮 dispatch (最多 3 次)
  │   → 3 次耗尽 → 跳过该 tool
  │
  └─ 其他错误 (业务/参数/超时) → transient = false
      → 包装为 {"error":"..."} 注入 history
      → LLM 看到错误自行决策
```

---

## 4. 分层设计详解

### 4.1 ToolProvider —— 插件的核心契约

```go
// provider/tool_provider.go
type ToolProvider interface {
    ProviderName() string
    Tools() []Tool
    Dispatch(ctx context.Context, name, argsJSON string) (string, error)
    HasTool(name string) bool
}
```

设计要点：
- **Tools() 每次实时调用**，而非缓存——保证 MCP 热插拔、Hub 热更新
- **HasTool() 独立方法**——避免 Runtime 每次遍历完整工具列表
- **argsJSON 已是 JSON string**——调用方不需要关心参数 schema

### 4.2 Runtime —— 无状态的编排器

```go
// core/runtime.go:91-101
func (r *Runtime) tools() []types.Tool {
    r.mu.RLock()
    providers := r.providers  // 拷贝 slice header，快速释放锁
    r.mu.RUnlock()
    // 遍历聚合 ...
}
```

**注册顺序 = 路由优先级**：先注册的 Provider 先被 HasTool 匹配。

### 4.3 Agent —— 四段式防御体系

每一轮 LLM 调用前后有四层保护：

| 阶段 | 机制 | 位置 |
|------|------|------|
| **调用前** | 上下文压缩 (+相邻轮次去重) | core/agent.go + history/context_compress.go |
| **调用中** | dispatch 信号量 (max 5) | core/agent.go:217 |
| **调用后** | 瞬时错误重试 + 工具结果截断 | core/agent.go + history/context_limit.go |
| **保底** | 硬截断 + maxLoops 上限 | history/context_limit.go |

ContextConfig 可通过 Agent.SetContextConfig() 按会话调整：

```go
// history/context_limit.go:13-24
type ContextConfig struct {
    MaxTokens          int  // 硬上限，默认 8192
    CompressThreshold  int  // 压缩触发阈值，默认 6144 (75%)
    MaxToolResultChars int  // 单条工具结果最大字符数，默认 4000
}
```

### 4.4 ChatClient —— SSE 流式解析

状态机设计（`llm/chat_client.go`）：

```
sseState:
  tcMap       map[int]*ToolCall   // index → 累积中的 tool_call
  sb          strings.Builder     // 纯文本累积
  reasoningSB strings.Builder     // 思索文段累积
  isToolMode  bool                // 首个 tool_call 帧到达后锁定
```

关键规则：一旦 `isToolMode = true`，后续所有文本 delta 被忽略。

### 4.5 HubProvider —— 瞬时错误识别

```go
// provider/Hub_provider.go:22
var ErrToolUnavailable = errors.New("tool temporarily unavailable")

func allTransportErrors(results []hubbase.DispatchResult) bool {
    for _, r := range results {
        if r.Err == nil && len(r.Responses) > 0 {
            return false  // 工具正常响应了
        }
    }
    return true
}
```

### 4.6 MCPProvider —— 多 server 路由

- 单 server：工具名保持原样
- 多 server：`serverName__toolName` 格式，`__` 双下划线分隔
- `resolveRoute(name)` 根据 server 数量自动选择策略

### 4.7 WorkPlan —— 糖与执行分离

```
sugar.go:   公有方法 (Auto/If/Switch/Loop/Fork/...)  — 只构造 node 结构体
plan.go:    私有方法 (primitiveAuto/primitiveLoop/...) — 所有执行逻辑
node.go:    类型定义 (NodeKind/Signal/SwitchCase/...)
gate.go:    ApprovalGate 接口 + CLI 实现
```

构造函数零副作用，执行逻辑完全内聚。

### 4.8 Signal —— Loop 的活引用

```
构建期:   sig := wp.Loop("retry", "body", Until(...), MaxIter(5))
          sig.OnUpdate(func(jsonVal string) { ... })

执行期:   primitiveLoop 中每次迭代调用 sig.set(result, iter)
          → 触发所有 OnUpdate 回调
          → 最后 sig.close() 解除 Wait() 阻塞
```

---

## 5. 请求风暴与Token风暴防护

### 5.1 请求放大链路

```
WorkPlan.Run()
  │
  ├─ Pipeline(N 步)     N × maxLoops(4)    次 LLM 调用 (串行)
  ├─ Fork(M 分支)       M × maxLoops(4)    次 LLM 调用 (并发, 信号量限制)
  ├─ Loop(K 迭代)       K × maxLoops(4)    次 LLM 调用 (串行, 有退避)
  ├─ Approve            2 × maxLoops(4)    次 LLM 调用 (计划+执行)
  └─ Retry(R 重试)      R × maxLoops(4)    次 LLM 调用 (串行, 有退避)
```

**FullOpsOrchestration 最坏情况估算: ~120 次 LLM 调用/WorkPlan**

### 5.2 防护机制全览

| # | 机制 | 位置 | 作用 | 默认值 |
|---|------|------|------|--------|
| 1 | Agent maxLoops | `core/agent.go:99` | 单次 Chat 最大 tool_call 轮数 | 4 |
| 2 | dispatch 信号量 | `core/agent.go:217` | 限制单轮 tool_call 并发数 | 5 |
| 3 | Fork 分支信号量 | `workplan/plan.go:327` | 限制 Fork 并发 Agent 数 | 3 (可配置) |
| 4 | Loop 最大迭代 | `workplan/sugar.go:205` | 限制 Loop 最大循环次数 | 调用方指定 |
| 5 | Loop 迭代退避 | `workplan/plan.go:297-306` | 100ms×iter, 上限 2s | 必启用 |
| 6 | 全局 WorkPlan 限制 | `workplan/plan.go:28-36` | 限制同时运行的 WorkPlan 数 | 不限 (可配置) |
| 7 | WorkPlan 超时 | ops workflows | context.WithTimeout 兜底 | 10-20 min |
| 8 | 压缩去重 | `core/agent.go:99-111` | 相邻轮次不重复压缩 | 必启用 |
| 9 | Token 三级截断 | history/ | 压缩→截断→硬截断 | 6144/4000/8192 |

### 5.3 Token 风暴防护链

```
token 增长
  │
  ├─ Tool 结果截断  (MaxToolResultChars=4000)  ← 单条上限
  │
  ├─ 压缩触发  (CompressThreshold=6144)        ← LLM 智能摘要
  │    ├─ 最近 4 条消息保留
  │    ├─ 旧消息 → LLM 生成摘要
  │    └─ 压缩去重: 相邻轮次不重复触发
  │
  └─ 硬截断保底 (MaxTokens=8192)               ← 丢弃最旧非 system
       └─ 仍超限 → 截断最长单条消息内容
```

每一层都是上一层的降级保底，确保单 Agent 的上下文不会无限增长。

---

## 6. 设计模式与关键决策

| 模式 | 应用位置 | 说明 |
|------|---------|------|
| **Strategy** | ToolProvider 接口 | 不同工具源可插拔替换 |
| **Chain of Responsibility** | Runtime.dispatch() | 按注册顺序遍历 provider |
| **Template Method** | WorkPlan Run() | 固定执行框架，节点类型决定行为 |
| **Observer** | Signal.OnUpdate | Loop 进度实时推送 |
| **Builder** | sugar.go 链式 API | 声明式构建工作流 DAG |
| **Semaphore** | Fork + dispatch + Run | 三层信号量控制并发 |
| **Sentinel Error** | ErrToolUnavailable + errors.Is | 区分瞬时/持久错误 |
| **Retry with Backoff** | dispatch 重试 + Loop 退避 | 3 次重试(固定2s) + 迭代退避(递增) |
| **Lazy Init** | MCPProvider (ensureMCPProvider) | 不装 MCP 则零开销 |
| **Defense in Depth** | 压缩→截断→硬截断 | 三层上下文保护 |
| **Sugar/Primitive分离** | workplan/ | 公有 API 零逻辑，私有原语全逻辑 |
| **Options Pattern** | ContextConfig.Effective() | 零值→默认值，部分覆盖 |

---

## 7. 发现的问题

### 7.1 Bug

| # | 严重度 | 位置 | 问题 |
|---|--------|------|------|
| 1 | **中** | [Hub_provider.go](provider/Hub_provider.go) `Skills()` | Description 字段可能使用了 Method 而非 Description，导致 `/skills` 输出不正确 |
| 2 | **低** | [seele_api.go](sdk/api/seele_api.go) `MCPServers()` | 方法可能永远返回 nil，要么实现它（给 MCPProvider 加 ServerNames()），要么删除 |

### 7.2 设计问题

| # | 严重度 | 位置 | 问题 |
|---|--------|------|------|
| 3 | **中** | [agent.go](core/agent.go) `ChatStream` | tool_call 轮次也使用流式 `CompleteStream`，增加碎片泄露风险。当前通过 chunks 缓冲+确认后推送解决了此问题，但增加了内存开销 |
| 4 | **低** | [plan.go](workplan/plan.go) WorkPlan.Agent 接口 | 只有 `Chat()` 无 `ChatStream()`，无法做流式输出 |
| 5 | **低** | [mcp_provider.go](provider/mcp_provider.go) | `MCPServerConfig` 定义在 mcp_provider.go 而非 types/model.go，而 `LLMConfig` 在 model.go，不一致 |
| 6 | **低** | [harness.go](sdk/cluster/harness.go) + [runtime.go](core/runtime.go) | `HarnessConfig.MaxLoops` 默认值与 `runtime.go` Agent 默认值重复定义（两处均为 4），修改时易遗漏 |

### 7.3 上下文/请求风暴残余风险

| # | 风险 | 说明 |
|---|------|------|
| 7 | **Approve 双重 LLM 调用** | `primitiveApprove` 每次调用两次 agent.Chat()（计划+执行），是一次普通 Auto 节点的 2 倍 LLM 开销 |
| 8 | **token 估算公式 /3 对中文偏激进** | `EstimateTokens` 使用 `len(text)/3`，英文保守但中文 UTF-8 每字 3 字节 ≈ 1.5-2 token，可能低估 |
| 9 | **dispatch 瞬错重试仍为固定 2s** | 3 次重试用固定间隔，未采用指数退避。高频瞬错场景下可能加重后端负载 |

---

## 8. 架构亮点

- **ToolProvider 插件模式**：接口仅依赖标准库 + context，新增工具源零修改核心代码
- **provider 注册顺序 = 路由优先级**：简单直观，测试时可插入 mock provider 覆盖真实行为
- **热更新全链路**：`tools()` 每次实时聚合 → Attach/Detach/RefreshTools → Retire/Restore
- **瞬时错误不污染历史**：`ErrToolUnavailable` sentinel + 内层重试 + transient 标记跳过
- **WorkPlan 糖/原语分离**：声明式 API 的优雅与执行逻辑的内聚兼得
- **Fork 信号量 (可配置)**：限制并发分叉数量，防止下游工具被瞬间打垮
- **Signal 响应式机制**：Loop 进度外露而不侵入执行引擎
- **ApprovalGate 接口**：CLI 实现与 WebSocket 实现用同一接口，IO 细节完全解耦
- **四层请求风暴防护**：dispatch 信号量 + Fork 信号量 + Loop 退避 + 全局 WorkPlan 限制
- **三层 Token 防御**：压缩(LLM智能摘要+去重) → 截断(按换行截断保留语义) → 硬截断(保底)，逐级降级
- **ContextConfig 可配置**：MaxTokens/CompressThreshold/MaxToolResultChars 均可按 Agent/场景调整
- **Harness 框架化**：`sdk/cluster/` 从注册表裁剪到 gRPC 监听全部自动化，应用层只需注入 WorkflowMap

---

*本文档覆盖了 Seele 项目全部非 ops_tools/test 的 Go 源码，包含类图、数据流图、分层设计、风暴防护分析和问题清单。*
