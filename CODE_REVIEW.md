# Seele 框架代码审查

> 审查日期：2026-05-18 · 覆盖范围：除 `ops_tools/*` `test/*` 外的全部 Go 源码

---

## 目录

1. [架构总览](#1-架构总览)
2. [类图（类型关系全景）](#2-类图类型关系全景)
3. [数据流图](#3-数据流图)
4. [分层设计详解](#4-分层设计详解)
5. [设计模式与关键决策](#5-设计模式与关键决策)
6. [发现的问题](#6-发现的问题)
7. [架构亮点](#7-架构亮点)

---

## 1. 架构总览

Seele 是一个 Go 语言 AI Agent 框架，核心能力是 **LLM ReAct 循环驱动 + 多源工具接入 + 声明式工作流编排**。

### 1.1 分层结构

```
┌──────────────────────────────────────────────────────────────────┐
│  SDK 入口层                                                       │
│  sdk/cli/repl.go        交互式 REPL                               │
│  sdk/api/seele_api.go   Engine 组装工厂                           │
│  sdk/Cluster/           多 Agent 集群 Harness + Registry           │
├──────────────────────────────────────────────────────────────────┤
│  工作流编排层                                                     │
│  workplan/plan.go       私有原语执行引擎                           │
│  workplan/sugar.go      公有链式 API 语法糖                        │
│  workplan/node.go       节点/信号/结果类型定义                      │
│  workplan/gate.go       人工确认 IO 抽象                          │
├──────────────────────────────────────────────────────────────────┤
│  Agent 对话层                                                     │
│  agent.go               ReAct 循环 + 流式 + 上下文压缩              │
│  context_compress.go    LLM 驱动的上下文压缩                       │
│  context_limit.go       Token 估算 + 截断 + 硬截断                 │
├──────────────────────────────────────────────────────────────────┤
│  Runtime 编排层                                                   │
│  runtime.go             注册/路由/Agent 工厂                       │
│  tool_provider.go       ToolProvider 插件接口                      │
├──────────────────────────────────────────────────────────────────┤
│  Provider 实现层                                                  │
│  Hub_provider.go        microHub gRPC 工具源适配器                  │
│  mcp_provider.go        MCP 协议工具源适配器                        │
├──────────────────────────────────────────────────────────────────┤
│  基础设施层                                                       │
│  chat_client.go         OpenAI 兼容 HTTP 客户端 (同步+SSE流)        │
│  model.go               所有共享类型定义                            │
│  config.go              YAML 配置加载                              │
│  util/                  命令执行 + Viper YAML 读取                  │
└──────────────────────────────────────────────────────────────────┘
```

### 1.2 模块职责矩阵

| 模块 | 知道 LLM? | 知道 ToolProvider? | 知道 gRPC/MCP? | 职责 |
|------|----------|-------------------|----------------|------|
| `agent.go` | ✓ | ✗ | ✗ | 对话循环 + 历史管理 |
| `context_compress.go` | ✓ | ✗ | ✗ | LLM 驱动历史压缩 |
| `context_limit.go` | ✗ | ✗ | ✗ | 纯函数 token 估算 |
| `runtime.go` | ✓ | ✓ | ✗ | Provider 注册 + dispatch 路由 |
| `tool_provider.go` | ✗ | ✓ (接口定义) | ✗ | 抽象契约 |
| `Hub_provider.go` | ✗ | ✓ (实现) | ✓ | microHub gRPC 适配 |
| `mcp_provider.go` | ✗ | ✓ (实现) | ✓ | MCP 协议适配 |
| `workplan/*` | ✗ | ✗ | ✗ | 工作流编排 |
| `sdk/api/seele_api.go` | ✓ | ✓ | ✓ | 全局组装 |

---

## 2. 类图（类型关系全景）

```
                          ┌─────────────────────┐
                          │    ToolProvider      │  (tool_provider.go)
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
   │  (Hub_provider.go)   │              │  (mcp_provider.go)   │
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
   ┌──────────────────────┐             │     chatClient       │
   │      Runtime         │             │  (chat_client.go)    │
   │   (runtime.go)       │             ├──────────────────────┤
   ├──────────────────────┤  1:1       │ cfg     LLMConfig     │
   │ llm     *chatClient ─┼────────────▶│ client  *http.Client  │
   │ providers []ToolProv │             ├──────────────────────┤
   │ mu     sync.RWMutex  │             │ complete()   同步    │
   ├──────────────────────┤             │ completeStream()     │
   │ Register/Unregister()│             │ doStreamRequest()    │
   │ NewAgent() *Agent ───┼─┐           │ processFrame()       │
   │ tools() (私有)       │ │           │ buildToolCalls()     │
   │ dispatch() (私有)    │ │           └──────────────────────┘
   └──────────────────────┘ │
                            │ 创建
                            ▼
   ┌─────────────────────────────────────────────────┐
   │                   Agent                         │
   │                (agent.go)                       │
   ├─────────────────────────────────────────────────┤
   │ runtime    *Runtime                             │
   │ sessionID  string                               │
   │ history    []Message                            │
   │ maxLoops   int (默认 4)                          │
   ├─────────────────────────────────────────────────┤
   │ Chat(ctx, input) → (string, error)              │
   │ ChatStream(ctx, input, onChunk) → (string, err) │
   │ ClearHistory()                                  │
   │ ForceAppendHistory(msg)                         │
   │ SessionID() / History() / MaxLoops()            │
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
   │ vars      map        │
   ├──────────────────────┤
   │ Run() → Result       │
   │  └─ primitiveAuto()  │──── 调用 agent.Chat()
   │  └─ primitiveFork()  │──── 信号量限制并发=3
   │  └─ primitiveLoop()  │──── Signal 响应式回调
   │  └─ primitiveApprove()─── gate.Request()
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

消息类型 (model.go):
  Message ──────── role + content(*string) + tool_calls + tool_call_id + name + reasoning
    └─ ToolCall ── id + type + function
         └─ ToolCallFunction ── name + arguments(JSON string)
  Tool ─────────── type + function
    └─ ToolFunction ── name + description + parameters(map)

上下文控制 (context_limit.go + context_compress.go):
  EstimateTokens(text) → int           // (len+2)/3 保守估算
  EstimateHistoryTokens([]Message)     // 逐条求和
  TruncateToolResult(content) → string // 超 2000 字截断 + [truncated] 标记
  TrimHistory(msgs, max) → []Message   // 丢弃最旧非 system 消息
  NeedCompression([]Message) → bool    // > 1536 token 触发
  CompressHistory(ctx, client, msgs, max) → []Message  // LLM 摘要压缩
```

---

## 3. 数据流图

### 3.1 启动流程

```
main() / agent.Run()
  │
  ├─ registry.Init(path)              加载 registry.yaml → 内存工具表
  ├─ registry.ProbeAllOnStartup()     对所有工具地址健康检查
  ├─ registry.StartHealthProbe()      后台 15s 周期探测
  │
  ├─ hubbase.New(router)              创建 microHub
  ├─ hub.ServeAsync(addr)            启动 gRPC Server (后台)
  │
  ├─ LoadConfig(path)                读取 config.yaml → LLMConfig
  │    └─ os.ReadFile + yaml.Unmarshal(AppConfig)
  │
  ├─ NewRuntime(llmCfg)              创建 Runtime
  │    └─ newChatClient(llmCfg)      初始化 HTTP 客户端
  │
  ├─ NewHubProvider(hub, timeout)    创建 Hub 工具源
  ├─ rt.Register(hubProv)            注册到 Runtime
  │
  └─ [可选] AttachMCPServer(ctx, cfg)
       ├─ ensureMCPProvider()        延迟创建 MCPProvider
       └─ mcpProv.Attach(ctx, cfg)
            ├─ NewStdioMCPClient / NewSSEMCPClient
            ├─ client.Initialize()   MCP 握手
            ├─ client.ListTools()    拉取工具列表
            └─ 转为 Tool 格式存入 servers map
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
  ├─ 2. 上下文检查 (每轮循环前)
  │    ├─ NeedCompression(history)?
  │    │    ├─ YES → CompressHistory(ctx, llm, history, 2048)
  │    │    │         ├─ splitSystem()          分离 system + 非 system
  │    │    │         ├─ 保留最后 4 条 + 所有 system
  │    │    │         ├─ buildCompressInput()   序列化可压缩部分
  │    │    │         ├─ callCompressLLM()      调 LLM (nil tools, 300 tokens)
  │    │    │         └─ 组装: system + [摘要] + 保留消息
  │    │    └─ NO  → 继续
  │    └─ fallback: TrimHistory() 硬截断
  │
  ├─ 3. tools = rt.tools()         ← 每次实时聚合，支持热更新
  │    ├─ HubProvider.Tools()
  │    │    ├─ registry.GetOnlineTools()  获取在线工具
  │    │    ├─ 过滤 retired + offline
  │    │    └─ buildParameters()           schema 格式转换
  │    └─ MCPProvider.Tools()
  │         └─ 多 server 时加 "server__tool" 前缀
  │
  ├─ 4. msg = llm.complete(ctx, history, tools)
  │    │  POST /v1/chat/completions
  │    │  Body: { model, messages, tools, temperature, max_tokens }
  │    │
  │    ├─ 返回 tool_calls → dispatch 分支
  │    └─ 返回纯文本    → 结束循环
  │
  ├─ 5. 追加 assistant message → history
  │
  ├─ 6. [若有 tool_calls] 内层重试 dispatch (最多 3 次, 间隔 2s)
  │    │
  │    │  ┌─────────────────────────────────────────┐
  │    │  │  并发 dispatch 每个 tool_call             │
  │    │  │                                          │
  │    │  │  rt.dispatch(ctx, name, argsJSON)        │
  │    │  │    ├─ HubProvider.Dispatch()             │
  │    │  │    │    ├─ registry.SelectToolByName()   │
  │    │  │    │    ├─ 检查 retired / JSON valid     │
  │    │  │    │    ├─ pb_api.Request().Build()      │
  │    │  │    │    ├─ hub.Dispatch(ctx, req)        │
  │    │  │    │    │   → gRPC → tool 进程 → result  │
  │    │  │    │    └─ allTransportErrors()?         │
  │    │  │    │       ├─ YES → ErrToolUnavailable   │
  │    │  │    │       └─ NO  → business error/text  │
  │    │  │    │                                    │
  │    │  │    └─ MCPProvider.Dispatch()            │
  │    │  │         ├─ resolveRoute(name)           │
  │    │  │         ├─ parse args JSON              │
  │    │  │         ├─ client.CallTool(ctx, req)    │
  │    │  │         └─ extractMCPContent(result)    │
  │    │  │                                          │
  │    │  │  错误分类：                              │
  │    │  │    ErrToolUnavailable → 标记 transient   │
  │    │  │    其他 error         → 正常错误结果     │
  │    │  └─────────────────────────────────────────┘
  │    │
  │    │  有 transient → sleep 2s, 重试整轮 dispatch
  │    │  全成功       → 跳出重试
  │    │
  │    ▼
  │    追加 tool 消息 → history (每条经过 TruncateToolResult)
  │
  └─ 7. 回到步骤 2 (直到无 tool_calls 或达到 maxLoops)
       maxLoops 耗尽 → 返回 error
```

### 3.3 流式响应 (ChatStream)

与 Chat 核心逻辑相同，区别在 LLM 调用层：

```
Agent.ChatStream(ctx, input, onChunk)
  │
  ├─ tool_call 轮次 → llm.completeStream()  静默累积 tool_call
  │    └─ SSE 逐帧:
  │         data: {"delta":{"tool_calls":[...]}}  → 不推送, 累积到 tcMap
  │         data: {"delta":{"content":"..."}}     → onChunk(delta) 实时推送
  │         data: [DONE]                           → 流结束
  │
  └─ 最终文本轮次 → 同上, 有文本 delta 则实时推送
```

### 3.4 WorkPlan 执行流

```
WorkPlan.Run(ctx)
  │
  │  遍历节点链表:
  │
  ├─ Auto:      agent.Chat(ctx, input)              单 Agent, 完整 ReAct
  │
  ├─ Approve:   ① Agent 生成计划(不调工具)
  │             ② gate.Request() 阻塞等人确认
  │             ③ Agent 真正执行 或 跳过/终止
  │
  ├─ If:        cond(prevJSON) = true → trueID
  │                                 false → falseID
  ├─ Switch:    顺序匹配 cases → 跳转对应 nextID
  │
  ├─ Loop:      for iter < maxIter:
  │              ├─ primitiveAuto(bodyNode)
  │              ├─ sig.set(result, iter)  → 触发 OnUpdate 回调
  │              └─ until(result)? → break
  │
  ├─ Fork:      并发 N 个 agent.Chat()  (信号量限制 max=3)
  │             汇合: {"label1": result1, "label2": ...}
  │
  ├─ Checkpoint: 快照当前输出到 result.Checkpoints
  │
  └─ Emit:      当前值写入 wp.vars[key], 后续节点 {{.Vars.key}} 引用
```

### 3.5 上下文压缩的 LLM 往返

```
历史 > CompressThreshold (1536 tokens)
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
  │   └─ complete(ctx, messages, nil)  ← nil tools, 确保不触发工具调用
  │
  └─ 组装: system + "[Context summary: ...]" + keep
     若仍超限 → TrimHistory 硬截断保底
```

### 3.6 瞬时错误重试与历史保护

```
Dispatch 返回错误
  │
  ├─ errors.Is(err, ErrToolUnavailable) → transient = true
  │   → 不注入 history
  │   → 等 2s, tools 刷新, 重试整轮 dispatch (最多 3 次)
  │   → 3 次耗尽 → 跳过该 tool (不留痕迹)
  │
  └─ 其他错误 (业务/参数/超时) → transient = false
      → 包装为 `{"error":"..."}` 注入 history
      → LLM 看到错误自行决定后续行为
```

---

## 4. 分层设计详解

### 4.1 ToolProvider —— 插件的核心契约

```go
// tool_provider.go
type ToolProvider interface {
    ProviderName() string
    Tools() []Tool
    Dispatch(ctx context.Context, name, argsJSON string) (string, error)
    HasTool(name string) bool
}
```

设计要点：
- **Tools() 每次实时调用**，而非缓存——保证 MCP 热插拔、Hub 热更新立刻生效
- **HasTool() 独立方法**——避免 Runtime 每次 dispatch 时遍历完整工具列表
- **argsJSON 已经是 JSON string**——调用方不需要关心参数 schema，LLM 已经生成合法 JSON

### 4.2 Runtime —— 无状态的编排器

Runtime 自身不缓存工具列表，`tools()` 和 `dispatch()` 每次实时读取：

```go
func (r *Runtime) tools() []Tool {
    r.mu.RLock()
    providers := r.providers  // 拷贝 slice header，快速释放锁
    r.mu.RUnlock()
    // 遍历聚合 ...
}
```

**注册顺序 = 路由优先级**：先注册的 Provider 先被 HasTool 匹配。这允许用户覆盖行为（例如先注册一个 mock Provider）。

### 4.3 Agent —— 三段式防御体系

每一轮 LLM 调用前后有三层保护：

| 阶段 | 机制 | 文件 |
|------|------|------|
| **调用前** | 上下文压缩 (CompressHistory) | context_compress.go |
| **调用后** | 瞬时错误重试 + 工具结果截断 (TruncateToolResult) | agent.go + context_limit.go |
| **保底** | 硬截断 (TrimHistory) + maxLoops 上限 | context_limit.go |

maxLoops 默认值 = 4，在 `runtime.go:68` 和 `sdk/Cluster/harness.go:68` 两处统一。

### 4.4 chatClient —— SSE 流式解析

状态机设计：

```
sseState:
  tcMap       map[int]*ToolCall   // index → 累积中的 tool_call (arguments 拼接)
  sb          strings.Builder     // 纯文本累积
  reasoningSB strings.Builder     // 思索文段累积
  isToolMode  bool                // 首个 tool_call 帧到达后锁定
```

关键规则：一旦 `isToolMode = true`，后续所有文本 delta 被忽略——正常情况下一个回复中不会同时有文本和 tool_call。

`buildToolCalls` 按 index 排序，LLM 保证 index 从 0 连续递增。

### 4.5 HubProvider —— 瞬时错误识别

```go
var ErrToolUnavailable = errors.New("tool temporarily unavailable")

func allTransportErrors(results []hubbase.DispatchResult) bool {
    // 所有 result 都没有成功的 business response → 传输层问题
    for _, r := range results {
        if r.Err == nil && len(r.Responses) > 0 {
            return false  // 工具正常响应了
        }
    }
    return true
}
```

与 `errors.Is()` 配合：`fmt.Errorf("%w: ...", ErrToolUnavailable, ...)` 可以被上层检测。

### 4.6 MCPProvider —— 多 server 路由

命名策略：
- 单 server：工具名保持原样（对 LLM 提示更友好）
- 多 server：`serverName__toolName` 格式，用 `__` 双下划线分隔

`resolveRoute(name)` 根据 server 数量自动选择策略。

### 4.7 WorkPlan —— 糖与执行分离

```
sugar.go:   公有方法 (Auto/If/Switch/Loop/Fork/...)  — 只构造 node 结构体
plan.go:    私有方法 (primitiveAuto/primitiveLoop/...) — 所有执行逻辑
node.go:    类型定义 (NodeKind/Signal/SwitchCase/...)
gate.go:    ApprovalGate 接口 + CLI 实现
```

这是框架设计中最清晰的分层：构造函数零副作用，执行逻辑完全内聚。

### 4.8 Signal —— Loop 的活引用

```
构建期:   sig := wp.Loop("retry", "body", Until(...), MaxIter(5))
          sig.OnUpdate(func(jsonVal string) { ... })

执行期:   primitiveLoop 中每次迭代调用 sig.set(result, iter)
          → 触发所有 OnUpdate 回调 (同步, 在 loop goroutine 中)
          → 最后 sig.close() 解除 Wait() 阻塞
```

Signal 解决的核心问题：**外部如何在不侵入执行引擎的情况下感知 Loop 进度？** 答案是通过一个在构建期返回、执行期持续更新的活引用。

### 4.9 上下文控制 —— Token 预算管理

| 常量 | 值 | 含义 |
|------|----|------|
| `MaxContextTokens` | 2048 | LLM 上下文硬上限 |
| `CompressThreshold` | 1536 (75%) | 触发压缩的阈值 |
| `MaxToolResultChars` | 2000 | 单条工具结果最大字符数 |
| `keepRecent` | 4 | 压缩时始终保留的最近消息数 |

Token 估算公式：`(len(text) + 2) / 3`，对中英文都偏保守（高估），确保不超窗口。

压缩算法：system 全保留 + 最近 4 条非 system 保留 + 中间部分 LLM 摘要。

---

## 5. 设计模式与关键决策

| 模式 | 应用位置 | 说明 |
|------|---------|------|
| **Strategy** | ToolProvider 接口 | 不同工具源可插拔替换 |
| **Chain of Responsibility** | Runtime.dispatch() | 按注册顺序遍历 provider 找匹配 |
| **Template Method** | WorkPlan Run() | 固定执行框架，节点类型决定具体行为 |
| **Observer** | Signal.OnUpdate | Loop 进度实时推送 |
| **Builder** | sugar.go 链式 API | 声明式构建工作流 DAG |
| **Semaphore** | Fork (maxConcurrentFork=3) | 限制并发分支数 |
| **Sentinel Error** | ErrToolUnavailable + errors.Is | 区分瞬时/持久错误 |
| **Retry with Backoff** | agent.go 内层重试 | 3次重试 + 2s 固定间隔 |
| **Lazy Init** | MCPProvider (ensureMCPProvider) | 不装 MCP 则不开销 |
| **Defense in Depth** | 压缩→截断→硬截断 | 三层上下文保护 |
| **Sugar/Primitive分离** | workplan/ | 公有 API 零逻辑，私有原语全逻辑 |

---

## 6. 发现的问题

### 6.1 Bug

| # | 严重度 | 位置 | 问题 |
|---|--------|------|------|
| 1 | **中** | [Hub_provider.go:198](Hub_provider.go#L198) | `Skills()` 中 `Description: t.Method` 应为 `t.Description`。当前返回的 SkillInfo.Description 字段错误地使用了 Method 值，导致 `/skills` 命令输出不正确 |
| 2 | **中** | [seele_api.go:220-227](sdk/api/seele_api.go#L220-L227) | `MCPServers()` 永远返回 `nil`，代码注释写"实际使用时可扩展"。要么实现它（给 MCPProvider 加 ServerNames()），要么删除此方法并修改调用方 |

### 6.2 设计问题

| # | 严重度 | 位置 | 问题 |
|---|--------|------|------|
| 3 | **中** | [agent.go:210-218](agent.go#L210-L218) | `ChatStream` 对所有轮次使用 `completeStream`。tool_call 轮次不应流式输出——此时 LLM 的 content 可能为空，onChunk 没有内容可推；更关键的是，如果把 tool_call JSON 碎片意外当文本推送给用户，体验会很差。建议：tool_call 轮次用非流式 `complete`，仅最终文本轮次用 `completeStream` |
| 4 | **中** | [agent.go](agent.go) | `Chat` 和 `ChatStream` 的 dispatch 逻辑完全相同（~30 行），应提取为 `dispatchToolCalls` 私有方法来消除重复 |
| 5 | **低** | [workplan/sugar.go:346-357](workplan/sugar.go#L346-L357) | `stringContains` 自行实现子串查找，注释说"避免直接依赖 strings 包"，理由不成立——标准库可靠且更快 |
| 6 | **低** | [mcp_provider.go:16-36](mcp_provider.go#L16-L36) | `MCPServerConfig` 定义在 mcp_provider.go 而非 model.go，而 `LLMConfig` 在 model.go，不一致 |
| 7 | **低** | [workplan/plan.go:14-18](workplan/plan.go#L14-L18) | WorkPlan 的 `Agent` 接口只有 `Chat()` 无 `ChatStream()`，无法做流式输出 |
| 8 | **低** | [sdk/Cluster/harness.go:68](sdk/Cluster/harness.go#L68) | `HarnessConfig.MaxLoops` 默认值与 `runtime.go:68` 的 Agent 默认值重复定义（两处均为 4）。未来修改时容易遗忘一处 |

### 6.3 上下文控制建议

| # | 建议 |
|---|------|
| 9 | `MaxContextTokens = 2048` 偏小。当前主流模型支持 4K-128K，建议提升至 8192 或通过配置调整 |
| 10 | `CompressHistory` 中的 `keepRecent = 4` 是经验值，未经验证。对于某些需要连续多轮工具调用的场景可能不够 |

---

## 7. 架构亮点

- **ToolProvider 插件模式**：接口仅依赖标准库 + context，新增工具源零修改核心代码
- **provider 注册顺序 = 路由优先级**：简单直观，测试时可插入 mock provider 覆盖真实行为
- **热更新全链路**：`tools()` 每次实时聚合 → Attach/Detach/RefreshTools → Retire/Restore
- **瞬时错误不污染历史**：`ErrToolUnavailable` sentinel + 内层重试 + transient 标记跳过，防止 retry storm
- **WorkPlan 糖/原语分离**：声明式 API 的优雅与执行逻辑的内聚兼得
- **Fork 信号量**：限制并发分叉数量，防止下游工具被瞬间打垮
- **Signal 响应式机制**：Loop 进度外露而不侵入执行引擎
- **ApprovalGate 接口**：CLI 实现与 WebSocket 实现用同一接口，IO 细节完全解耦
- **三层上下文防御**：压缩(LLM智能摘要) → 截断(按换行截断保留语义) → 硬截断(保底)，逐级降级
- **Harness 框架化**：`sdk/Cluster/` 从注册表裁剪到 gRPC 监听全部自动化，应用层只需注入 WorkflowMap

---

*本文档覆盖了 Seele 项目全部非 ops_tools/test 的 Go 源码，包含类图、数据流图、分层设计和问题清单。*
