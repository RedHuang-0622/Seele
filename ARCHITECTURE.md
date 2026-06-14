# Seele 框架架构文档

> 生成时间: 2026-05-20
> 用途: bug 大修改运动前的基线参考

---

## 1. 总览

```
┌──────────────────────────────────────────────────────────────────────┐
│                        sdk/api (Engine)                              │
│  生命周期: New() → 使用 → Shutdown()                                  │
│  职责: 组装 Runtime + HubProvider + MCPProvider                       │
├──────────────────────────────────────────────────────────────────────┤
│                        sdk/Cluster (Harness)                         │
│  职责: 多 Agent 进程管理、注册表合并、gRPC 服务化                       │
├──────────────────────────────────────────────────────────────────────┤
│                     core/runtime (编排层)                             │
│  职责: 管理 []ToolProvider、工具聚合、dispatch 路由、Agent 工厂        │
├───────────────┬──────────────────────┬───────────────────────────────┤
│  core/agent   │  provider/           │  workplan/                    │
│  ReAct 循环    │  ToolProvider 接口    │  工作流 DAG 引擎              │
│  对话管理      │  HubProvider         │  Auto/Approve/If/Switch/      │
│  上下文压缩    │  MCPProvider          │  Loop/Fork/Checkpoint/Emit    │
├───────────────┴──────────────────────┴───────────────────────────────┤
│  llm/chat_client      history/              types/model              │
│  OpenAI 兼容 HTTP     上下文压缩/截断        共享类型                   │
│  同步+流式两种模式     Token 估算                                         │
└──────────────────────────────────────────────────────────────────────┘
```

## 2. 核心数据流

### 2.1 Agent.Chat() — ReAct 循环

```
用户输入
  │
  ▼
agent.Chat(ctx, userInput)
  │
  ├─ 1. 追加 user message 到 history
  │
  ├─ 2. runtime.tools() → 聚合所有 provider 的工具列表
  │      ├─ HubProvider.Tools()  → registry.GetOnlineTools() → []Tool
  │      └─ MCPProvider.Tools()  → 各 MCP Server 的 list_tools → []Tool
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
       │      └─ 失败 → TrimHistory 硬截断（从旧消息开始丢弃）
       │
       ├─ 3b. llm.Complete(history, tools)
       │      POST {BaseURL}/chat/completions
       │      Body: {model, messages, tools, max_tokens, temperature}
       │
       ├─ 3c. 解析回复
       │      ├─ 无 tool_calls → 返回 Content 文本 ✓ 循环结束
       │      └─ 有 tool_calls → 追加 assistant 消息到 history
       │
       ├─ 3d. dispatchToolCalls(ctx, toolCalls)
       │      并发执行（最多 5 并发）:
       │      FOR each tool_call:
       │        runtime.dispatch(name, argsJSON)
       │          └─ 遍历 providers，找第一个 HasTool(name)=true 的
       │               ├─ HubProvider.Dispatch   → gRPC → Skill 进程
       │               └─ MCPProvider.Dispatch   → CallTool → MCP Server
       │      结果追加为 tool 消息到 history
       │      瞬时错误 (ErrToolUnavailable) → 最多重试 3 次（间隔 2s）
       │      业务错误 → 包装为 {"error":"..."} 注入 history
       │
       └─ 3e. 工具列表刷新（每轮重新读取，支持热更新）
              tools = filteredTools(runtime.tools())
  │
  ▼
返回最终文本 或 超出 maxLoops 报错
```

### 2.2 Agent.ChatStream() — 流式 ReAct 循环

```
与 Chat() 流程相同，差异在 LLM 调用和结果处理:

llm.CompleteStream(history, tools, onChunk)
  │
  ├─ POST {BaseURL}/chat/completions  (stream: true)
  ├─ 逐行读 SSE: "data: {...}"
  ├─ processFrame(payload, state, onChunk)
  │     ├─ tool_call 帧 → state.isToolMode=true, 累积到 tcMap
  │     ├─ 文本帧 → state.sb.WriteString(delta), onChunk(delta) 实时推送
  │     └─ 思索帧 → state.reasoningSB 累积
  └─ 返回 (fullContent, reasoningContent, toolCalls, nil)

Agent 侧处理:
  ├─ 文本轮次: 将缓冲的 chunks 全部推送给 onChunk → 追加 history → 返回
  └─ tool_call 轮次: 丢弃缓冲的 chunks → 追加 history (仅结构化 tool_calls)
                        → dispatchToolCalls → 继续循环
```

### 2.3 工具调度数据流

```
LLM 返回 tool_calls
  │
  ▼
dispatchToolCalls(ctx, toolCalls)
  │
  ├─ 并发执行 (sem chan 限制 5 并发)
  │
  └─ 每个 tool_call:
       runtime.dispatch(ctx, name, argsJSON)
         │
         ├─ 遍历 providers (按注册顺序)
         │     │
         │     ├─ HubProvider.HasTool(name) → true?
         │     │     ├─ registry.SelectToolByName(name)
         │     │     ├─ 检查 retired 集合
         │     │     ├─ json.Valid 校验参数
         │     │     ├─ pb_api.Request().Method(t.Method).Params(argsJSON).Build()
         │     │     └─ hub.Dispatch(ctx, req)
         │     │           └─ registryRouter.Execute → 按 method 匹配 tool addr
         │     │               └─ gRPC stream call → Skill 进程
         │     │
         │     └─ MCPProvider.HasTool(name) → true?
         │           ├─ resolveRoute(name) → (serverName, toolName)
         │           ├─ json.Unmarshal(argsJSON)
         │           ├─ mcp.CallToolRequest{Name: toolName, Arguments: args}
         │           └─ client.CallTool(ctx, req)
         │
         └─ 结果处理:
               ├─ OK → 追加 tool 消息到 history
               ├─ ErrToolUnavailable → 跳过此条 → 整体重试
               └─ 其他错误 → {"error":"..."} 注入 history
```

### 2.4 Engine 启动流程

```
api.New(opts)
  │
  ├─ 1. registry.Init(opts.RegistryPath)
  │      读取 registry.yaml → 解析 tools 列表
  │      ├─ 每个 tool: {name, method, addr, description, input_schema}
  │      └─ 连接池参数 (pool 段)
  │
  ├─ 2. registry.ProbeAllOnStartup()
  │      └─ 检测所有已注册 skill 进程是否在线
  │
  ├─ 3. registry.StartHealthProbe(ctx, 15s)
  │      └─ 后台定时探测，自动标记 online/offline
  │
  ├─ 4. hub := hubbase.New(registryRouter)
  │      go hub.ServeAsync(addr)  → 启动 gRPC server
  │
  ├─ 5. config.LoadConfig(llmConfigPath)
  │      读取 config.yaml (agent 段) → LLMConfig
  │
  ├─ 6. core.NewRuntime(llmCfg)
  │      创建 ChatClient (http.Client)
  │
  ├─ 7. provider.NewHubProvider(hub, timeout)
  │      runtime.Register(hubProvider)
  │
  └─ 8. Engine 就绪
```

### 2.5 WorkPlan 执行引擎流程

```
wp.Run(ctx)
  │
  ├─ 全局并发控制: globalWorkPlanSem (可选)
  │
  ├─ 初始化: vars = {}, prevJSON = ""
  │
  └─ 节点循环: currentID = entryID
       WHILE currentID != "":
         │
         ├─ primitiveRunNode(ctx, n, prevJSON, result)
         │     │
         │     ├─ 渲染输入模板: {{.PrevResult}} / {{.Vars.key}} → 实际值
         │     │
         │     └─ switch n.kind:
         │           ├─ Auto:      agent.Chat(ctx, input) → toJSON(out)
         │           ├─ Approve:   两步: Agent 生成计划 → Gate.Request 等人确认 → 执行
         │           ├─ If/Switch: 不执行 Agent, 透传 prevJSON, 仅做路由
         │           ├─ Loop:      循环体 agent.Chat → signal.set() → until() 检查
         │           ├─ Fork:      并发多个 agent.Chat → JSON object 汇合
         │           ├─ Join:      占位, Fork 已处理
         │           ├─ Checkpoint: 保存输出快照到 result.Checkpoints
         │           └─ Emit:      写入 vars[key] = prevJSON
         │
         ├─ 节点结果加入 result.NodeResults
         │
         └─ primitiveNext(n, prevJSON) → currentID
               ├─ If:     cond(prev) → trueID 或 falseID
               ├─ Switch: 顺序匹配 cases → nextID
               ├─ Loop:   signal.Iter() >= maxIter → exhaustedID, 否则 next
               └─ 其他:   n.next

WorkPlan 变量传递机制:
  {{.PrevResult}}   → fromJSON(prevJSON)   上一节点输出去 JSON 引号
  {{.Vars.key}}     → fromJSON(vars[key])  Emit 节点写入的命名变量

fromJSON(s): 若 s 是 JSON string → 返回内容; 否则返回原始 s
toJSON(s):   若 s 已是合法 JSON → 直接返回; 否则 json.Marshal 包裹
```

### 2.6 上下文压缩流程

```
NeedCompression(history, threshold)
  └─ EstimateHistoryTokens(history) > threshold
       │
       ▼
CompressHistory(ctx, llm, history, maxTokens)
  │
  ├─ 1. splitSystem(history) → sys + rest
  │
  ├─ 2. 若 len(rest) <= 4 → 跳过压缩，直接硬截断
  │
  ├─ 3. keep = rest[-4:]           (最近 4 条)
  │      compressible = rest[:-4]   (可压缩部分)
  │
  ├─ 4. buildCompressInput(compressible)
  │      user 消息 → "User: ..."
  │      tool_calls → "Called name(args)"
  │      tool 结果 → "Result from name: ..." (截断到 800 字符)
  │
  ├─ 5. callCompressLLM(ctx, llm, input)
  │      ⚠ BUG: 直接修改共享 client.Cfg.MaxTokens/Temperature
  │      临时设为 MaxTokens=300, Temperature=0.3
  │      以 system prompt 要求生成 ≤150 词摘要
  │
  ├─ 6. 组装: sys + [摘要注入为 system 消息] + keep
  │
  └─ 7. 若压缩后仍超限 → TrimHistory 硬截断

Token 估算:
  EstimateTokens(text) = (len(text) + 2) / 3   ← 按 UTF-8 字节数估算
  EstimateMessageTokens(msg) = 10 + content + reasoning + tool_calls + name + id
  EstimateHistoryTokens(msgs) = Σ EstimateMessageTokens

Tool 结果截断:
  TruncateToolResult(content, maxChars)
    content 长度 ≤ maxChars → 原样返回
    否则 → 截断 + "...[truncated]" 标记
    优先在换行符处断开
```

### 2.7 Cluster Harness 流程

```
cluster.Run(wfMap, cfg)
  │
  ├─ 1. 环境变量读取
  │      AGENT_ROLE (必填)
  │      REGISTRY_PATH → HUB_REGISTRY → TOOLS_REGISTRY → LLM_CONFIG
  │      优先级: HarnessConfig > 环境变量 > 默认值
  │
  ├─ 2. 解析个性注册表 registry_{role}.yaml
  │      AgentRegistry{Role, Provides, Uses, Peers, Network}
  │
  ├─ 3. BuildAgentRegistry(reg, hubRegistry, toolsRegistry)
  │      ├─ 从 registry.tools.yaml 按 uses.tools 白名单筛选原子工具
  │      ├─ 从 registry.yaml 按 peers capability 筛选 Agent 路由
  │      ├─ 连接池参数优先取 toolsRegistry, fallback 到 hubRegistry
  │      └─ 写入 /tmp/seele-agent-{role}.yaml
  │
  ├─ 4. seeleapi.New(opts) → Engine
  │
  ├─ 5. EngineFactory 适配为 workplan.AgentFactory
  │
  ├─ 6. NewAgentHandler(role, reg, factory, wfMap)
  │      实现 microHub tool.Handler 接口
  │      Execute(req) → 启动 goroutine → 从 wfMap 路由到具体工作流
  │
  └─ 7. tool.New(handler).Serve(port)
        启动 gRPC server，阻塞等待 Dispatch
```

---

## 3. 关键类型

### 3.1 消息类型

```
Message {
    Role             string      // "system" | "user" | "assistant" | "tool"
    Content          *string     // 消息正文，可能为 nil（tool_calls 时）
    ReasoningContent string      // 思索文段
    ToolCalls        []ToolCall  // assistant 发起的工具调用
    ToolCallID       string      // tool 消息对应的调用 ID
    Name             string      // tool 消息的工具名
}

ToolCall {
    ID       string            // 唯一标识
    Type     string            // "function"
    Function ToolCallFunction{ Name, Arguments }
}
```

### 3.2 Provider 接口

```
ToolProvider interface {
    ProviderName() string
    Tools() []Tool                            // 每次 LLM 调用前实时调用
    Dispatch(ctx, name, argsJSON) (string, error)
    HasTool(name string) bool
}
```

### 3.3 WorkPlan 节点类型

```
NodeKind:
  kindAuto       (0) → Agent ReAct 循环
  kindApprove    (1) → 两阶段: 计划生成 + 人工确认 + 执行
  kindIf         (2) → 条件二选一分支
  kindSwitch     (3) → 多路条件分支
  kindLoop       (4) → 带 Signal 的循环
  kindFork       (5) → 多 Agent 并发
  kindJoin       (6) → Fork 结果汇合
  kindCheckpoint (7) → 输出快照
  kindEmit       (8) → 写入命名变量
```

### 3.4 上下文配置

```
ContextConfig {
    MaxTokens          int  // 硬上限，默认 8192
    CompressThreshold  int  // 压缩触发阈值，默认 6144
    MaxToolResultChars int  // 单条 tool 结果最大字符数，默认 4000
}
```

---

## 4. 并发模型

| 组件 | 并发策略 |
|------|----------|
| Runtime | `sync.RWMutex` 保护 providers 切片，读多写少 |
| Agent | 不加锁，同一 Agent 不应跨 goroutine 调用 |
| HubProvider | 两个锁: `mu` 保护 retired, `mu2` 保护 toolIndex |
| MCPProvider | `sync.RWMutex` 保护 servers map |
| dispatchToolCalls | sem chan (max 5) 限制并发数 |
| primitiveFork | sem chan (max 3) 限制并发分支数 |
| WorkPlan | `sync.RWMutex` 保护 vars (Fork 中有并发写入) |
| Signal | `sync.RWMutex` 保护 value/iter/cbs, `sync.Once` 保护 done channel |
| ChatClient | **无锁 (⚠ 问题)** — Cfg 字段被压缩调用直接修改 |

---

## 5. 已知 BUG 清单 (待修复)

### #1 — ChatClient 压缩时并发写 Cfg 字段

- **文件**: `history/context_compress.go:148-152`
- **严重程度**: 🔴 高
- **问题**: `callCompressLLM` 直接修改共享的 `client.Cfg.MaxTokens` 和 `client.Cfg.Temperature`，无锁保护，多 Agent 并发时产生竞态
- **影响**: Agent A 的压缩参数漏到 Agent B 的正常 LLM 调用中
- **建议**: 压缩时创建临时 ChatClient 副本，或将 MaxTokens/Temperature 作为请求参数传入

### #2 — primitiveFork 中 toolFilter nil 被强制覆盖为空切片

- **文件**: `workplan/plan.go:355-362`
- **严重程度**: 🟡 中
- **问题**: `n.toolFilter == nil` (不限工具) 时被设为 `[]string{}` (无任何工具)，语义反转
- **影响**: Fork 分支中的 Agent 在未显式设置 toolFilter 时无法使用任何工具
- **建议**: 移除对 nil 的覆盖，或只在 non-nil 时才调用 SetToolFilter

### #3 — SetMaxLoops 注释与默认值不一致

- **文件**: `core/agent.go:62-63`
- **严重程度**: 🟢 低
- **问题**: 注释写 "默认值为 8"，实际 `NewAgent` 中默认 `loopTimes = 4`
- **影响**: 误导调用方
- **建议**: 统一注释与实际默认值

### #4 — Token 估算公式不精确

- **文件**: `history/context_limit.go:68-73`
- **严重程度**: 🟡 中
- **问题**: `EstimateTokens = len(text)/3`，对中文偏高，对英文偏低
- **影响**: 英文对话可能过早触发压缩，浪费 LLM 调用
- **建议**: 按 rune 计数或引入 BPE tokenizer

### #5 — primitiveApprove 中审批通过后重新创建 Agent

- **文件**: `workplan/plan.go:260-261`
- **严重程度**: 🟢 低
- **问题**: 计划阶段和执行阶段各创建一个 Agent，执行 Agent 无法看到计划阶段的 tool 调用结果
- **影响**: 如果计划阶段调用了工具获取上下文，执行阶段会丢失这些上下文
- **建议**: 复用同一个 Agent 或把计划结果注入执行 Agent 的上下文

---

## 6. 目录结构

```
Seele/
├── config/
│   └── loader.go            # YAML 配置加载
├── core/
│   ├── agent.go              # Agent ReAct 循环、对话管理
│   └── runtime.go            # Provider 管理、工具调度、Agent 工厂
├── history/
│   ├── context_compress.go   # LLM 摘要压缩
│   └── context_limit.go      # Token 估算、硬截断、工具结果截断
├── llm/
│   └── chat_client.go        # OpenAI 兼容 HTTP 客户端 (同步+流式)
├── provider/
│   ├── tool_provider.go      # ToolProvider 接口定义
│   ├── Hub_provider.go       # microHub gRPC 工具提供者
│   └── mcp_provider.go       # MCP 协议工具提供者 (stdio/SSE)
├── sdk/
│   ├── api/
│   │   └── seele_api.go      # Engine (组装层入口)
│   └── Cluster/
│       ├── handler.go        # AgentHandler (gRPC → WorkPlan 路由)
│       ├── harness.go        # Harness.Run (Agent 进程完整生命周期)
│       └── registry.go       # 个性注册表解析 + 注册表合并
├── types/
│   └── model.go              # Message, Tool, Config 等共享类型
├── util/
│   ├── cmd_running.go        # 子进程管理
│   └── yaml_reading.go       # Viper YAML 读取
├── workplan/
│   ├── plan.go               # WorkPlan.Run + primitive 执行引擎
│   ├── gate.go               # ApprovalGate 接口 + CLI/Auto 实现
│   ├── node.go               # node 结构体 + Signal + 结果类型
│   └── sugar.go              # 构建期语法糖 (Auto/If/Loop/Fork/...)
├── test/
│   ├── helpers_test.go       # mock Provider + mock LLM Server
│   ├── limit_test.go         # 上下文限制测试
│   ├── pool_test.go          # AgentPool 测试
│   ├── probe_test.go         # 健康探测测试
│   └── workplan_test.go      # WorkPlan 引擎测试
└── example_Implement/
    ├── quick_start/main.go
    ├── mcp_using/main.go
    ├── skill_example/main.go
    └── work_flow_example/main.go
```

## 7. 外部依赖

| 包 | 用途 |
|----|------|
| `github.com/mark3labs/mcp-go` | MCP 协议客户端 |
| `github.com/RedHuang-0622/microHub` | 内部 gRPC 微服务框架 (BaseHub, registry, pb_api) |
| `gopkg.in/yaml.v3` | YAML 解析 |
| `github.com/spf13/viper` | util 层的 YAML 读取 (仅 util/yaml_reading.go 使用) |

## 8. 扩展点

- **替换 ApprovalGate**: 实现 `workplan.ApprovalGate` 接口 → 注入 `workplan.New()`
- **新增配置来源**: 修改 `config.LoadConfig` 支持环境变量/远程配置
