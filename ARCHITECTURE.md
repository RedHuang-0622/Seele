# Seele 框架架构文档

> 生成时间: 2026-05-20 · 最后更新: 2026-06-14
> 涵盖 v0.3 重构（策略模式工具层 + 图引擎抽象）

---

## 1. 总览

```
┌──────────────────────────────────────────────────────────────────────┐
│                        sdk/api (类型别名层)                            │
│  Engine = agent.Agent · Session = session.Holder                     │
├──────────────────────────────────────────────────────────────────────┤
│                     core/agent (编排层)                                │
│  职责: 持有 LLM + tool_holder + Hub，生命周期管理                      │
│  方法: NewSession / QuickChat / RegisterInlineTool / Shutdown         │
├───────────────┬──────────────────────┬───────────────────────────────┤
│  core/session  │  core/tool_holder    │  workplan/                    │
│  ReAct 循环    │  O(1) map 分发       │  Graph + Edge 图引擎          │
│  对话管理      │  多 Provider 聚合     │  Auto/If/Switch/Loop/Fork     │
│  上下文压缩    │  瞬时错误重试         │  Approve/Gate/Emit/Checkpoint │
├───────────────┴──────────────────────┴───────────────────────────────┤
│  provider/                    llm/              history/              │
│  ToolProvider 接口 (1方法)    OpenAI 兼容 HTTP   上下文压缩/截断        │
│  HubHandler / MCPHandler /   同步+流式两种模式   Token 估算            │
│  InlineHandler / SchemaOf                                        │
└──────────────────────────────────────────────────────────────────────┘
```

## 2. 核心数据流

### 2.1 Agent.Chat() — ReAct 循环

```
用户输入 → session.Holder.Chat(ctx, input)
  │
  ├─ 1. 追加 user message 到 history
  │
  ├─ 2. tools.Tools() → 聚合所有 provider 的工具列表
  │      tool_holder 内部维护 map[string]ToolEntry
  │      ├─ HubProvider.Tools()     → []ToolEntry (gRPC skills)
  │      ├─ MCPProvider.Tools()     → []ToolEntry (MCP tools)
  │      └─ InlineProvider.Tools()  → []ToolEntry (Go functions)
  │
  └─ 3. FOR loop = 0; loop < maxLoops; loop++:
       │
       ├─ 3a. 上下文压缩检查
       │      NeedCompression(history, threshold) → true?
       │      ├─ CompressHistory(llm, history, maxTokens)
       │      │     ├─ 分离 system 消息
       │      │     ├─ 保留最近 4 条非 system 消息
       │      │     ├─ 其余 → buildCompressInput → 调用 LLM 生成摘要
       │      │     └─ 组装: system + [摘要注入为 system] + 最近4条
       │      └─ 失败 → TrimHistory 硬截断
       │
       ├─ 3b. llm.Complete(history, tools)
       │      POST {BaseURL}/chat/completions
       │
       ├─ 3c. 解析回复
       │      ├─ 无 tool_calls → 返回 Content 文本 ✓ 循环结束
       │      └─ 有 tool_calls → 追加 assistant 消息到 history
       │
       ├─ 3d. dispatchToolCalls(ctx, toolCalls)
       │      并发执行（最多 5 并发）:
       │      FOR each tool_call:
       │        tool_holder.Dispatch(name, argsJSON)
       │          └─ O(1) map 查找 → handler.Execute(ctx, argsJSON)
       │               ├─ HubToolHandler   → gRPC → Skill 进程
       │               ├─ MCPToolHandler   → CallTool → MCP Server
       │               └─ InlineToolHandler → Go 函数调用
       │      结果追加为 tool 消息到 history
       │      瞬时错误 (ErrToolUnavailable) → 最多重试 3 次（间隔 2s）
       │
       └─ 3e. 工具列表刷新（每轮重新读取，支持热更新）
```

### 2.2 WorkPlan 图引擎流程

```
wp.Run(ctx)
  │
  ├─ 全局并发控制: globalWorkPlanSem (可选)
  │
  ├─ 初始化: ExecutionContext{Vars, PrevOutput, Result, Metadata}
  │
  └─ 节点循环: currentID = entryID
       WHILE currentID != "":
         │
         ├─ graph.GetNode(currentID) → NodeRunner
         │     │
         │     └─ runner.Run(ctx, ec) → (output, error)
         │           ├─ autoRunner       → Agent Chat（支持模板渲染+工具过滤）
         │           ├─ controlRunner    → 透传（If/Switch 纯路由）
         │           ├─ loopRunner       → 循环体迭代 + Signal 实时推送
         │           ├─ forkRunner       → 多 Agent 并发 + JSON 汇合
         │           ├─ approveRunner    → 暂停等待人工决策
         │           ├─ checkpointRunner → 快照保存
         │           └─ emitRunner       → 变量写入
         │
         ├─ 节点结果写入 result.NodeResults（含 Output 字段）
         │
         └─ graph.resolve(currentID, ec) → nextID
               ├─ 无条件边 → 直接跟随
               └─ 条件边 → 按 Priority 排序，首个匹配的跟随

模板变量:
  {{.PrevResult}}   → fromJSON(ec.PrevOutput)
  {{.Vars.key}}     → fromJSON(ec.Vars[key])
```

### 2.3 工具调度数据流

```
LLM 返回 tool_calls → dispatchToolCalls
  │
  └─ 每个 tool_call:
       tool_holder.Dispatch(ctx, name, argsJSON)
         │
         ├─ mu.RLock() → toolMap[name] → O(1) 查找
         │
         └─ handler.Execute(ctx, argsJSON)
               ├─ HubToolHandler    → gRPC stream → microHub Skill
               ├─ MCPToolHandler    → client.CallTool → MCP Server
               └─ InlineToolHandler → fn(ctx, argsJSON)
```

### 2.4 Engine 启动流程

```
api.New(opts) → agent.New(opts)
  │
  ├─ 1. registry.Init(RegistryPath)     — 加载注册表
  ├─ 2. registry.ProbeAllOnStartup()    — 探测 skill 在线状态
  ├─ 3. config.LoadConfig(LLMConfigPath) — 加载 LLM 配置
  ├─ 4. llm.NewChatClient(llmCfg)       — 创建 HTTP 客户端
  ├─ 5. tool_holder.New()               — 创建工具注册中心
  ├─ 6. hub.ServeAsync(addr)            — 启动 gRPC（goroutine）
  ├─ 7. tool_holder.Register(hubProvider) — 注册 Hub 工具
  ├─ 8. health probe 启动（15s 间隔）
  └─ 9. Agent 就绪
```

---

## 3. 关键类型

### 3.1 ToolProvider 接口（v0.3 重构）

```go
// 1 个方法，替代旧 4 方法接口
type ToolProvider interface {
    ProviderName() string
    Tools() []ToolEntry        // 每次 LLM 调用前实时调用
}

// 工具条目 = 定义 + 处理器
type ToolEntry struct {
    Definition types.Tool       // LLM 可见的工具描述
    Handler    ToolHandler      // 执行策略
}

// 三种策略实现
type ToolHandler interface {
    Execute(ctx context.Context, argsJSON string) (string, error)
}
// HubToolHandler    — gRPC → microHub Skill
// MCPToolHandler    — stdio/SSE → MCP Server
// InlineToolHandler — Go 函数直接调用
```

### 3.2 WorkPlan 图引擎类型（v0.3 新增）

```go
// 图
type Graph struct {
    nodes map[string]NodeRunner
    edges []Edge
    entry string
}

// 边（一等公民）
type Edge struct {
    From      string
    To        string
    Condition EdgeCondition   // nil = 无条件边
    Priority  int             // 条件边优先级，0 最高
    Label     string
}

// 可执行节点
type NodeRunner interface {
    ID() string
    Run(ctx context.Context, ec *ExecutionContext) (string, error)
}

// 执行状态载体
type ExecutionContext struct {
    Vars       map[string]string
    PrevOutput string
    Result     *WorkPlanResult
    Metadata   map[string]any
}
```

### 3.3 消息类型

```go
type Message struct {
    Role             string      // system / user / assistant / tool
    Content          *string
    ReasoningContent string
    ToolCalls        []ToolCall
    ToolCallID       string
    Name             string
}
```

### 3.4 上下文配置

```go
type ContextConfig struct {
    MaxTokens          int  // 硬上限，默认 8192
    CompressThreshold  int  // 压缩触发阈值，默认 6144
    MaxToolResultChars int  // 单条 tool 结果最大字符数，默认 4000
}
```

---

## 4. 并发模型

| 组件 | 并发策略 |
| ---- | -------- |
| tool_holder.Holder | `sync.RWMutex` 保护 toolMap + providers |
| Agent | `mcpMu` 保护 mcpProvider 延迟初始化 + shutdown channel 防竞态 |
| HubProvider | 读写锁保护 retired 集合 |
| MCPProvider | `sync.RWMutex` 保护 servers map |
| InlineProvider | `sync.RWMutex` 保护 tools map |
| dispatchToolCalls | sem chan（max 5）限制并发 |
| forkRunner | sem chan（max 3）限制并发分支 + panic recovery |
| WorkPlan | `sync.RWMutex` 保护 vars（Fork 并发写入） |
| Signal | `sync.RWMutex` 保护 value/iter/cbs，`sync.Once` 保护 done |
| Graph | `sync.RWMutex` 保护 nodes/edges |
| globalWorkPlanSem | `sync.Mutex` 保护 channel 读写 |

---

## 5. 已知问题

### Bug（待修复）

| # | 严重度 | 问题 | 位置 |
| -- | ------ | ---- | ---- |
| B1 | 🔴 | `Shutdown()` 未停止 hub gRPC 服务，goroutine 泄漏 | `core/agent/agent.go` |
| B2 | 🔴 | dispatch goroutine 无 panic recovery — handler panic 导致 WaitGroup 永久阻塞 | `core/session/dispatch.go` |
| B3 | 🔴 | MCP `Attach()` 失败时 client 已创建但未 Close — 子进程泄漏 | `provider/mcp_provider.go` |
| B4 | 🟡 | Chat/ChatStream ReAct 循环体 ~80% 重复 | `core/session/chat.go` |
| B5 | 🟡 | `bufio.Scanner` 与 `handleApproval` 第二个 scanner 抢缓冲区 | `sdk/cli/repl.go` |
| B6 | 🟡 | MCP handler 将业务错误包装为 `ErrToolUnavailable` | `provider/mcp_handler.go` |
| B7 | 🟡 | `ToolCallTimeOut` 拼写错误（应为 Timeout） | `core/agent/agent.go` |
| B8 | 🟡 | 压缩 prompt 硬编码英文，不支持多语言 | `history/context_compress.go` |
| B9 | 🟡 | `EstimateTokens = len(text)/3` 对中英文估算都不精确 | `history/context_limit.go` |

### 设计改进

| # | 问题 | 位置 |
| -- | ---- | ---- |
| D1 | 两套执行引擎并存（`primitive.go` + `runner.go`），应统一到 runner | `workplan/` |
| D2 | `graph.Execute()` 完美实现但无人调用，`plan.go:Run()` 手动调度 | `workplan/` |
| D3 | `sdk/api/seele_api.go` 纯 type alias，零抽象价值 | `sdk/api/` |
| D4 | `EngineFactory` 命名不当 — 实际创建 Session，非 Agent | `sdk/cluster/harness.go` |
| D5 | `go 1.25.5` 版本过高，应降为 1.23 | `go.mod` |
| D6 | `go-sql-driver/mysql` 依赖但全项目零引用 | `go.mod` |

### 已修复（v0.3 重构）

| # | 问题 | 状态 |
| -- | ---- | ---- |
| ✅ | ToolProvider 4 方法接口臃肿 → 1 方法 + Handler 策略 | 已修复 |
| ✅ | tool_holder O(n) 遍历 → map O(1) 分发 | 已修复 |
| ✅ | `_` 前缀工具 dispatch 路由失败 | 已修复 |
| ✅ | WorkPlan 线性链表 → Graph + Edge 图引擎 | 已修复 |
| ✅ | `NodeResult.Output` 从未赋值 → FinalOutput 永远空 | 已修复 |
| ✅ | `MCP()` 无锁并发 → mcpMu + shutdown channel | 已修复 |
| ✅ | `RegisterInlineTool` API 新增 | 已修复 |
| ✅ | `SchemaOf` struct → JSON Schema 自动生成 | 已修复 |
| ✅ | B11 `New()` 失败时 hub goroutine 泄漏 | 已修复 |
| ✅ | B3 `Chat()` nil Content panic | 已修复 |
| ✅ | B8 `SetMaxConcurrentWorkPlans` data race | 已修复 |
| ✅ | `Config.LoadConfig` 默认值补全 | 已修复 |
| ✅ | `tool_holder.Dispatch` 重试可配置 | 已修复 |

---

## 6. 目录结构

```
Seele/
├── config/loader.go                # YAML 配置加载
├── core/
│   ├── agent/
│   │   ├── agent.go                # Agent 编排层 (New/Shutdown/Hub/MCP/RegisterInlineTool)
│   │   ├── session.go              # NewSession / QuickChat / DirectDispatch
│   │   └── pool.go                 # 多会话池
│   ├── session/
│   │   ├── interface.go            # ToolDispatcher / ApprovalCallback
│   │   ├── session.go              # Holder (历史/配置/生命周期)
│   │   ├── chat.go                 # Chat/ChatStream ReAct 循环
│   │   └── dispatch.go             # dispatchToolCalls + resolveApproval
│   └── tool_holder/
│       ├── holder.go               # Holder (map 中转 + rebuild)
│       ├── provider.go             # Register/Unregister
│       └── tools.go                # Tools (聚合+_前缀过滤) / Dispatch (O(1)+重试)
├── provider/
│   ├── tool_provider.go            # ToolProvider 接口 (1方法) + ToolHandler + ToolEntry
│   ├── Hub_provider.go             # HubProvider (gRPC 工具管理)
│   ├── hub_handler.go              # HubToolHandler (gRPC 执行策略)
│   ├── hub_router.go               # hubRouter (microHub 路由实现)
│   ├── mcp_provider.go             # MCPProvider (stdio/SSE 连接管理)
│   ├── mcp_handler.go              # MCPToolHandler (MCP 执行策略)
│   ├── inline_provider.go          # InlineProvider (Go 函数工具管理)
│   ├── inline_handler.go           # InlineToolHandler (Go 函数执行策略)
│   └── schema.go                   # SchemaOf (struct→JSON Schema 反射生成)
├── history/
│   ├── context_compress.go         # LLM 摘要压缩
│   └── context_limit.go            # Token 估算、硬截断、工具结果截断
├── llm/chat_client.go              # OpenAI 兼容 HTTP 客户端（同步+流式）
├── types/model.go                  # Message, Tool, Config 等共享类型
├── workplan/
│   ├── plan.go                     # WorkPlan struct + Run/Resume
│   ├── graph.go                    # Graph + Edge + ExecutionContext + NodeRunner
│   ├── runner.go                   # 6 种 Runner 实现
│   ├── sugar.go                    # 构建期语法糖 (Auto/If/Loop/Fork/...)
│   ├── node.go                     # node/Signal/SwitchCase/ForkBranch/NodeResult
│   ├── gate.go                     # ApprovalGate 接口 + CLI/Network/Auto 实现
│   ├── validate.go                 # 拓扑校验（DFS 三色环检测）
│   └── primitive.go                # 旧执行引擎（待废弃，已迁至 runner.go）
├── sdk/
│   ├── api/seele_api.go            # 类型别名 (Engine = agent.Agent)
│   ├── cli/
│   │   ├── repl.go                 # 交互式 REPL + 审批 UI
│   │   └── prompt_loader.go        # 系统提示词热加载（fsnotify）
│   └── cluster/
│       ├── harness.go              # Harness.Run (多 Agent 部署)
│       └── handler.go              # AgentHandler (gRPC → WorkPlan 路由)
├── test/                           # 集成测试
├── example_Implement/              # 使用示例
│   ├── config/                     # 共享配置模板
│   ├── 01_hello_seele/             # 最简入门
│   ├── 02_inline_tools/            # 内联工具深度演示
│   ├── 03_workplan/                # WorkPlan 完整演示
│   └── 04_mcp/                     # MCP 集成
└── docs/
    ├── plan/                       # 重构方案文档
    └── review/                     # 架构审查文档
```

## 7. 外部依赖

| 包 | 用途 |
| -- | ---- |
| `github.com/RedHuang-0622/microHub` | 内部 gRPC 微服务框架 |
| `github.com/mark3labs/mcp-go` | MCP 协议客户端 |
| `gopkg.in/yaml.v3` | YAML 解析 |
| `github.com/fsnotify/fsnotify` | 文件监听（prompt 热加载） |

---

## 8. 扩展点

- **新增工具来源**：实现 `ToolProvider` 接口（1 方法），注入 `engine.Tools().Register()`
- **替换审批门**：实现 `workplan.ApprovalGate` 接口 → 注入 `workplan.New()`
- **自定义 NodeRunner**：实现 `NodeRunner` 接口，通过 `graph.AddNode()` 注入图引擎
- **Schema 扩展**：`SchemaOf` 的 `typeToSchema` 可扩展支持 `$ref`/`oneOf`/`anyOf`
