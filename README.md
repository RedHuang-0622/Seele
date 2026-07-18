# Seele — Go AI Agent 框架

[![Go Build](https://img.shields.io/badge/go%20build-passing-brightgreen)](#)
[![Go Vet](https://img.shields.io/badge/go%20vet-passing-brightgreen)](#)
[![Go Test](https://img.shields.io/badge/go%20test-passing-brightgreen)](#)
[![Go Version](https://img.shields.io/badge/go-1.25.5-blue)](./go.mod)
[![License](https://img.shields.io/badge/license-MIT-green)](./LICENSE)
[![Code Size](https://img.shields.io/badge/code~12.5k%20lines-blue)](#)

Seele 是一个 **Go 原生 AI Agent 框架**，核心差异化是 **WorkPlan 图编排 + 三态 NodeStrategy**——你可以在同一个工作流里混合执行全量 Agent、纯 LLM 调用和本地函数，精准控制 token 消耗。不是 LangChainGo 的 Go 移植，是一个从头设计的 Go 原生框架。~65 个库文件，~12.5k 行，一个下午读完。

---

## 目录

- [为什么是 Seele](#为什么是-seele)
- [整体架构](#整体架构)
- [请求生命周期](#请求生命周期)
- [核心模块](#核心模块)
  - [Engine — ReAct 循环引擎](#engine--react-循环引擎)
  - [Agent — 工具路由中枢](#agent--工具路由中枢)
  - [ChatClient — LLM HTTP 封装](#chatclient--llm-http-封装)
  - [ProviderStrategy — 传输层策略模式](#providerstrategy--传输层策略模式)
  - [function.Strategy — 工具编码策略模式](#functionstrategy--工具编码策略模式)
  - [Holder — 工具注册调度中心](#holder--工具注册调度中心)
  - [Graph — 图执行引擎](#graph--图执行引擎)
  - [Permission Gate — 权限门控](#permission-gate--权限门控)
  - [MCP Circuit Breaker — 熔断与降级](#mcp-circuit-breaker--熔断与降级)
  - [NodeStrategy — 三态节点执行策略](#nodestrategy--三态节点执行策略)
  - [Tracer — 可观测性追踪树](#tracer--可观测性追踪树)
  - [AccountPool — 号池管理](#accountpool--号池管理)
- [设计模式](#设计模式)
- [项目目录](#项目目录)
- [配置格式](#配置格式)
- [快速开始](#快速开始)
- [示例一览](#示例一览)
- [扩展方式](#扩展方式)
- [学习路线](#学习路线)
- [设计原则](#设计原则)

---

## 为什么是 Seele

### 项目定位

Seele 解决的核心问题：**Go 生态中缺少成熟的 AI Agent 框架。** 现有的方案要么是 Python 框架的 Go 移植（LangChainGo），要么节点类型单一（Eino、Galdor），无法在同一个工作流里混合不同粒度的节点。

**与同类项目对比：**

| 特性 | Seele | LangChainGo | Eino（字节） | Galdor |
|------|-------|-------------|-------------|--------|
| 语言 | Go 原生 | Go 移植 Python | Go 原生 | Go 原生 |
| 第三方 LLM SDK | 零依赖 | 多 | go-openai | 无 |
| 节点粒度 | 三态 | 单一 Agent | 单一 Agent | 单一 Agent |
| 图编排 | WorkPlan | Chain/LCEL | Graph | Graph |
| Provider 传输层 | Strategy 模式 | 硬编码 | 多实现 | 多实现 |
| 可观测性 | 可选 Tracer | 无内置 | 有 | 无 |
| 号池原生 | ✅ | ❌ | ❌ | ❌ |
| 代码规模 | ~12.5k | 数万行 | 数万行 | ~5k |

**适合场景：**
- 需要 WorkPlan 级别编排控制的 Go Agent 应用
- 对 token 消耗敏感，需要混合零 token（本地函数）和全量 Agent 的场景
- 需要多 Provider 号池管理（round-robin、按名称切换、RPM 限流）
- 偏好零第三方 LLM SDK、纯 net/http 的小体积生产服务

**不适合场景：**
- 需要开箱即用大量 Provider（只内置 OpenAI/Anthropic，其余需自己实现）
- 需要大规模社区支持（目前是个人项目）
- 需要高级 RAG 管道（无内置 RAG 组件）

### 技术栈

| 层面 | 技术 | 选择原因 |
|------|------|----------|
| 语言 | Go 1.25.5 | 静态类型、接口组合、并发原语、零运行时 |
| HTTP | net/http（标准库） | 零外部依赖，掌控 HTTP 全生命周期 |
| LLM 协议 | HTTP/1.1 JSON + SSE | Provider 通用传输协议 |
| 工具协议 | MCP（Model Context Protocol） | 行业标准 Agent 工具协议 |
| 微服务 | gRPC（microHub） | 外部 Skill 进程的二进制通信 |
| 序列化 | encoding/json + yaml.v3 | 标准库 + YAML 配置 |
| 配置 | YAML | 可读性好 |
| 可观测性 | OpenTelemetry（可选） | 集成标准可观测生态 |
| 外部 MCP | mark3labs/mcp-go | MCP 协议 Go 客户端实现 |
| 追踪 | 内置 SimpleTracer | 不强制依赖 OTel，可选增强 |

**没有第三方 LLM SDK（OpenAI Go SDK、Anthropic Go SDK 等）是核心设计决策**——所有 LLM 通信通过 `net/http` + JSON + SSE 完成，保证零外部传输层依赖。

---

## 整体架构

```
┌────────────────────────────────────────────────────────────────────────────┐
│  Engine（ReAct 循环引擎）                                                    │
│  ├─ Chat / ChatStream                                                     │
│  ├─ ReActLoop: LLM → tool_calls → dispatch → repeat                       │
│  ├─ history: session 级对话历史管理                                        │
│  └─ tracer: 全链路追踪（可选注入，默认零开销 NoopTracer）                    │
├────────────────────────────────────────────────────────────────────────────┤
│  Agent（工具路由中枢）                                                      │
│  ├─ tool.Holder:  工具注册中心                                             │
│  ├─ tool.Gateway: 可见性过滤 + 调度                                         │
│  ├─ MCP / Hub:    外部工具协议接入                                          │
│  └─ InlineTool:   Go 函数工具                                              │
├────────────────────────────────────────────────────────────────────────────┤
│  WorkPlan（图编排 — 核心差异化）                                             │
│  │  ┌──────────────────────────────────────────────────────────────────┐   │
│  │  │ sugar.go: 声明式 API         │  runner.go: 节点执行引擎          │   │
│  │  │ ├─ Auto(id, input)          │  ├─ strategyRunner（适配器）       │   │
│  │  │ ├─ Method(id, fn)           │  ├─ loopRunner + Signal           │   │
│  │  │ ├─ LLM(id, input)           │  ├─ forkRunner（并发分支）         │   │
│  │  │ ├─ Strategy(id, custom)     │  └─ approveRunner（审批）          │   │
│  │  │ ├─ If / Switch / Loop       │                                    │   │
│  │  │ ├─ Fork(branches)           │  graph.go: 图引擎                  │   │
│  │  │ ├─ Approve / Gate           │  ├─ NodeRunner 接口               │   │
│  │  │ ├─ Emit(key) / Checkpoint   │  ├─ Edge{From,To,Condition}       │   │
│  │  │ └─ Pipeline(steps)          │  └─ resolve() 统一路由             │   │
│  │  │                              │                                    │   │
│  │  │ strategy.go: 三态节点        │  plan.go: 序列化                   │   │
│  │  │ MethodStrategy(零 token)     │  ├─ Plan / PlanNodeSpec           │   │
│  │  │ LLMStrategy(纯 LLM)         │  ├─ ToPlan() / LoadPlan()         │   │
│  │  │ AgentStrategy(全量 ReAct)   │  └─ ToTool() → 子图包装为工具      │   │
│  │  └──────────────────────────────────────────────────────────────────┘   │
├────────────────────────────────────────────────────────────────────────────┤
│  Contexts（LLM 会话层）                                                     │
│  ├─ ChatClient: HTTP 生命周期 + ProviderStrategy                           │
│  │     ├─ ProviderStrategy("openai")  ← 传输层格式                         │
│  │     └─ function.Strategy("openai") ← 工具编码格式                       │
│  ├─ cache:     TTL + 内容寻址缓存（SHA256 去重）                           │
│  ├─ storage:   JSON 分片持久化                                             │
│  └─ tracer:    追踪树 + OTel 发射（可选）                                   │
└────────────────────────────────────────────────────────────────────────────┘
```

### 关键设计：零循环依赖

```
types/      ← 所有包（无入边）
   ↑
contexts/  ← agent/（使用 ChatClient）
   ↑
agent/     ← engine/（使用 Agent.LLM / Agent.Dispatch）
   ↑
engine/    ← 用户代码 / WorkPlan
   ↑
workplan/  ← 仅依赖 Agent 接口（3 行定义），不依赖 agent/ 包
```

`workplan/` 只依赖三个接口方法（`Agent` 接口的 `Chat` 方法），实现了完全的解耦。

---

## 请求生命周期

```
User Input
    │
    ▼
Engine.Chat(ctx, "Hello!")
    │
    ▼
ReActLoop.Run()
    ├─ [1] restoreFromCache()        ← FileCache 恢复历史（TTL + 置信度）
    ├─ [2] append user message        ← 追加到 history
    ├─ [3] CompressHistory(if needed) ← token > 6144 时自动压缩
    │
    ▼ 循环开始（MaxLoops 次）
    for loop = 0; loop < cfg.MaxLoops; loop++:
    │   ├─ [4] VisibleTools()         ← 获取当前对 LLM 可见的工具列表
    │   │
    │   ├─ [5] ChatClient.Complete()   ← 调用 LLM
    │   │   ├─ effectiveAccount()     ← 从号池获取可用账号（round-robin + RPM）
    │   │   ├─ effectiveStrategy()    ← 按 Provider 选择传输层策略
    │   │   ├─ BuildRequest()         ← 协议特定 JSON 序列化
    │   │   ├─ HTTP POST → 响应解析
    │   │   └─ ParseResponse()        ← 协议特定反序列化
    │   │
    │   ├─ 纯文本回复？→ return content
    │   │
    │   └─ tool_calls 回复：
    │       for each tool_call:
    │       ├─ [6] Agent.Dispatch()    ← 通过工具网关 → Holder
    │       ├─ Handler.Execute()       ← 带重试 + 超时
    │       └─ append tool result      ← 截断后注入历史
    │
    ▼ 循环结束
    ├─ [7] saveToCache()              ← 保存历史到缓存
    ├─ [8] ExportTrace() → Tree       ← 导出全链路追踪树
    └─ return reply
```

---

## 核心模块

### Engine — ReAct 循环引擎

**文件：** `engine/engine.go`、`engine/loop.go`、`engine/config.go`

**职责：** Agent ReAct 循环的上层封装。用户主入口。管理会话历史、缓存、追踪。不感知具体工具逻辑。

**入口：** `engine.New(agt, opts...)`
**核心接口：** `Loop`（`engine/loop.go:21`）— 使 ReAct 循环可替换

```go
type Loop interface {
    Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
    History() []types.Message
    ClearHistory()
}
```

**设计模式：** 功能选项（Functional Options）、策略模式（Loop 接口可替换）

**关键逻辑（`ReActLoop.Run`）：**
1. 创建 Tracer 根 span
2. `restoreFromCache()` — 从 FileCache 恢复历史
3. 追加用户消息，必要时压缩历史
4. 循环：获取工具 → 调用 LLM → 纯文本？返回 → 工具调用？dispatch 后继续
5. `saveToCache()` — 保存历史
6. 导出 Trace Tree

`ChatClient.Complete()` 内部通过 `effectiveAccount()` + `effectiveStrategy()` 双层路由实现 Provider 无关的 LLM 调用。

---

### Agent — 工具路由中枢

**文件：** `agent/agent.go`

**职责：** 工具路由中枢，组装 LLM 客户端、工具注册中心、网关。是"胶水层"——不执行 LLM 调用或工具逻辑，只做拼装和路由。

```go
type Agent struct {
    llmClient  *api.ChatClient
    tools      *holder.Holder      // 工具注册中心
    apiGW      apigw.Gateway      // API 账号网关
    toolGW     toolgw.Gateway     // 工具网关（可见性过滤）
    pool       *api.AccountPool   // 号池
    hub        *hubbase.BaseHub
    hubProvider *hubprov.HubProvider
    mcpProvider *mcp.Provider
    // ...
}
```

**关键方法：**
- `New(opts)` — 9 步初始化（registry → Hub → Pool → Gateway → ChatClient → Provider）
- `RegisterTool(name, desc, schema, handler)` — 注册 Go 函数工具
- `RegisterWorkPlanTool(tool)` — 注册 WorkPlan 作为工具
- `VisibleTools(ctx)` — 获取当前可见工具列表（通过 Gateway 过滤）
- `Dispatch(ctx, name, args)` — 调度工具执行（带优雅关闭的 wg 追踪）
- `Shutdown()` — 优雅关闭：发送信号 → 等待 in-flight 完成 → 清理 MCP → 关闭

**设计亮点：** `Shutdown` 用信号量模式（`chan struct{}` 关闭信号 + `sync.WaitGroup` 追踪 in-flight），保证所有 Dispatch 完成后再释放资源。

---

### ChatClient — LLM HTTP 封装

**文件：** `agent/core/api/client.go`

**职责：** LLM API 的轻量 HTTP 封装。无第三方 SDK，纯标准库 `net/http`。

```go
type ChatClient struct {
    Cfg             types.LLMConfig
    Client          *http.Client
    pool            *AccountPool     // 账号池
    strategy        ProviderStrategy // 传输层策略
    provider        ProviderType     // llm_config.provider 锁死消息格式
    providerFilter  ProviderType     // 非空时只从 pool 获取该 provider 的账号
}
```

**关键方法：**
- `Complete(ctx, messages, tools)` — 同步请求
- `CompleteStream(ctx, messages, tools, onChunk)` — SSE 流式请求
- `SelectAccount(name)` — 按名称切换号池账号
- `SetProvider(provider)` — 设置会话级 provider

**设计亮点：**
- 双层策略选择：`c.provider`（`llm_config` 设定）> `acct.Provider`（账号级），保证同一 session 内消息格式一致
- SSE 状态机（`sseState`）：通过 `tcMap` 累积多帧 tool_call，处理流式多并发工具调用的增量累积
- `requestOpts()` 中 Account 级配置覆盖全局配置

---

### ProviderStrategy — 传输层策略模式

**文件：** `agent/core/api/strategy.go`、`strategy_openai.go`、`strategy_anthropic.go`

**接口 — 6 个方法封装 Provider 协议差异：**

```go
type ProviderStrategy interface {
    Name() string                           // 策略名称
    Endpoint() string                       // API 路径，如 "/chat/completions"
    BuildRequest(model, messages, tools, stream, opts) ([]byte, error)  // 请求体序列化
    ParseResponse(body) (types.Message, error)  // 同步响应解析
    ParseSSEEvent(eventType, payload) ([]SSEEvent, error)  // SSE 帧解析
    AuthHeader(apiKey) (string, string)     // 认证头部
    SSEHeaders() map[string]string          // 流式额外头部
}
```

**内置实现：**
- `OpenAIStrategy` — POST `/chat/completions`，`Authorization: Bearer`
- `AnthropicStrategy` — POST `/v1/messages`，`x-api-key`

**注册表模式：** 全局 `map[string]ProviderStrategy`，策略通过 `init()` 自注册。新增 Provider 只需实现 6 个方法 + `init()` 注册。

---

### function.Strategy — 工具编码策略模式

**文件：** `agent/core/function/strategy.go`、`openai.go`、`anthropic.go`

```go
type Strategy interface {
    EncodeTools(tools []types.Tool) interface{}       // 工具定义编码
    DecodeToolCall(raw interface{}) *types.ToolCall   // 工具调用解码
}
```

**为何传输层和工具层分离：** ProviderStrategy 处理"怎么发请求"，function.Strategy 处理"工具长什么样"。两者不同步——OpenAI/Anthropic 都用 JSON 但工具格式（`{type,function}` vs `{name,input_schema}`）不同。各自的注册表独立管理。

---

### Holder — 工具注册调度中心

**文件：** `agent/core/tool/holder/holder.go`

**职责：** 工具注册、调度、插件装配件管理。

```go
type Holder struct {
    providers []interfaces.ToolProvider
    state     atomic.Pointer[holderState]   // 无锁读
    DispatchRetries    int
    ToolCallTimeout    time.Duration
    pluginMgr          *PluginManager
}
```

**设计亮点：**
- **原子状态模式**：`holderState`（含 `toolMap` + `toolList`）通过 `atomic.Pointer` 实现无锁高频读；写时 `rebuildLocked()` 全量重建——适合工具低频注册、高频读取的场景
- **Dispatch 重试**：只对 `ErrToolUnavailable` 重试，超时和业务错误不重试
- `RegisterInline` 自动创建 `inlineProvider`，`_inline` 前缀的 ProviderName

**三种 ToolProvider 来源：**

| Provider | 来源 | 特点 |
|----------|------|------|
| `inlineProvider` | Go 函数 | 直接注册的本地函数 |
| `HubProvider` | microHub gRPC | 外部 Skill 进程发现与调度 |
| `MCP Provider` | MCP 协议 | stdio/SSE 连接的 MCP Server |

---


### Permission Gate — 权限门控



**文件：** `agent/core/tool/permission/types.go`、`checker.go`，`agent/gateway/tool/default.go`



**职责：** 在工具执行前进行权限拦截。支持 full_access（全放行）和 manual（白名单外需审批）两种模式。



```go

type Mode string

const (

    ModeFullAccess Mode = "full_access" // 所有工具静默放行

    ModeManual     Mode = "manual"      // 白名单内自动放行，白名单外弹审批

)



type PermissionConfig struct {

    Mode  Mode

    Rules []PermissionRule  // 细粒度规则（glob 模式匹配）

}



type PermissionRule struct {

    ToolName string    // 工具名（支持 glob）

    Patterns []string  // 参数模式（如 "git *"）

    Action   Action    // allow | ask | deny

}

```



**核心流程：**

```

Dispatch(name, args)

  → PermissionChecker.Check(name, args)  ← 每次调用硬性读取 Mode

    → full_access → ResultAllow → 放行

    → manual → 匹配规则 → Allow/Deny/Ask

    → Ask → ApprovalHandler → TUI 弹框 → 用户选择 → 执行/拒绝/记住

```



**设计要点：**

- **对 LLM 透明：** 模型不知道权限检查存在，看到的只有"执行成功"或"permission denied"

- **glob 模式匹配：** `grep_search`、`git *`、`rm *` 级别控制

- **始终允许：** 用户选择"始终允许"后写入缓存，后续同工具+参数自动放行

- **运行时热切换：** `PermissionChecker.SetMode()` 可在运行时切换模式



### MCP Circuit Breaker — 熔断与降级



**文件：** `agent/core/tool/mcp/breaker.go`



**职责：** 防止 MCP Server 宕机时 Agent 循环重试导致的 token 风暴。



```go

type mcpBreaker struct {

    servers  map[string]*breakerState

    maxFails int           // 连续失败 N 次后打开熔断（默认 3）

    backoff  time.Duration // 指数退避：5s → 10s → 20s → 40s → 60s（封顶）

}



// handler.go:32  // 执行路径嵌入熔断检查

func (h *Handler) Execute(ctx, args) (string, error) {

    breaker.beforeCall(server)   // 熔断中？→ 降级返回

    result, err := client.CallTool()

    breaker.afterCall(server, isConnErr)

    if connErr && breaker.isOpen(server) {

        breaker.startRecovery(server, ping)  // 后台孤立 ping，不烧 token

    }

}

```



**关键设计：**

- **连接错误 vs 业务错误分离：** 仅有网络断开/进程退出等连接错误触发熔断；JSON 解析/业务逻辑错误正常返回

- **后台孤立恢复：** 熔断打开后启动独立 goroutine 每 3s ping 一次，与 Agent ReAct 循环完全隔离

- **降级返回不标记 ErrToolUnavailable：** 避免 Holder 触发重试循环

- **多 Server 故障隔离：** Server-A 熔断不影响 Server-B 正常调用

- **半开探测：** 熔断超时到期后允许一次探测调用，成功则关熔断，失败则继续退避

### Graph — 图执行引擎

**文件：** `workplan/graph.go`

**职责：** WorkPlan 底层的图执行引擎。

```go
type Graph struct {
    nodes map[string]NodeRunner
    edges []Edge
    entry string
}

type Edge struct {
    From      string
    To        string
    Condition EdgeCondition   // func(ec *ExecutionContext) bool
    Priority  int
    Label     string
}
```

**设计模式：** 有向图遍历（while 循环）+ 条件边（函数作为一等公民）

**核心逻辑（`resolve()`）：**
1. 找到从当前节点出发的所有出边
2. 有无条件边？→ 直接返回目标节点
3. 有条件边？→ 按 Priority 排序后依次匹配，第一个命中返回
4. 无出边？→ 图结束

**为什么这样设计：**
- `EdgeCondition` 是函数类型，图引擎不需要知道具体的条件逻辑
- 无条件边提供"默认路由"，条件边提供"策略路由"
- 边优先级控制匹配顺序，Switch 的多路分支自然映射为按 Priority 排序的条件边

---

### NodeStrategy — 三态节点执行策略

**文件：** `workplan/strategy.go`

**接口：**

```go
type NodeStrategy interface {
    Execute(ctx context.Context, ec *ExecutionContext) (string, error)
}
```

**三个内置实现 + 一个适配器：**

| 策略 | 输入 | 输出 | Token | 场景 |
|------|------|------|-------|------|
| `MethodStrategy` | Go 函数 `fn(ctx, input)` | 任意 JSON | 零 token | 数据转换、校验、计算 |
| `LLMStrategy` | AgentFactory → `agent.Chat()` | 纯文本 JSON | 少量 token | 翻译、摘要、分类 |
| `AgentStrategy` | AgentFactory → 完整 ReAct 循环 | 文本 + 工具调用 | 大量 token | 搜索、分析、对话 |
| `AdapterDeprecatedStrategy` | 旧签名适配 | 同上 | 按适配策略 | 兼容旧代码 |

**用户自定义策略：**
```go
type MyStrategy struct{}
func (s *MyStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
    return `{"result": "custom"}`, nil
}
wp.Strategy("my-node", &MyStrategy{})
```

**为什么会存在三态：** Go 生态中所有其他框架（LangChainGo、Eino、Galdor）的节点都是全量 Agent——每个节点都要走完完整的 ReAct 循环。Seele 通过 NodeStrategy 接口让节点可以混合零 token（Method）、轻量（LLM）、全量（Agent），在同一工作流中精准控制成本。

NodeStrategy 通过 `strategyRunner` 适配为 `NodeRunner`（桥接模式）：

```go
type strategyRunner struct {
    id       string
    input    string        // 模板渲染后写入 ec.PrevOutput
    strategy NodeStrategy
}

func (r *strategyRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
    input := renderTemplate(r.input, ec)  // 框架层统一渲染
    if input != "" { ec.PrevOutput = rendered }
    return r.strategy.Execute(ctx, ec)    // 策略只关心 ec.PrevOutput
}
```

---

### Tracer — 可观测性追踪树

**文件：** `contexts/tracer/tracer.go`

**核心接口：**

```go
type Tracer interface {
    NewTrace(ctx, traceID) (context.Context, Span)
    StartSpan(ctx, name, kind, attrs) (context.Context, Span)
    Export(ctx) *Tree
}

type Span interface {
    End(opts ...SpanOption)
    ID() string
    SetAttr(key, value string)
    AddEvent(name string, attrs map[string]string)
}
```

**两个实现：**

| 实现 | 特点 | 适用场景 |
|------|------|----------|
| `NoopTracer` | 所有方法空实现，编译器内联消除 | 生产环境无追踪需求 |
| `SimpleTracer` | 内存存储 → JSON 导出，可选 OTel 发射 | 开发调试、运维监控 |

**追踪树结构：**
```
Trace: 一次 Chat/ChatStream 的完整执行链路
  └─ Node（span）
        ├─ kind="react_loop"      — 根节点，整体 ReAct 循环
        ├─ kind="llm_call"        — 单次 LLM 调用（含 token 计数）
        ├─ kind="tool_dispatch"   — 单次工具调度（含工具名、参数摘要）
        └─ kind="cache_op"        — 缓存读写操作
```

**零开销原理：** `Engine` 默认使用 `NoopTracer`（`e.tracer = &tracer.NoopTracer{}`）。NoopTracer 的方法全是空函数，Go 编译器会内联消除——不注入即零成本。

---

### AccountPool — 号池管理

**文件：** `agent/core/api/pool.go`

**核心结构：**

```go
type Account struct {
    Name     string       // 账号名称
    Provider ProviderType // "openai" | "anthropic"
    BaseURL  string
    APIKey   string
    Model    string
    Priority int          // 优先级，数字越小越优先
    MaxRPM   int          // 每分钟最大请求数
    Disabled bool         // 是否禁用
    window   []time.Time  // RPM 滑动窗口
}

type AccountPool struct {
    accounts []*Account
    current  int            // round-robin 索引
}
```

**特性：**
- round-robin 轮询（按 Priority 排序）
- RPM 滑动窗口限流（`allow()` 方法）
- 按 Provider 筛选（`GetByProvider`）
- 按名称切换（`Select(name)`）
- 运行时添加账号（`Add`）

**号池数据流：**
```
ChatClient.Complete()
  → effectiveAccount()
    → pool.Get() → round-robin 查找可用账号
    → Account.allow() → 滑动窗口 RPM 检查
  → effectiveStrategy()
    → c.provider > acct.Provider > "openai"
```

---

## 设计模式

Seele 大量使用 Go 的接口组合和函数式编程模式：

| 模式 | 使用位置 | 文件 | 说明 |
|------|---------|------|------|
| **策略模式** | ProviderStrategy | `agent/core/api/strategy.go` | 6 方法封装 LLM Provider 协议差异 |
| **策略模式** | function.Strategy | `agent/core/function/strategy.go` | 工具编码格式差异 |
| **策略模式** | NodeStrategy | `workplan/strategy.go` | 三态节点执行策略 |
| **策略模式** | Loop 接口 | `engine/loop.go` | ReAct 循环可替换 |
| **策略模式** | CompletionStrategy | `contexts/react/strategy.go` | 同步/流式策略 |
| **适配器模式** | strategyRunner | `workplan/runner.go:22` | NodeStrategy → NodeRunner |
| **适配器模式** | AdapterDeprecatedStrategy | `workplan/strategy.go:39` | 旧节点签名适配 |
| **函数选项** | Engine Option | `engine/engine.go:42` | WithTracer/WithCache/WithSystemPrompt |
| **函数选项** | WorkPlanOption | `workplan/plan.go:34` | WithSemaphore/WithTracer |
| **函数选项** | ReActLoopOption | `engine/loop.go:44` | WithMaxLoops/WithSessionID |
| **注册表模式** | ProviderStrategy 注册表 | `agent/core/api/strategy.go:80` | 全局 map + init() 自注册 |
| **注册表模式** | function.Strategy 注册表 | `agent/core/function/strategy.go:27` | 同上 |
| **注册表模式** | ConditionRegistry | `workplan/plan.go:508` | 条件标签 → EdgeCondition |
| **Null Object** | NoopTracer | `contexts/tracer/tracer.go:209` | 编译器零开销 |
| **组合模式** | Engine 组合 Agent | `engine/engine.go:26` | Engine 持有 Agent |
| **组合模式** | Agent 组合多 Provider | `agent/agent.go:81` | tools + apiGW + toolGW + pool |
| **桥接模式** | 两层策略分离 | `api/strategy.go` + `function/strategy.go` | 传输层独立于工具层 |
| **模板方法** | ReActLoop.Run | `engine/loop.go:103` | ReAct 循环骨架 |
| **装饰器模式** | CachedStrategy | `workplan/cache_strategy.go` | NodeStrategy + 缓存 |
| **信号量模式** | WorkPlan semaphore | `workplan/plan.go:40` | Buffered channel 控制并发 |
| **信号量模式** | Signal | `workplan/node.go:171` | Loop 活引用 |
| **原子状态模式** | Holder state | `agent/core/tool/holder/holder.go:22` | atomic.Pointer 无锁读 |
| **工厂方法** | AgentFactory | `workplan/plan.go:28` | NewAgent(systemPrompt) |

---

## 项目目录

```
Seele/
│
├── cmd/repl/main.go               ← 唯一入口：交互式 REPL
│
├── agent/                           ← Agent 编排层
│   ├── agent.go                    core：Agent 创建 + 工具注册
│   ├── pool.go                     AccountPool + round-robin
│   └── core/
│       ├── api/
│       │   ├── client.go           ChatClient（HTTP 编排 + SSE 状态机）
│       │   ├── strategy.go         ProviderStrategy 接口 + 注册表
│       │   ├── strategy_openai.go  OpenAI 协议实现
│       │   ├── strategy_anthropic.go Anthropic 协议实现
│       │   ├── pool.go             Account + AccountPool
│       │   └── config.go          YAML 号池配置加载
│       ├── function/
│       │   ├── strategy.go         function.Strategy 接口
│       │   ├── openai.go          OpenAI 工具编码
│       │   └── anthropic.go       Anthropic 工具编码
│       └── tool/
│           ├── schema.go          SchemaOf 反射生成 JSON Schema
│           ├── interfaces/types.go ToolEntry/ToolProvider 接口
│           ├── holder/
│           │   ├── holder.go      Holder：工具注册/调度中枢
│           │   ├── config.go      配置
│           │   └── plugin.go      PluginManager 插件装配件
│           ├── hub/
│           │   ├── hub.go         HubProvider（microHub 集成）
│           │   ├── handler.go     HubToolHandler gRPC 客户端
│           │   └── router.go      HubRouter
│           ├── mcp/
│           │   ├── provider.go    MCP Provider（stdio/SSE + 健康检查）
│           │   ├── handler.go     MCP Handler（含熔断拦截）
│           │   └── breaker.go     Circuit Breaker（熔断 + 后台恢复）
│           └── permission/
│               ├── types.go       PermissionConfig + Mode + Rule
│               └── checker.go     PermissionChecker（glob 匹配 + cache）
│   └── gateway/
│       ├── api/gateway.go         API 网关
│       └── tool/gateway.go        Tool 网关（可见性过滤）
│
├── engine/                           ← ReAct 循环引擎
│   ├── engine.go                   Engine 核心 + Tracer/Cache 集成
│   ├── loop.go                     Loop 接口 + ReActLoop 实现
│   └── config.go                   SessionConfig
│
├── contexts/                         ← LLM 会话上下文
│   ├── tracer/tracer.go            Trace Tree（NoopTracer 零开销）
│   ├── cache/provider.go           FileCache（SHA256 内容寻址 + TTL）
│   ├── storage/store.go            JSON 分片持久化
│   ├── react/strategy.go           CompletionStrategy 接口
│   ├── history/history.go          历史管理
│   ├── cache_tool.go               缓存工具
│   └── config.go                   上下文配置
│
├── workplan/                         ← 图编排引擎（核心差异化）
│   ├── plan.go                     WorkPlan + Run + Plan 序列化 + ToTool
│   ├── graph.go                    图引擎 + Edge 条件路由
│   ├── strategy.go                 NodeStrategy 接口 + 三态实现
│   ├── sugar.go                    声明式构建 API（零执行逻辑）
│   ├── runner.go                   所有 Runner 实现
│   ├── node.go                     NodeKind + Signal + WorkPlanResult
│   ├── gate.go                     两段式审批（Approve/Resume）
│   ├── validate.go                 拓扑校验 + DFS 环检测
│   ├── cache_strategy.go           CachedStrategy 装饰器
│   └── tracer_internal.go          内置 Tracer/Span 接口
│
├── types/model.go                    ← 核心类型（ChatCompleter/Message/Tool）
├── config/                           ← 配置文件 + 加载器
├── example_Implement/                ← 7 个由浅入深示例
├── test/                             ← 集成测试
├── tools/tools.go                    ← SchemaOf 工具函数
├── docs/                             ← 设计文档 / PRD / Review
└── sandbox/                          ← 沙箱
```

---

## 配置格式

配置文件使用 `account-{provider}.yaml` 格式，`llm_config` 段锁死消息格式：

```yaml
llm_config:
  provider: openai                 # "openai" | "anthropic"（锁死消息格式）
  max_tokens: 4096
  timeout: 60
  temperature: 0.7

accounts:
  - name: main                     # 号池内多个账号（round-robin 轮转）
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-v4-flash
    priority: 1

  - name: fallback                 # 备选账号
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-v4-pro
    priority: 2
    max_tokens: 8192               # 逐账号覆盖全局设置
    disabled: true                 # 暂时禁用
```

**设计思想：** `llm_config.provider` 锁死消息格式，Account 只做路由（不携带协议信息）。Account 级配置（`max_tokens`、`timeout`、`temperature`）可覆盖全局 `llm_config`。

---

## 快速开始

```bash
# 1. 创建配置文件
cp config/account-openai.yaml config/account-openai.yaml
# 编辑 config/account-openai.yaml，填入你的 API Key

# 2. 运行示例
cd example_Implement

go run ./01_hello_seele/ -c ../config/account-openai.yaml    # OpenAI 格式
go run ./01_hello_seele/ -c ../config/account-anthropic.yaml # Anthropic 格式
go run ./03_workplan/ -c ../config/account-openai.yaml       # WorkPlan 编排
go run ./07_tracer/ -c ../config/account-openai.yaml         # 全链路追踪
```

### 在你的代码中使用

```go
package main

import (
    "context"
    "flag"
    "github.com/RedHuang-0622/Seele/agent"
    "github.com/RedHuang-0622/Seele/agent/core/api"
    "github.com/RedHuang-0622/Seele/contexts/tracer"
    "github.com/RedHuang-0622/Seele/engine"
    "github.com/RedHuang-0622/Seele/types"
)

var cfgPath = flag.String("c", "config/account-openai.yaml", "config path")

func main() {
    flag.Parse()

    // 1. 加载配置（YAML → AccountPool + LLMDefaults）
    result, _ := api.LoadFullAccountsConfig(*cfgPath)
    ls := result.LLMDefaults
    pool := result.Pool
    acct := pool.All()[0]

    llmCfg := types.LLMConfig{
        BaseURL: acct.BaseURL, APIKey: acct.APIKey,
        Model: acct.Model, MaxTokens: ls.MaxTokens,
    }

    // 2. 创建 Agent（初始化 LLM 客户端、工具注册中心、Hub、MCP）
    agt, _ := agent.New(agent.Options{LLMConfig: llmCfg})
    defer agt.Shutdown()

    // 3. 注入号池 + 锁死消息格式
    chatClient := agt.LLM().(*api.ChatClient)
    chatClient.WithAccountPool(pool)
    chatClient.SetProvider(ls.Provider)

    // 4. 注册工具（Go 函数，自动包装为 function calling 格式）
    agt.RegisterTool("hello", "say hello",
        map[string]any{"type": "object", "properties": map[string]any{}},
        func(ctx context.Context, args string) (string, error) {
            return `{"reply":"hello"}`, nil
        })

    // 5. 创建 Engine（可选注入 Tracer、Cache、Store）
    tr := tracer.NewSimpleTracer()
    eng := engine.New(agt,
        engine.WithTracer(tr),
        engine.WithSystemPrompt("You are helpful."))

    // 6. 对话 + 导出追踪树
    reply, _ := eng.Chat(context.Background(), "Hello!")
    println(reply)

    tree := eng.ExportTrace()
    println(tree.String()) // JSON 追踪树
}
```

### WorkPlan 示例（核心差异化）

```go
wp := workplan.New(factory, gate, prompt)
wp.Method("validate", validateFunc).         // 0 token：本地校验输入
  LLM("rewrite", "改写为搜索关键词").          // N token：纯 LLM，不给工具
  Auto("search", "搜索文档").                 // N+M token：全量 Agent
  Method("format", formatFunc).              // 0 token：本地格式化
  Auto("answer", "生成最终回答")               // N+M token：全量 Agent
```

Go 生态中**没有其他框架提供这个粒度**的节点类型——LangChainGo 的 Chain、Eino 的 Graph、Galdor 的 Node 都是全量 Agent，你无法在一个工作流里混合零 token 和全量节点来控制成本。

---

## 示例一览

| 示例 | 说明 | 运行 |
|------|------|------|
| 01_hello_seele | 最简入门：Agent + Engine + 内联工具 | `go run . -c ../config/account-openai.yaml` |
| 02_inline_tools | SchemaOf 深度演示 | 同上 |
| 03_workplan | WorkPlan 工作流引擎（Auto/If/Loop/Fork） | 同上 |
| 04_mcp | MCP 协议集成（stdio / sse） | 同上（需安装 MCP Server） |
| 05_graph_tools | 图结构工具（子图→Tool） | 同上 |
| 06_provider_switch | 号池账号切换演示 | 同上 |
| 07_tracer | Trace Tree 可观测性追踪树 | 同上 |

所有示例均支持 `-c` 指定配置文件，OpenAI 和 Anthropic 格式通用。

---

## 扩展方式

### 新增一个 Provider（API）

只需实现 `ProviderStrategy` 接口的 6 个方法 + `init()` 注册：

```go
// 1. 创建 strategy_gemini.go
type GeminiStrategy struct{}
func (s *GeminiStrategy) Name() string { return "gemini" }
func (s *GeminiStrategy) Endpoint() string { return "/v1/models/gemini-pro:generateContent" }
// ... 实现 BuildRequest / ParseResponse / ParseSSEEvent / AuthHeader / SSEHeaders

func init() { RegisterProviderStrategy(&GeminiStrategy{}) }
```

同时（可选）实现 `function.Strategy`：
```go
// 2. 创建 function/gemini.go
type GeminiFuncStrategy struct{}
func (s *GeminiFuncStrategy) EncodeTools(tools []types.Tool) interface{} { ... }
func (s *GeminiFuncStrategy) DecodeToolCall(raw interface{}) *types.ToolCall { ... }

func init() { Register("gemini", &GeminiFuncStrategy{}) }
```

### 新增一个 Tool

```go
// 方式 1：Agent.RegisterTool
agt.RegisterTool("my_tool", "desc", inputSchema, handler)

// 方式 2：SchemaOf（结构体 → JSON Schema）
type Input struct { Query string `json:"query" desc:"搜索关键词"` }
agt.RegisterTool("search", "搜索", tool.SchemaOf(Input{}), handler)
```

### 新增一个 WorkFlow（图编排）

```go
wp := workplan.New(factory, gate, "You are helpful.")
wp.Auto("step1", "分析输入").
  Fork("step2", []workplan.ForkBranch{
    {Label: "分支A", Input: "方式A：{{.PrevResult}}"},
    {Label: "分支B", Input: "方式B：{{.PrevResult}}"},
  }).
  Emit("save", "result").
  LLM("summarize", "总结：{{.Vars.result}}")
```

### 新增自定义 Loop 实现

```go
type MyLoop struct{ ... }
func (l *MyLoop) Run(ctx, input, cb) (string, error) { ... }
func (l *MyLoop) History() []types.Message { ... }
func (l *MyLoop) ClearHistory() { ... }

eng := engine.New(agt, engine.WithLoop(&MyLoop{}))
```

### 新增号池账号

仅配置变更，无需改代码：

```yaml
accounts:
  - name: deepseek-lite
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-chat
    priority: 1
```

---

## 设计原则

1. **零循环依赖** — Engine → Agent → Contexts 单向依赖，WorkPlan 只依赖 `Agent` 接口
2. **Go 标准库** — net/http、log/slog、sync、atomic，零第三方 LLM SDK
3. **Strategy > Factory** — 协议差异用策略模式封装，不复制 HTTP 编排逻辑
4. **WorkPlan 三态节点** — Method（零 token）/ LLM（纯文本）/ Agent（全量）混合编排，精准控费
5. **号池轻量** — Account 只做路由，不携带协议信息（从 `llm_config` 继承）
6. **可观测性可选** — Tracer 接口 + NoopTracer 默认零开销，注入即开启
7. **可读性优先** — ~65 个库文件，~12.5k 行，无泛型过度使用，一个下午读完
8. **Sugar 层零执行逻辑** — `sugar.go` 只做声明式注册，执行在 `runner.go` 的 Run 方法

## 支持的 Provider

| Provider | 端点 | 认证 | 工具格式 | 状态 |
|----------|------|------|----------|------|
| OpenAI | `/chat/completions` | `Authorization: Bearer` | `{type,function}` | ✅ 正式 |
| Anthropic | `/v1/messages` | `x-api-key` | `{name,input_schema}` | ✅ 正式 |
| 自定义 | 任意 | 任意 | 任意 | 实现 6 个方法即可 |

Provider 数量不是 Seele 的目标——专注把 Go 原生框架的架构和编排做到最好。需要更多 Provider？实现一个 Strategy。

---

## 链接

- [GitHub 仓库](https://github.com/RedHuang-0622/Seele)
- [License: MIT](./LICENSE)
