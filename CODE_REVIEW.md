# Seele 代码评审：数据流与函数关系分析

> 评审日期：2026-05-16

---

## 目录

1. [项目概览](#1-项目概览)
2. [整体架构分层](#2-整体架构分层)
3. [核心数据流图](#3-核心数据流图)
4. [类型体系](#4-类型体系)
5. [函数调用关系图](#5-函数调用关系图)
6. [逐文件分析](#6-逐文件分析)
7. [WorkPlan 子包分析](#7-workplan-子包分析)
8. [发现的问题与建议](#8-发现的问题与建议)

---

## 1. 项目概览

**Seele** 是一个 Go 语言编写的可扩展 AI Agent 框架。核心能力：

- 通过 **ToolProvider 插件接口** 统一管理工具来源（microHub gRPC 技能 + MCP 协议服务器）
- **Agent** 驱动 LLM ReAct 循环（推理 → 工具调用 → 推理），支持同步和 SSE 流式两种模式
- **WorkPlan** 子包在 Agent 之上提供工作流编排（条件分支、循环、并发 Fork、人工确认）

依赖链：`cmd/main.go` → `sdk/api` (Engine) → `Runtime` → `Agent` + `ToolProvider`

---

## 2. 整体架构分层

```
┌──────────────────────────────────────────────────────────┐
│  cmd/main.go          入口：创建 Engine，启动 REPL         │
├──────────────────────────────────────────────────────────┤
│  sdk/cli/repl.go      REPL 交互循环                       │
├──────────────────────────────────────────────────────────┤
│  sdk/api/seele_api.go  Engine 组装层                      │
│                        · 初始化 microHub + registry       │
│                        · 创建 HubProvider / MCPProvider   │
│                        · 暴露 Agent / Skill / MCP 管理    │
├──────────────────────────────────────────────────────────┤
│  runtime.go            Runtime 编排层                     │
│                        · 管理 []ToolProvider              │
│                        · 聚合 tools()                     │
│                        · 路由 dispatch()                  │
│                        · Agent 工厂 NewAgent()            │
├──────────────────┬───────────────────────────────────────┤
│  agent.go        │  tool_provider.go  接口定义            │
│  · Chat()        │  Hub_provider.go   microHub 实现       │
│  · ChatStream()  │  mcp_provider.go   MCP 实现            │
├──────────────────┴───────────────────────────────────────┤
│  chat_client.go     HTTP 客户端 (OpenAI 兼容)             │
│  model.go           所有数据类型                           │
│  config.go          YAML 配置加载                         │
└──────────────────────────────────────────────────────────┘

workplan/   ← 独立的 workflow 子包，在 Agent 之上做编排
```

---

## 3. 核心数据流图

### 3.1 启动流程

```
main()
  │
  ├─ api.New(Options)
  │    ├─ registry.Init(path)           // 加载 registry.yaml 技能列表
  │    ├─ registry.ProbeAllOnStartup()  // 健康检查所有技能
  │    ├─ registry.StartHealthProbe()   // 后台定期探测
  │    ├─ hub.ServeAsync(addr)          // 启动 gRPC Hub 服务
  │    ├─ runtime.LoadConfig(path)      // 加载 LLM 配置
  │    ├─ runtime.NewRuntime(llmCfg)    // 创建 Runtime + chatClient
  │    ├─ runtime.NewHubProvider(hub)   // 创建 Hub 工具提供者
  │    └─ rt.Register(hubProv)          // 注册到 Runtime
  │
  └─ cli.RunREPL(ctx, opts)
       └─ engine.NewAgent(prompt)       // 创建 Agent
            └─ rt.NewAgent(prompt, 8)   // Runtime 工厂方法
```

### 3.2 对话请求数据流（核心路径）

```
用户输入 "帮我 ping 一下 127.0.0.1"
  │
  ▼
Agent.Chat(ctx, input)
  │
  ├─ 1. 追加 user message 到 history
  │
  ├─ 2. rt.tools()  ←────────────────────────────────────┐
  │    ├─ HubProvider.Tools() → registry.GetOnlineTools() │
  │    └─ MCPProvider.Tools() → 各 server 缓存工具列表     │
  │                                                       │
  ├─ 3. llm.complete(ctx, history, tools)  ───────────────┤
  │    │  POST /v1/chat/completions                       │
  │    │  Body: { model, messages, tools }                │
  │    ▼                                                  │
  │    LLM 返回: { role: "assistant", tool_calls: [...] } │
  │                                                       │
  ├─ 4. 追加 assistant message (含 tool_calls) 到 history  │
  │                                                       │
  ├─ 5. 并发 dispatch 每个 tool_call                       │
  │    │                                                  │
  │    ├─ rt.dispatch(ctx, "ping", `{"host":"127.0.0.1"}`)
  │    │    ├─ HubProvider.HasTool("ping") → false        │
  │    │    └─ MCPProvider.HasTool("ping") → ...          │
  │    │         └─ mcpClient.CallTool(ctx, req)          │
  │    │              └─ 返回 `{"alive":true,"latency":1}`│
  │    │                                                  │
  │    └─ 每个 tool 结果追加为 role:"tool" 消息            │
  │                                                       │
  ├─ 6. 回到步骤 2 (tools 实时刷新，支持热更新)  ─────────┘
  │
  └─ 7. LLM 返回纯文本 → 返回给用户
```

### 3.3 流式响应的数据流

```
Agent.ChatStream(ctx, input, onChunk)
  │
  │  每轮循环（和 Chat 结构相同，区别在 LLM 调用方式）:
  │
  ├─ llm.completeStream(ctx, history, tools, onChunk)
  │    │
  │    ├─ 1. POST /v1/chat/completions (stream: true)
  │    │
  │    ├─ 2. 逐行读取 SSE:
  │    │    ├─ "data: {...delta.content...}"  → onChunk(delta) 实时推送
  │    │    └─ "data: {...delta.tool_calls...}" → 静默累积进 tcMap
  │    │
  │    └─ 3. 返回 (完整文本, reasoning, toolCalls, err)
  │
  │  tool_calls 非空 → dispatch → 下一轮循环
  │  tool_calls 为空 → 返回最终文本
```

### 3.4 MCP Server 接入流程

```
Engine.AttachMCPServer(ctx, MCPServerConfig{
    Name: "filesystem", Transport: "stdio",
    Command: "npx", Args: ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"],
})
  │
  ├─ ensureMCPProvider()     // 首次调用时延迟创建
  │    └─ NewMCPProvider() → rt.Register(mcpProv)
  │
  └─ mcpProv.Attach(ctx, cfg)
       ├─ NewStdioMCPClient(cmd, env, args...)   // 启动子进程
       ├─ client.Initialize(ctx, initReq)         // MCP 握手
       ├─ client.ListTools(ctx, ...)              // 拉取工具列表
       │    └─ JSON Marshal/Unmarshal 转换 Schema
       └─ 存入 servers map → Tools() 立即可见
```

### 3.5 工具调用的路由分发

```
Runtime.dispatch(ctx, name, argsJSON)
  │
  │  按注册顺序遍历 providers，找到第一个 HasTool(name) 的:
  │
  ├─ HubProvider.Dispatch()
  │    ├─ registry.SelectToolByName(name)  // 查 registry
  │    ├─ 检查是否 retired
  │    ├─ pb_api.Request().Method().Params().Build()  // 构建 gRPC 请求
  │    ├─ hub.Dispatch(ctx, req)                      // gRPC 调用
  │    └─ 解析结果 (ok/partial/error)
  │
  └─ MCPProvider.Dispatch()
       ├─ resolveRoute(name)  // 多 server 时解析 server__tool 前缀
       ├─ json.Unmarshal(argsJSON) → map
       ├─ mcp.CallToolRequest{ Params: { Name, Arguments } }
       ├─ client.CallTool(ctx, req)
       └─ extractMCPContent(result)  // 合并多 content block
```

---

## 4. 类型体系

```
                    ┌──────────────┐
                    │ ToolProvider │  (接口 - tool_provider.go)
                    │ 接口         │
                    ├──────────────┤
                    │ ProviderName │
                    │ Tools()      │
                    │ HasTool()    │
                    │ Dispatch()   │
                    └──┬───────┬──┘
           ┌───────────┘       └───────────┐
           ▼                               ▼
    ┌──────────────┐              ┌──────────────┐
    │ HubProvider  │              │ MCPProvider  │
    │ (microHub)   │              │ (MCP 协议)    │
    ├──────────────┤              ├──────────────┤
    │ hub          │              │ servers map  │
    │ retired set  │              │ mu           │
    │ toolIndex    │              └──────────────┘
    │ Skills()     │
    │ Retire()     │
    │ Restore()    │
    └──────────────┘

                    ┌──────────────┐
                    │   Runtime    │  (编排层 - runtime.go)
                    ├──────────────┤
                    │ llm          │──→ chatClient
                    │ providers[]  │──→ []ToolProvider
                    │ mu           │
                    ├──────────────┤
                    │ Register()   │
                    │ Unregister() │
                    │ NewAgent()   │
                    │ tools()      │  (私有)
                    │ dispatch()   │  (私有)
                    └──────┬───────┘
                           │ 创建
                           ▼
                    ┌──────────────┐
                    │    Agent     │  (对话层 - agent.go)
                    ├──────────────┤
                    │ runtime      │──→ *Runtime
                    │ sessionID    │
                    │ history[]    │──→ []Message
                    │ maxLoops     │
                    ├──────────────┤
                    │ Chat()       │
                    │ ChatStream() │
                    │ ClearHistory│
                    │ SessionID()  │
                    │ History()    │
                    └──────────────┘

消息类型 (model.go):
  Message        ← role + content + tool_calls + tool_call_id + name
  ToolCall       ← id + type + function
  ToolCallFunction ← name + arguments (JSON string)
  Tool           ← type + function
  ToolFunction   ← name + description + parameters (JSON Schema map)

配置类型 (model.go):
  AppConfig      ← LLM + Hub + Registry
  LLMConfig      ← BaseURL / APIKey / Model / MaxTokens / Timeout / Temperature
  MCPServerConfig ← Name + Transport + Command/Args/Env + URL
```

---

## 5. 函数调用关系图

### 5.1 核心调用链

```
main()
  └─ api.New()
       ├─ registry.Init()
       ├─ registry.ProbeAllOnStartup()
       ├─ registry.StartHealthProbe()
       ├─ hubbase.New()
       ├─ hub.ServeAsync()
       ├─ runtime.LoadConfig()      ← config.go:LoadConfig()
       │    └─ os.ReadFile + yaml.Unmarshal
       ├─ runtime.NewRuntime()      ← runtime.go
       │    └─ newChatClient()      ← chat_client.go
       └─ runtime.NewHubProvider()  ← Hub_provider.go
            └─ rt.Register()

RunREPL()                           ← sdk/cli/repl.go
  └─ agent.Chat() / agent.ChatStream()
       ├─ rt.tools()
       │    ├─ HubProvider.Tools()
       │    │    ├─ registry.GetOnlineTools()
       │    │    ├─ buildParameters()      ← 微Hub Schema → OpenAI Schema
       │    │    └─ schemaNodeToOpenAI()   ← 递归转换
       │    └─ MCPProvider.Tools()
       │         └─ 多 server 时加前缀
       │
       ├─ llm.complete() / llm.completeStream()
       │    ├─ doStreamRequest()    ← 建立 SSE 连接
       │    ├─ processFrame()       ← 解析每个 SSE data 帧
       │    │    └─ 文本帧 → onChunk / tool_call 帧 → 累积 tcMap
       │    └─ buildToolCalls()     ← tcMap → []ToolCall
       │
       └─ rt.dispatch()
            ├─ HubProvider.Dispatch()
            │    ├─ registry.SelectToolByName()
            │    ├─ pb_api.Request().Build()
            │    └─ hub.Dispatch()
            └─ MCPProvider.Dispatch()
                 ├─ resolveRoute()  ← 解析 server__tool 前缀
                 ├─ client.CallTool()
                 └─ extractMCPContent()
```

### 5.2 WorkPlan 执行调用链

```
WorkPlan.Run(ctx)
  │
  │  遍历节点链表:
  │
  ├─ primitiveRunNode(ctx, n, prevJSON, result)
  │    ├─ kindAuto → primitiveAuto()
  │    │    └─ primitiveNewAgent(n) → factory.NewAgent(prompt)
  │    │         └─ agent.Chat(ctx, input)    ← 完整 ReAct 循环
  │    │
  │    ├─ kindApprove → primitiveApprove()
  │    │    ├─ planAgent.Chat("请分析...不要实际执行")  ← 阶段1: 生成计划
  │    │    ├─ gate.Request(ctx, req)                 ← 阶段2: 等人确认
  │    │    └─ execAgent.Chat(ctx, input)             ← 阶段3: 真正执行
  │    │
  │    ├─ kindIf / kindSwitch → 透传 prevJSON (不做 Agent)
  │    │
  │    ├─ kindLoop → primitiveLoop()
  │    │    ├─ for iter < maxIter:
  │    │    │    ├─ primitiveAuto(bodyNode)  ← 执行循环体
  │    │    │    ├─ signal.set(result, iter) ← 触发 OnUpdate 回调
  │    │    │    ├─ until(result) → break
  │    │    │    └─ iter >= maxIter → break
  │    │    └─ signal.close() → 解除 Wait() 阻塞
  │    │
  │    ├─ kindFork → primitiveFork()
  │    │    ├─ 并发启动多个 goroutine，每个独立 agent.Chat()
  │    │    ├─ wg.Wait() 等待全部完成
  │    │    └─ json.Marshal({"label": result, ...}) 汇合
  │    │
  │    ├─ kindCheckpoint → 保存快照到 result.Checkpoints
  │    └─ kindEmit → primitiveEmit() → 写入 wp.vars[key]
  │
  └─ primitiveNext(n, prevJSON)
       ├─ kindIf → 评估 ifCond → 返回 trueID/falseID
       ├─ kindSwitch → 顺序匹配 case → 返回对应 nextID
       ├─ kindLoop → 检查 exhausted → 返回 exhaustedID/next
       └─ default → 返回 n.next
```

---

## 6. 逐文件分析

### 6.1 `model.go` — 数据类型

| 类型 | 用途 | 关键字段 |
|------|------|---------|
| `Message` | 对话历史单条记录 | Role, Content(*string), ToolCalls, ToolCallID, Name, ReasoningContent |
| `ToolCall` | LLM 发起的函数调用 | ID, Type("function"), Function |
| `ToolCallFunction` | 工具名和参数 | Name, Arguments(JSON字符串) |
| `Tool` | OpenAI function calling 工具描述 | Type, Function(ToolFunction) |
| `ToolFunction` | 工具函数描述 | Name, Description, Parameters(JSON Schema map) |
| `SkillInfo` | 技能摘要(对外展示) | Name, Description, Method, Addr |

**设计注意**：`Message.Content` 为 `*string`，允许区分"空字符串"和"无内容"（JSON omitempty）。

### 6.2 `tool_provider.go` — 插件接口

```go
type ToolProvider interface {
    ProviderName() string
    Tools() []Tool
    Dispatch(ctx context.Context, name, argsJSON string) (string, error)
    HasTool(name string) bool
}
```

这是整个框架的核心抽象。每个 Provider 负责：
- **声明自己能提供哪些工具**（`Tools()`，每次 LLM 请求前调用，保证热更新）
- **执行工具调用**（`Dispatch()`，入参已经是 JSON 字符串）
- **路由判断**（`HasTool()`，避免遍历完整 Tools 列表）

### 6.3 `runtime.go` — 编排中枢

| 函数 | 可见性 | 职责 |
|------|--------|------|
| `NewRuntime(llmCfg)` | 公开 | 创建 Runtime，初始化 chatClient |
| `Register(p)` | 公开 | 注册 ToolProvider（追加到列表，先注册先匹配） |
| `Unregister(name)` | 公开 | 按名称移除 provider（全部同名移除） |
| `NewAgent(prompt, loops)` | 公开 | Agent 工厂，生成唯一 sessionID，注入 system prompt |
| `tools()` | 私有 | 聚合所有 provider.Tools()，每次实时读取 |
| `dispatch()` | 私有 | 按注册顺序找到第一个 HasTool 的 provider 并执行 |

**数据流要点**：
- `providers` 切片通过 `sync.RWMutex` 保护，并发安全
- `tools()` 和 `dispatch()` 每次都拷贝切片引用（`providers := r.providers`），避免长时间持锁
- 先注册的 provider 有更高的 dispatch 路由优先级

### 6.4 `agent.go` — 对话引擎

| 函数 | 职责 |
|------|------|
| `Chat(ctx, input)` | 同步 ReAct 循环：LLM 推理 → tool dispatch → 再推理，直到返回文本或超限 |
| `ChatStream(ctx, input, onChunk)` | 流式版本：文本 token 实时推送，tool_call 内部静默处理 |
| `ClearHistory()` | 清空历史但保留 system 消息 |
| `SessionID()` / `History()` | 状态查询 |

**Agent 循环流程（Chat）**：

```
for loop := 0; loop < maxLoops; loop++ {
    1. tools = rt.tools()              // 实时拉取，支持热更新
    2. msg = llm.complete(history, tools)  // 同步 HTTP 调用
    3. history.append(assistant msg)
    4. if no tool_calls → return content   // 纯文本，结束
    5. 并发 dispatch 所有 tool_calls       // goroutine + WaitGroup
    6. 每个 tool 结果追加为 tool 消息到 history
}
```

### 6.5 `chat_client.go` — HTTP 客户端

| 函数 | 职责 |
|------|------|
| `newChatClient(cfg)` | 创建客户端，设置超时 |
| `complete(ctx, messages, tools)` | 同步 POST，返回完整 Message |
| `doStreamRequest(ctx, messages, tools)` | 建立 SSE 流连接，返回 body |
| `processFrame(payload, state, onChunk)` | 解析单个 SSE data 帧，分流文本/tool_call |
| `buildToolCalls(tcMap)` | 将 map 整理为有序 []ToolCall |
| `completeStream(ctx, messages, tools, onChunk)` | 流式入口：建立连接 → 逐行 SSE → 返回 |

**SSE 流解析细节**：

```
sseState:
  tcMap       map[int]*ToolCall  // index → 累积的 toolCall（arguments 碎片拼接）
  sb          strings.Builder    // 纯文本累积
  reasoningSB strings.Builder    // 思索文段累积
  isToolMode  bool               // 收到第一个 tool_call 帧后锁定为 true，不可逆
```

`processFrame` 的逻辑分流：
- `delta.ToolCalls` 非空 → 设置 `isToolMode = true`，按 index 累积到 tcMap
- `!isToolMode && delta.Content != ""` → 追加到 sb，调用 onChunk 实时推送
- `delta.ReasoningContent != ""` → 追加到 reasoningSB

### 6.6 `Hub_provider.go` — microHub 适配器

| 函数 | 职责 |
|------|------|
| `Tools()` | 从 registry 拉在线工具 → 过滤 retired → 过滤 offline → 转换 Schema 格式 |
| `HasTool(name)` | 查 toolIndex 缓存（Tools() 时更新） |
| `Dispatch(ctx, name, argsJSON)` | registry 查找 → 检查 retired → JSON 校验 → 构建 gRPC 请求 → hub.Dispatch |
| `Skills()` | 返回人类可读的技能摘要 |
| `Retire(name)` / `Restore(name)` | 运行时禁用/恢复 Hub 技能 |
| `buildParameters(inputSchema)` | 将 microHub JSON Schema 转为 OpenAI 格式 |
| `schemaNodeToOpenAI(node)` | 递归转换 Schema 节点 |

**Dispatch 结果处理**：多个 gRPC 响应，区分 `ok/partial/error`，错误信息拼接到 errs，成功结果用 `\n` 拼接。

### 6.7 `mcp_provider.go` — MCP 协议适配器

| 函数 | 职责 |
|------|------|
| `Attach(ctx, cfg)` | 创建 MCP 客户端 → 握手 → 拉取工具列表 → 存入 servers map |
| `Detach(name)` | 断开连接，删除 server |
| `RefreshTools(ctx, serverName)` | 重新拉取工具列表（热更新） |
| `Tools()` | 汇总所有 server 工具，多 server 时加 `serverName__` 前缀 |
| `HasTool(name)` | 查 toolSet，处理多 server 前缀 |
| `Dispatch(ctx, name, argsJSON)` | 解析路由 → 查找 server → 调用 CallTool → 提取文本结果 |
| `resolveRoute(name)` | 根据是否多 server 决定行为 |
| `fetchTools(ctx, client)` | ListTools → JSON 序列化转换 Schema |
| `extractMCPContent(result)` | 合并多 content block（Text/Image/Audio） |

**工具命名策略**：
- 单 server：保持原始工具名，LLM 提示更简洁
- 多 server：`serverName__toolName` 格式防冲突，用 `__` 双下划线作为分隔符

---

## 7. WorkPlan 子包分析

### 7.1 设计思路

WorkPlan 是 Agent 之上的**声明式工作流编排层**，用链式 API 构建 DAG（有向无环图），然后用 `Run()` 解释执行。分为三层：

| 层 | 文件 | 职责 |
|----|------|------|
| **公有语法糖层** | `sugar.go` | 链式 API（Auto/If/Switch/Loop/Fork/Pipeline/Retry），只构造 node，不含执行逻辑 |
| **私有原语层** | `plan.go` | 所有执行逻辑（primitiveAuto/primitiveLoop/primitiveFork...），外部不可见 |
| **类型定义层** | `node.go` | NodeKind、Signal、SwitchCase、ForkBranch、NodeResult、WorkPlanResult 等类型 |
| **人工确认层** | `gate.go` | ApprovalGate 接口 + CLIApprovalGate 实现 |

### 7.2 节点类型与数据流

```
┌─────────┐     ┌──────────┐     ┌─────────┐     ┌──────────┐
│  Auto   │     │ Approve  │     │   If    │     │  Switch  │
│  ReAct  │     │ 人工确认  │     │ 二选一   │     │ 多路分支  │
│  循环   │     │ 两阶段   │     │ 路由    │     │  路由    │
└────┬────┘     └────┬─────┘     └────┬────┘     └────┬─────┘
     │               │               │               │
     ▼               ▼               ▼               ▼
  输出 JSON      输出/跳过/终止    跳转 trueID      跳转匹配 case
     │               │            / falseID          的 NextID
     └───────────────┴───────────────┬───────────────────┘
                                     │
    ┌─────────┐    ┌─────────┐    ┌──┴──────┐    ┌─────────┐
    │  Loop   │    │  Fork   │    │Checkpoint│    │  Emit   │
    │  循环   │    │  并发   │    │  快照   │    │ 命名变量 │
    │ Signal  │    │ Agent   │    │  保存   │    │  写入   │
    └────┬────┘    └────┬────┘    └────┬─────┘    └────┬────┘
         │              │              │               │
         ▼              ▼              ▼               ▼
    Signal.Set()   JSON Object    存入 result.    写入 wp.vars
    触发回调       汇合结果       Checkpoints        map
```

### 7.3 节点间数据传递

```
节点 A (Auto)  → 输出 JSON string
                    │
                    ▼
              prevJSON  ← 被下一个节点的 input 模板引用
                    │
                    ▼
节点 B input = "分析日志：{{.PrevResult}}"
  │
  ├─ {{.PrevResult}} → 替换为上一节点的 fromJSON(prevJSON)
  │   (JSON string 自动去引号，object/array 保持原样)
  │
  └─ {{.Vars.key}} → 替换为 wp.vars[key] 的 fromJSON 值
      (由之前的 Emit 节点写入)
```

### 7.4 Signal — Loop 的响应式机制

```
Signal 是 Loop 节点返回的"活引用":

  构建期:
    sig := wp.Loop("retry", "body", ...)
    sig.OnUpdate(func(jsonVal string) {
        log.Println("迭代结果:", jsonVal)
    })

  执行期 (primitiveLoop):
    for iter := 0; ...; iter++ {
        out = primitiveAuto(bodyNode, input)
        sig.set(out, iter+1)  // ← 触发所有 OnUpdate 回调
        if until(out) { break }
    }
    sig.close()  // ← 解除 Wait() 阻塞

  外部监听:
    sig.Wait()     // 阻塞直到 Loop 结束
    sig.Get()      // 随时读取当前值，无阻塞
    sig.GetString()// JSON string 去引号的纯文本版本
```

---

## 8. 发现的问题与建议

### 8.1 Bug / 潜在缺陷

| # | 严重度 | 位置 | 问题 |
|---|--------|------|------|
| 1 | **中** | [Hub_provider.go:189](Hub_provider.go#L189) | `Skills()` 中 `Description: t.Method` 应为 `t.Description`。当前返回的 SkillInfo 的 Description 字段错误地使用了 Method 值 |
| 2 | **高** | [test/unit_test.go](test/unit_test.go) + [test/benchmark_test.go](test/benchmark_test.go) | 整个 test 包编译失败（`go test ./test/` 无法通过）。根本原因是重构时将 `NewRuntime(llmCfg, hub, timeout)` 改为 `NewRuntime(llmCfg)`，并将 `Skills()`/`Retire()`/`Restore()` 从 Runtime 移到 HubProvider，但测试代码未同步更新。涉及的编译错误包括：`too many arguments in call to runtime.NewRuntime`、`f.Skills undefined`、`f.Retire undefined`、`f.Restore undefined`、`string` vs `*string` 类型不匹配等 |
| 2.1 | **中** | [test/unit_test.go:70](test/unit_test.go#L70) | `Message.Content` 是 `*string` 类型，但测试代码中多处使用 `string` 直接赋值（如 `Content: "hello"`），编译失败 |
| 3 | **低** | [seele_api.go:226](seele_api.go#L226) | `MCPServers()` 永远返回 `nil`，方法体内有注释 `// 实际使用时可扩展`。要么实现它（在 MCPProvider 上加 ServerNames()），要么删除此方法 |

### 8.2 设计改进建议

| # | 严重度 | 位置 | 建议 |
|---|--------|------|------|
| 4 | **中** | [agent.go:151-158](agent.go#L151-L158) | `ChatStream` 对所有轮次都使用 `completeStream`（流式）。但 tool_call 轮次不应该流式输出——LLM 返回 tool_call 时的 content 可能为空，把 onChunk 传入但没有内容可推，还可能把 tool_call 的 JSON 碎片意外当文本推给用户。建议：tool_call 轮次用非流式 `complete`，只有最终文本轮次才用 `completeStream` |
| 5 | **低** | [agent.go:108-113](agent.go#L108-L113) | 日志消息中硬编码了源文件行号（如 `[agent.go_Chat_line107]`），代码重构行号变化后日志信息会误导排查。建议使用运行时调用栈或移除行号 |
| 6 | **低** | [workplan/sugar.go:346-357](workplan/sugar.go#L346-L357) | `stringContains` 自行实现子串查找，注释说"避免直接依赖 strings 包"，但这是不必要的——标准库 `strings.Contains` 更可靠、更快，且不会增加任何依赖成本 |
| 7 | **低** | [workplan/plan.go:14-18](workplan/plan.go#L14-L18) | `Agent` 接口只有 `Chat()` 没有 `ChatStream()`，WorkPlan 无法享受流式输出体验。如果 Engine 需要流式推送给最终用户，当前设计做不到 |
| 8 | **低** | [mcp_provider.go:38-46](mcp_provider.go#L38-L46) | `MCPServerConfig` 类型定义在 `mcp_provider.go` 而非 `model.go`。其他配置类型（`LLMConfig`、`AppConfig`）都在 `model.go`，不一致。并且 `seele_api.go` 引用 `runtime.MCPServerConfig`，如果未来有其他包也需要此类型会产生耦合 |

### 8.3 代码风格

| # | 位置 | 说明 |
|---|------|------|
| 9 | [chat_client.go:317](chat_client.go#L317) | 注释 `// 可以理解为一个接收的loop` 是中英混杂，建议统一为中文或英文 |
| 10 | [agent.go](agent.go) | `Chat` 和 `ChatStream` 中 dispatch 的并发执行逻辑有大量重复代码（~30 行几乎相同），可以提取为 `dispatchToolCalls` 私有方法 |

### 8.4 架构亮点

- **ToolProvider 插件模式**：接口只依赖标准库和 context，新增工具源不需要修改核心代码
- **热更新设计**：`tools()` 每次 LLM 调用前实时聚合，MCP 可以运行时 Attach/Detach/RefreshTools
- **并发安全**：Runtime 和两个 Provider 都使用 `sync.RWMutex` 保护共享状态
- **分层清晰**：Engine → Runtime → Agent，每层只依赖下层接口
- **WorkPlan 的糖/原语分离**：公有方法零执行逻辑，只构造数据结构；所有执行逻辑在私有原语方法中，职责分明
- **Signal 模式**：Loop 的响应式机制很巧妙，让外部能在不侵入执行引擎的情况下实时感知进度

---

*本文档由代码评审生成，涵盖了 Seele 项目的主要数据流、类型体系、函数关系和架构分析。*
