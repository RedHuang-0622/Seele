# Seele 框架架构分析

## 一、完整数据流图

```
┌─────────────────────────────────────────────────────────────────────┐
│                         表达层 (Presentation)                         │
│                                                                       │
│  ┌─────────────────┐     ┌─────────────────┐                         │
│  │  REPL (CLI)     │     │  Web / API      │                         │
│  │  sdk/cli/repl   │     │  (未来扩展)      │                         │
│  └───────┬─────────┘     └────────┬────────┘                         │
│          │                        │                                   │
│          │ ① 用户输入              │                                   │
│          │    OnApproval 回调      │                                   │
│          ▼                        ▼                                   │
├─────────────────────────────────────────────────────────────────────┤
│                       SDK / API 层                                    │
│                                                                       │
│  ┌──────────────────────────────────────────────┐                    │
│  │  Engine  (sdk/api/seele_api)                  │                    │
│  │  ┌──────────────────────────────────────┐    │                    │
│  │  │  QuickChat / QuickChatStream         │    │                    │
│  │  │  DirectDispatch (绕过 LLM 调工具)     │    │                    │
│  │  │  Skills() 摘要                       │    │                    │
│  │  └──────────────┬───────────────────────┘    │                    │
│  └─────────────────┼────────────────────────────┘                    │
│                    │                                                 │
│                    │ ② NewAgent(systemPrompt, maxLoops)              │
│                    ▼                                                 │
├─────────────────────────────────────────────────────────────────────┤
│                        核心层 (Core)                                  │
│                                                                       │
│  ┌──────────────────────────────────────────────────────┐           │
│  │  Runtime  (core/runtime)                              │           │
│  │  ┌──────────────────────────────────────────┐        │           │
│  │  │  providers: []ToolProvider               │        │           │
│  │  │  llm: *ChatClient                        │        │           │
│  │  │  tools() → 聚合所有 provider 工具列表     │        │           │
│  │  │  Dispatch(name, argsJSON) → 路由到 tool  │        │           │
│  │  └──────────────────┬───────────────────────┘        │           │
│  └─────────────────────┼────────────────────────────────┘           │
│                        │                                             │
│  ┌─────────────────────┼──────────────────────────────────────┐     │
│  │  Agent  (core/agent)  │                                      │     │
│  │  ┌────────────────────┴───────────────────────────────────┐ │     │
│  │  │  Chat(userInput) → for loop < maxLoops:                 │ │     │
│  │  │    ③ LLM.Complete(history, tools) → msg                 │ │     │
│  │  │    ④ if no ToolCalls → return text                       │ │     │
│  │  │    ⑤ dispatchToolCalls(msg.ToolCalls)                    │ │     │
│  │  │       ├─ 普通 tool: 结果注入 history                      │ │     │
│  │  │       └─ awaiting_approval: 走 OnApproval 回调            │ │     │
│  │  │    ⑥ loop 继续 → LLM 看到 tool 结果 → 继续推理            │ │     │
│  │  └────────────────────────────────────────────────────────┘ │     │
│  │                                                              │     │
│  │  resolveApproval() — 审批循环（LLM 无感知）:                   │     │
│  │    ⑦ OnApproval(ctx, approvalJSON) → choice key              │     │
│  │    ⑧ Dispatch("_decide", {question_id, choice})              │     │
│  │    ⑨ 若仍 awaiting_approval → goto ⑦ (嵌套审批)              │     │
│  │    ⑩ 最终结果注入 history                                     │     │
│  └──────────────────────────────────────────────────────────┘     │
│                                                                    │
│  ┌──────────────────────────────────────────┐                     │
│  │  Context Management  (history/)           │                     │
│  │  CompressHistory() — LLM 压缩早期记录      │                     │
│  │  TrimHistory()    — 硬截断保底             │                     │
│  │  TruncateToolResult() — 工具结果截断       │                     │
│  └──────────────────────────────────────────┘                     │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              │ Dispatch → gRPC
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│                       Provider 层                                     │
│                                                                       │
│  ┌─────────────────────────────────────────────────────┐            │
│  │  ToolProvider 接口  (provider/)                       │            │
│  │  ├─ Tools() → []Tool                                 │            │
│  │  ├─ Dispatch(ctx, name, argsJSON) → string           │            │
│  │  └─ HasTool(name) → bool                             │            │
│  └──────────────────────┬──────────────────────────────┘            │
│                         │                                            │
│  ┌──────────────────────┴──────────────────────────────┐            │
│  │  HubProvider  (provider/Hub_provider)                │            │
│  │  封装 microHub service_registry                       │            │
│  │  - Tools(): 读 registry → 过滤 _ 前缀工具 → 转换 schema │        │
│  │  - Dispatch(): 查 registry → 构建 gRPC 请求 → 派发    │          │
│  └──────────────────────┬──────────────────────────────┘            │
└─────────────────────────┼──────────────────────────────────────────┘
                          │ gRPC / microHub
                          ▼
┌─────────────────────────────────────────────────────────────────────┐
│                     工具层 (Tools + Workflows)                        │
│                                                                       │
│  ┌───────────┐ ┌──────────────┐ ┌───────────┐ ┌───────────┐        │
│  │ host_shell │ │file_writer   │ │chart_sbox │ │web_search │        │
│  │  (:50230)  │ │  (:50250)    │ │ (:50260)  │ │ (:50240)  │        │
│  │ 宿主机原生  │ │ 宿主机原生     │ │宿主机原生  │ │ Docker    │        │
│  └───────────┘ └──────────────┘ └───────────┘ └───────────┘        │
│                                                                       │
│  ┌───────────┐ ┌──────────────┐                                      │
│  │ mysql_ops │ │script_sbox   │     Docker 容器                      │
│  │ (:50210)  │ │  (:50220)    │                                      │
│  │ Docker    │ │ Docker       │                                      │
│  └───────────┘ └──────────────┘                                      │
│                                                                       │
│  ┌─────────────────────────────────────────────────────┐            │
│  │  Model Workflow Agent  (:51111)   sdk/cluster        │            │
│  │  ┌───────────────────────────────────────┐          │            │
│  │  │  AgentHandler (handler.go)             │          │            │
│  │  │  ├─ wfMap: {"model_report"→fn, ...}   │          │            │
│  │  │  ├─ Execute(): 路由到 workflow 或 _decide  │       │            │
│  │  │  ├─ sendQuestion(): 构建 approval 响应     │       │            │
│  │  │  └─ handleDecide(): 恢复暂停的 workflow     │       │            │
│  │  └───────────────────────────────────────┘          │            │
│  │                                                      │            │
│  │  Harness (harness.go): Run() 启动 gRPC 服务          │            │
│  │  注入 NetworkApprovalGate, 管理 pausedExecutions     │            │
│  └──────────────────────┬──────────────────────────────┘            │
│                         │                                            │
│                         ▼                                            │
│  ┌─────────────────────────────────────────────────────┐            │
│  │  WorkPlan 执行引擎  (workplan/)                       │            │
│  │  ┌───────────────────────────────────────┐          │            │
│  │  │  WorkPlan (plan.go)                    │          │            │
│  │  │  ├─ Run() → 遍历 nodes → 执行           │          │            │
│  │  │  ├─ Approve 节点: prepareApprove()     │          │            │
│  │  │  │   → Plan Agent 生成计划              │          │            │
│  │  │  │   → pauseSnapshot 保存状态           │          │            │
│  │  │  │   → 返回 PausedWorkPlan              │          │            │
│  │  │  └─ Resume() → executeApprove() → 继续  │          │            │
│  │  └───────────────────────────────────────┘          │            │
│  │                                                      │            │
│  │  Gate 接口 (gate.go)                                  │            │
│  │  ├─ NetworkApprovalGate: gRPC 模式，两阶段协议         │            │
│  │  ├─ CLIApprovalGate: fmt.Scanln 本地输入               │            │
│  │  └─ AutoApproveGate: 自动通过，无需用户交互             │            │
│  └─────────────────────────────────────────────────────┘            │
└─────────────────────────────────────────────────────────────────────┘
```

### 关键数据流路径

| 编号 | 路径 | 说明 |
|:---:|------|------|
| ① | 用户输入 → REPL → Agent.Chat | 普通对话入口 |
| ③④ | Agent → LLM → 文本回复 | 正常推理循环 |
| ③⑤⑥ | Agent → LLM → tool_call → Dispatch → 结果 → LLM | ReAct 循环 |
| ⑤→⑦⑧⑨ | Dispatch 返回 awaiting_approval → OnApproval → _decide → 恢复 → 循环 | **审批旁路（LLM 无感知）** |
| ⑧ | dispatchToolCalls → Runtime.Dispatch → HubProvider → gRPC → AgentHandler | _decide 恢复工作流 |
| WP | AgentHandler.Execute → WorkPlan.Run → Approve 暂停 → sendQuestion → 返回 | **工作流暂停** |

---

## 二、依赖分析

### 2.1 包级依赖图

```
model_agent/cmd/cli/
    └── sdk/cli/          (REPL, PromptLoader, handleApproval)
    └── sdk/api/          (Engine)

model_agent/cmd/workflow/
    └── sdk/cluster/      (Harness, AgentHandler)
    └── model_agent/workflows/  (ModelReportWorkflow 等)

model_agent/tools/*/
    └── microHub          (pb_api, root_class/tool)

sdk/api/
    └── core/             (Runtime, Agent)
    └── provider/         (HubProvider)
    └── microHub          (registry, hub)

sdk/cluster/
    └── workplan/         (WorkPlan, Gate, Node)
    └── microHub          (pb_api, root_class/tool)

core/
    └── llm/              (ChatClient, HTTP API 调用)
    └── provider/         (ToolProvider 接口)
    └── history/          (上下文压缩、截断)
    └── types/            (Message, Tool, LLMConfig)

provider/
    └── types/            (Tool, SkillInfo)
    └── microHub          (registry, jsonSchema)

workplan/
    └── core/             (通过 AgentFactory 创建 Agent)
    └── types/            (Message)

history/
    └── types/            (Message)
    └── llm/              (压缩时调用 LLM)
```

### 2.2 依赖方向

```
                    ┌──────────────┐
                    │    types     │  ← 纯数据结构，零依赖
                    └──────┬───────┘
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │  llm     │ │ history  │ │ provider │  ← 工具层契约
        └────┬─────┘ └────┬─────┘ └────┬─────┘
             │            │            │
             └────────────┼────────────┘
                          ▼
                    ┌──────────┐
                    │   core   │  ← 运行时：Agent + Runtime
                    └────┬─────┘
                         │
              ┌──────────┼──────────┐
              ▼          ▼          ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │ sdk/api  │ │sdk/cluster│ │workplan  │  ← SDK + 工作流
        └────┬─────┘ └────┬─────┘ └──────────┘
             │            │
             ▼            ▼
        ┌──────────────────────────┐
        │  model_agent/cmd/*       │  ← 应用入口（样例）
        └──────────────────────────┘
```

**核心原则**：
- `types` 最底层，只有数据结构，不依赖任何内部包
- `core` 依赖 `llm` + `history` + `provider`，是运行时中枢
- `sdk/*` 依赖 `core`，向外提供 API 和工作流引擎
- `model_agent/*` 是使用者 / 样例，依赖 `sdk/*`
- **无循环依赖**

---

## 三、类图

### 3.1 Agent + Runtime

```
┌──────────────────────────────────────────────────────┐
│                     Runtime                           │
│                   (core/runtime)                      │
├──────────────────────────────────────────────────────┤
│  - llm: *ChatClient                                  │
│  - providers: []ToolProvider                         │
│  - mu: sync.RWMutex                                  │
├──────────────────────────────────────────────────────┤
│  + NewRuntime(cfg LLMConfig) → (*Runtime, error)     │
│  + Register(p ToolProvider)                          │
│  + Unregister(name string)                           │
│  + NewAgent(systemPrompt, loopTimes) → *Agent        │
│  - tools() → []Tool                                  │
│  + Dispatch(ctx, name, argsJSON) → (string, error)   │
└──────────────────────┬───────────────────────────────┘
                       │ creates
                       ▼
┌──────────────────────────────────────────────────────┐
│                      Agent                            │
│                    (core/agent)                        │
├──────────────────────────────────────────────────────┤
│  - runtime: *Runtime                                  │
│  - sessionID: string                                  │
│  - history: []Message                                 │
│  - maxLoops: int          (default: 4)                │
│  - contextCfg: ContextConfig                           │
│  - toolFilter: []string    (nil = 不限制)              │
│  - lastCompressLoop: int                              │
│  + OnApproval: ApprovalCallback  (审批回调)            │
├──────────────────────────────────────────────────────┤
│  + SessionID() → string                               │
│  + History() → []Message                              │
│  + ClearHistory()                                     │
│  + UpdateSystemPrompt(newPrompt string)               │
│  + MaxLoops() → int                                   │
│  + SetMaxLoops(n int)                                 │
│  + ContextConfig() → ContextConfig                    │
│  + SetContextConfig(cfg ContextConfig)                │
│  + SetToolFilter(filter []string)                     │
│  + ForceAppendHistory(msg Message)   // 仅测试用       │
│  + Chat(ctx, userInput) → (string, error)             │
│  + ChatStream(ctx, userInput, onChunk) → (string, err)│
│  - dispatchToolCalls(ctx, toolCalls)                  │
│  - resolveApproval(ctx, json, qID) → (string, error)  │
│  - filteredTools(all) → []Tool                        │
└──────────────────────────────────────────────────────┘
                       │
                       │ uses
                       ▼
┌──────────────────────────────────────────────────────┐
│              ApprovalCallback                          │
│              (core/agent)                              │
├──────────────────────────────────────────────────────┤
│  type ApprovalCallback = func(                        │
│      ctx context.Context,                             │
│      approvalJSON string,                             │
│  ) (choice string, err error)                         │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│            ToolProvider (interface)                    │
│              (provider/)                               │
├──────────────────────────────────────────────────────┤
│  + ProviderName() → string                            │
│  + Tools() → []Tool                                   │
│  + Dispatch(ctx, name, argsJSON) → (string, error)    │
│  + HasTool(name) → bool                               │
└──────────────────────────────────────────────────────┘
                       △
                       │ implements
                       │
┌──────────────────────────────────────────────────────┐
│                   HubProvider                          │
│              (provider/Hub_provider)                   │
├──────────────────────────────────────────────────────┤
│  - hub: *BaseHub                                      │
│  - retired: map[string]struct{}                       │
│  - toolIndex: map[string]struct{}  // HasTool 快速查找  │
│  - toolCallTimeout: time.Duration                     │
├──────────────────────────────────────────────────────┤
│  + NewHubProvider(hub, timeout) → (*HubProvider, err) │
│  + ProviderName() → "microhub"                        │
│  + Tools() → []Tool    // 过滤 _ 前缀 + offline       │
│  + Dispatch(ctx, name, argsJSON) → (string, error)    │
│  + HasTool(name) → bool                               │
│  + Skills() → []SkillInfo                             │
│  + Retire(name) / Restore(name)                       │
└──────────────────────────────────────────────────────┘
```

### 3.2 WorkPlan 工作流引擎

```
┌──────────────────────────────────────────────────────┐
│                  AgentFactory                          │
│               (workplan/primitive)                     │
├──────────────────────────────────────────────────────┤
│  type AgentFactory = func(                            │
│      systemPrompt string,                             │
│      loopTimes int,                                   │
│  ) Agent                                              │
│                                                       │
│  Agent (interface):                                   │
│    Chat(ctx, input) → (string, error)                 │
│    SetToolFilter([]string)                            │
│    MaxLoops() / SetMaxLoops(n)                        │
│    ContextConfig() / SetContextConfig(cfg)             │
│    ForceAppendHistory(Message)                        │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                    WorkPlan                            │
│                  (workplan/plan)                       │
├──────────────────────────────────────────────────────┤
│  - factory: AgentFactory                              │
│  - gate: ApprovalGate                                 │
│  - systemPrompt: string                               │
│  - nodes: []Node                                      │
│  - vars: map[string]string                            │
│  - checkpoints: map[string]WorkPlanResult             │
│  - execState: ExecState                               │
│  - pauseSnapshot: *pauseSnapshot                      │
│  - pauseDecision: V (any)                             │
│  + PausedWorkPlan: *WorkPlan   // 运行中的暂停实例     │
├──────────────────────────────────────────────────────┤
│  + New(factory, gate, sysPrompt) → *WorkPlan          │
│  + auto/sugar methods:                                │
│    - Auto(id, input, opts...)                         │
│    - Approve(id, input, options, opts...)             │
│    - Gate(id, input, opts...)                         │
│    - Emit(id, key)                                    │
│    - Checkpoint(id)                                   │
│  + Run(ctx) → (*WorkPlanResult, error)                │
│  + Resume(ctx) → (*WorkPlanResult, error)             │
│  + PendingQuestion() → (Question, bool)               │
│  + SetDecision(v any)                                 │
└──────────────────────┬───────────────────────────────┘
                       │
                       │ builds
                       ▼
┌──────────────────────────────────────────────────────┐
│                     Node                               │
│                  (workplan/node)                       │
├──────────────────────────────────────────────────────┤
│  - id: string                                         │
│  - input: string                                      │
│  - kind: NodeKind  (auto / approve)                   │
│  - tools: []string   // 工具白名单                     │
│  - options: []NodeOption                              │
│  - approveOptions: []ChoiceOption                     │
│  - approveKVS: map[string]any                         │
│  - approveTimeout: time.Duration                      │
├──────────────────────────────────────────────────────┤
│  + buildKVS() → map[string]any                        │
│  + approvePlanPrompt() → string                       │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                   Question                             │
│                  (workplan/node)                       │
├──────────────────────────────────────────────────────┤
│  + ID: string         // "exec_xxx_approve_节点id"    │
│  + Content: string    // Plan Agent 生成的计划 JSON    │
│  + Options: []ChoiceOption                            │
│  + KVS: map[string]any  // Key → Value 映射（不序列化） │
│  + Timeout: time.Duration                             │
├──────────────────────────────────────────────────────┤
│  + DefaultChoice() → string                           │
│  + Resolve(choice) → (any, bool)                      │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                  ChoiceOption                          │
│                  (workplan/node)                       │
├──────────────────────────────────────────────────────┤
│  + Key: string          // "execute" / "skip" / "abort"│
│  + Label: string        // "执行" / "跳过" / "终止"     │
│  + Description: string                                │
│  + Style: string        // primary/secondary/danger    │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                ApprovalGate (interface)                │
│                  (workplan/gate)                       │
├──────────────────────────────────────────────────────┤
│  + Ask(ctx, Question) → (any, error)                  │
└──────────────────────────────────────────────────────┘
         △                      △                      △
         │ implements            │                      │
         │                       │                      │
┌────────┴──────────┐ ┌─────────┴───────────┐ ┌───────┴──────────┐
│NetworkApprovalGate │ │  CLIApprovalGate    │ │ AutoApproveGate  │
│   (gate.go:42)     │ │   (gate.go:162)     │ │  (gate.go:252)   │
├───────────────────┤ ├─────────────────────┤ ├──────────────────┤
│ gRPC 两阶段协议    │ │ fmt.Scanln 本地输入  │ │ 直接返回首选项 V  │
│ OnQuestion 回调    │ │ 格式化打印选项       │ │ 无人机交互        │
│ Decide(ch) 恢复    │ │                     │ │                  │
└───────────────────┘ └─────────────────────┘ └──────────────────┘
```

### 3.3 SDK / API 层

```
┌──────────────────────────────────────────────────────┐
│                     Engine                             │
│                 (sdk/api/seele_api)                    │
├──────────────────────────────────────────────────────┤
│  - hub: *BaseHub                                      │
│  - hubProvider: *HubProvider                          │
│  - llmCfg: LLMConfig                                  │
│  - toolCallTimeout: Duration                          │
├──────────────────────────────────────────────────────┤
│  + New(opts Options) → (*Engine, error)               │
│  + NewAgent(sysPrompt, loops) → *Agent                │
│  + QuickChat(ctx, sysPrompt, userInput) → (string,err)│
│  + QuickChatStream(ctx, sP, uI, onChunk) → (string,err)│
│  + DirectDispatch(ctx, name, argsJSON) → (string,err) │
│  + Skills() → []SkillInfo                             │
│  + Shutdown()                                         │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                   REPLOptions                          │
│                  (sdk/cli/repl)                        │
├──────────────────────────────────────────────────────┤
│  + Prompt: string                                     │
│  + SystemPrompt: string        // 兜底 prompt         │
│  + SystemPromptPath: string    // 热加载 prompt 文件     │
│  + Engine: *Engine                                   │
│  + Output: io.Writer                                  │
│  + Input: io.Reader                                   │
│  + Stream: bool                                       │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                  PromptLoader                          │
│               (sdk/cli/prompt_loader)                  │
├──────────────────────────────────────────────────────┤
│  - path: string                                       │
│  - content: string    // 缓存最新内容                   │
│  - watcher: *fsnotify.Watcher                         │
│  - done: chan struct{}                                │
├──────────────────────────────────────────────────────┤
│  + NewPromptLoader(path) → (*PromptLoader, error)     │
│  + Get() → string      // 最新内容                     │
│  + Reload() → (string, error) // 立即重读              │
│  + Stop()                                             │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                  AgentHandler                          │
│              (sdk/cluster/handler)                     │
├──────────────────────────────────────────────────────┤
│  - wfMap: WorkflowMap  // {"name" → fnFactory}        │
│  - gate: *NetworkApprovalGate                         │
│  - registryPath: string                               │
│  - llmConfigPath: string                              │
│  - executions: map[qID]pausedExecution                │
│  - mu: sync.Mutex                                     │
├──────────────────────────────────────────────────────┤
│  + NewHandler(wfMap, gate, opts) → *AgentHandler      │
│  + Execute(req) → (<-chan *ToolResponse, error)       │
│  - isDecideMethod(method) → bool                      │
│  - sendQuestion(q, wp) → builds approval JSON         │
│  - handleDecide(params) → dispatch _decide + Resume    │
│  - cleanExpired()   // TTL = 5 minutes                │
└──────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────┐
│                 HarnessConfig                          │
│              (sdk/cluster/harness)                     │
├──────────────────────────────────────────────────────┤
│  + Name: string                                       │
│  + Port: int              (default: 51111)            │
│  + RegistryPath: string                               │
│  + LLMConfigPath: string                              │
│  + MaxLoops: int          (default: 8)                │
├──────────────────────────────────────────────────────┤
│  + Run(wfMap WorkflowMap, cfg HarnessConfig)          │
│    → 创建 AgentHandler + NetworkApprovalGate          │
│    → 启动 microHub gRPC 服务                          │
│    → 注册 registry 并监听健康检查                       │
└──────────────────────────────────────────────────────┘
```

### 3.4 类型定义

```
types/model.go:

┌───────────────────┐    ┌───────────────────┐
│     Message        │    │     ToolCall       │
├───────────────────┤    ├───────────────────┤
│ Role: string       │    │ ID: string         │
│ ReasoningContent   │    │ Type: "function"   │
│ Content: *string   │    │ Function           │
│ ToolCalls: []      │    └────────┬──────────┘
│ ToolCallID: string │             │
│ Name: string       │             ▼
└───────────────────┘    ┌───────────────────┐
                          │ ToolCallFunction   │
┌───────────────────┐    ├───────────────────┤
│      Tool          │    │ Name: string       │
├───────────────────┤    │ Arguments: string   │
│ Type: "function"   │    └───────────────────┘
│ Function           │
└────────┬──────────┘
         │              ┌───────────────────┐
         ▼              │    SkillInfo       │
┌───────────────────┐   ├───────────────────┤
│   ToolFunction     │   │ Name: string       │
├───────────────────┤   │ Description        │
│ Name: string       │   │ Method: string     │
│ Description: string│   │ Addr: string       │
│ Parameters: map    │   └───────────────────┘
└───────────────────┘

┌───────────────────┐    ┌───────────────────┐
│    LLMConfig       │    │  ContextConfig     │
├───────────────────┤    ├───────────────────┤
│ BaseURL: string    │    │ MaxTokens: int     │
│ APIKey: string     │    │ CompressThreshold  │
│ Model: string      │    │ MaxToolResultChars │
│ MaxTokens: int     │    └───────────────────┘
│ Timeout: int       │
│ Temperature: float │
└───────────────────┘
```

---

## 四、审批决策完整流程

### 4.1 暂停：WorkPlan.Approve → awaiting_approval

```
WorkPlan.Run()
  → 碰到 Node{kind: approve}
  → prepareApprove(node):
      创建 Plan Agent → 生成执行计划 JSON → 构建 Question{Q,K,V}
  → wp.pauseSnapshot = {question, prevJSON, result, startedAt}
  → wp.execState = StateAwaitingApproval
  → wp.result.PausedWorkPlan = wp
  → 返回 paused WorkPlan

AgentHandler.Execute() 检测到 result.PausedWorkPlan != nil
  → sendQuestion():
      wp.PendingQuestion() → Question
      构建 JSON:
        {"status":"awaiting_approval",
         "question_id":"exec_xxx_approve_节点id",
         "content":"{plan JSON}",
         "options":[{"key":"execute","label":"执行"},...]}
      存入 h.executions[questionID]
      返回给 caller (→ hub → provider → agent)
```

### 4.2 拦截：Agent.dispatchToolCalls

```
dispatchToolCalls(toolCalls)
  → Runtime.Dispatch() 返回 {"status":"awaiting_approval",...}
  → parseApprovalQuestionID(result) → ("exec_xxx_...", true)
  → a.OnApproval != nil? → YES → 调用 resolveApproval()
  → awaiting_approval 内容不注入 LLM history
```

### 4.3 用户交互：REPL.handleApproval

```
handleApproval(out, in, approvalJSON)
  → JSON 解析 → 提取 content + options
  → 格式化输出:

     ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
     [审批] 工作流需要你的决定：
     {plan content}
     选项：
       [1] 执行 (execute)
       [2] 跳过 (skip)
       [3] 终止 (abort)
     请输入选项 key（execute / skip / abort）>

  → 读取用户输入
  → 匹配逻辑:
     空输入 → 返回 options[0].Key
     "1" → 索引解析 → options[0].Key
     "execute" → key 直接匹配
     "执行" → label 匹配（大小写不敏感）
  → 返回 choice key
```

### 4.4 恢复：resolveApproval → _decide → Resume

```
resolveApproval(ctx, approvalJSON, questionID)
  loop (防止嵌套审批):
    ① choice := a.OnApproval(ctx, current)   // REPL 返回 "execute"
    ② decideArgs := {"question_id": qID, "choice": choice}
    ③ result := a.runtime.Dispatch(ctx, "_decide", decideArgs)
         → HubProvider → gRPC → AgentHandler.isDecideMethod("_decide") = true
         → handleDecide({question_id, choice})
             question.Resolve("execute") → "execute" (V)
             exec.wp.SetDecision("execute")
             exec.wp.Resume(ctx)
                 executeApprove():
                   wp.pauseDecision = "execute" = ChoiceExecute
                   → 创建 execution Agent → 运行原始 node input
                   → 返回 output
                   → 继续执行后续 nodes
                 → 若再遇 Approve → 再次暂停 → sendQuestion()
                   → 返回新的 awaiting_approval
             → 删除 h.executions[questionID]
         → 返回恢复结果
    ④ if result is awaiting_approval → continue loop (嵌套审批)
    ⑤ else → return result  // 最终业务结果
  end loop

  最终结果注入 agent.history (只有最终结果，无审批中间状态)
  → Agent 主循环继续
  → LLM 看到最终结果，进行后续推理
```

### 4.5 时序图

```
REPL          Agent          Runtime      HubProvider    AgentHandler    WorkPlan
 │              │               │              │              │              │
 │─Chat(text)──→│               │              │              │              │
 │              │─LLM Complete─→│              │              │              │
 │              │←tool_call────│              │              │              │
 │              │─dispatch────→│              │              │              │
 │              │               │─gRPC────────→│─────────────→│              │
 │              │               │              │              │─Run()───────→│
 │              │               │              │              │              │
 │              │               │              │              │←Approve node │
 │              │               │              │              │  (pause)     │
 │              │               │              │              │              │
 │              │               │              │←sendQuestion │              │
 │              │               │←{"status":"awaiting_approval",...}         │
 │              │←result────────│              │              │              │
 │              │               │              │              │              │
 │              │ parseApproval │              │              │              │
 │              │ → OnApproval()│              │              │              │
 │←─────────────│              │              │              │              │
 │ handleApproval:              │              │              │              │
 │ 展示选项,读用户输入            │              │              │              │
 │ return "execute"             │              │              │              │
 │─────────────→│              │              │              │              │
 │              │─Dispatch─────→│─────────────→│─────────────→│              │
 │              │  _decide     │              │ _decide      │              │
 │              │               │              │              │─Resume()────→│
 │              │               │              │              │  execute     │
 │              │               │              │              │←─result──────│
 │              │               │←───────────────────────────│              │
 │              │←final result─│              │              │              │
 │              │               │              │              │              │
 │              │ 注入 history  │              │              │              │
 │              │ (仅最终结果)    │              │              │              │
 │              │─LLM Complete─→│              │              │              │
 │              │←text reply───│              │              │              │
 │←展示给用户────│              │              │              │              │
```

---

## 五、关键设计决策

| 决策 | 位置 | 理由 |
|------|------|------|
| `_` 前缀工具对 LLM 不可见 | [Hub_provider.go](provider/Hub_provider.go) | 框架内部工具（`_decide`）不应被 LLM 自主调用 |
| 审批结果不入 LLM context | [agent.go](core/agent.go) | 避免浪费 token，LLM 只看到最终结果 |
| REPL 硬编码选项解析 | [repl.go](sdk/cli/repl.go) | 支持 key/数字/label/空输入，容错且无需 LLM 翻译 |
| 嵌套审批自动循环 | [agent.go](core/agent.go) `resolveApproval` | 防止工作流多级审批时卡死 |
| Registry 热加载 | microHub viper watch | 改 YAML 自动生效，无需重启 |
| Prompt 文件热加载 | [prompt_loader.go](sdk/cli/prompt_loader.go) | fsnotify 监听，修改即生效 |
| chart_sandbox 宿主机原生 | [chart_sandbox/main.go](model_agent/tools/chart_sandbox/main.go) | 图片直接落盘，返回路径，零 base64 开销 |

---

## 六、遗漏关键层补充

### 6.1 Config 层 (`config/loader.go`)

```
config.LoadConfig(path) → LLMConfig      // 读取 config.yaml 的 agent 块
config.LoadAppConfig(path) → AppConfig    // 读取完整配置（agent + hub + registry）
```

AppConfig 结构：

```
AppConfig
├── LLM: LLMConfig       (yaml tag: "agent")
│   ├── BaseURL  string  (yaml: ai_url)
│   ├── Model    string  (yaml: ai_name)
│   ├── APIKey   string  (yaml: ai_api_key)
│   ├── MaxTokens int
│   ├── Timeout   int
│   └── Temperature float64
├── Hub: HubConfig       (yaml tag: "hub")
│   ├── Addr string      (default: :50051)
│   └── StartupDelayMs   (default: 100)
└── Registry: RegistryConfig (yaml tag: "registry")
    └── Path string       (default: ./config/registry.yaml)
```

### 6.2 LLM 层 (`llm/chat_client.go`)

```
ChatClient                          ← 纯 stdlib http.Client，无第三方 SDK
├── Cfg: LLMConfig
├── Client: *http.Client
├── Complete(ctx, messages, tools) → Message
│   └── POST /v1/chat/completions → 返回完整响应（含 ToolCalls）
└── CompleteStream(ctx, messages, tools, onChunk) → (content, reasoning, toolCalls, err)
    └── SSE 流式解析：文本 delta → onChunk，tool_call delta → 内部累积
    └── isToolMode 锁：一旦检测到 tool_call，抑制文本输出防止 JSON 碎片泄露
```

### 6.3 MCP Provider (`provider/mcp_provider.go`)

多 MCP 服务器热插拔支持，与 HubProvider 互补（一个连本地 gRPC 工具，一个连外部 MCP 服务器）：

```
MCPProvider
├── servers: map[string]*mcpServerConn    // 按 server name 管理多连接
├── Tools() → []Tool                       // 聚合所有连接的 MCP 服务器工具
│   └── 多服务器冲突处理：工具名加前缀 "serverName__toolName"
├── Dispatch(ctx, name, argsJSON) → string
│   └── splitToolName() 反解前缀 → 路由到正确的 MCP 连接
├── Attach(ctx, MCPServerConfig) error     // 运行时热挂载（stdio/sse）
├── Detach(name string)                    // 运行时卸载
├── RefreshTools(ctx, serverName) error    // 重新拉取工具列表
└── ServerNames() []string

MCPServerConfig
├── Name: string          // 唯一标识
├── Transport: "stdio"|"sse"
├── Command/Args/Env      // stdio 模式
└── URL string            // sse 模式
```

### 6.4 WorkPlan 原语执行引擎 (`workplan/primitive.go`)

9 种节点类型的执行原语：

| 原语 | 行为 |
|------|------|
| `primitiveAuto` | 创建 Agent → `Chat(input)` → 返回 JSON 字符串 |
| `prepareApprove` | 创建 Plan Agent → 生成结构化执行计划 → 构建 Question(K-V) → 暂停 |
| `executeApprove` | 根据 `pauseDecision`：skip/abort/execute → 执行节点内容 |
| `primitiveIf` | 执行 `ifCond(prevJSON)` → true/false 分支路由 |
| `primitiveSwitch` | 遍历 `cases` → 首个 `Match==true` → 跳到对应 node |
| `primitiveLoop` | 迭代执行 body node → 更新 Signal → 检查 `loopUntil/loopMaxIter` |
| `primitiveFork` | 并发启动多个 Agent（上限 3）→ 合并结果为 `{"label": result, ...}` |
| `primitiveEmit` | 保存 node 输出到 `wp.vars[key]`（模板渲染用） |
| `primitiveCheckpoint` | 快照当前状态到 `wp.checkpoints[id]` |

模板渲染：`primitiveRenderInput` 支持 `{{.PrevResult}}`（上一个节点输出）和 `{{.Vars.key}}`（Emit 保存的变量）。

### 6.5 上下文压缩 (`history/context_compress.go`)

```
CompressHistory(ctx, client, history, maxTokens) → compressedHistory

算法：
1. 分离 system 消息 + 保留最近 4 条非 system 消息
2. 防止拆分 assistant(tool_call)/tool_result 配对
3. 将早期消息序列化为文本 → 调用压缩 LLM（无 tools, T=0.3, max=300 tokens）
4. 摘要作为 system 消息插入 → system + [摘要] + 近期消息
5. 若压缩后仍超限 → fallback 到 TrimHistory（硬截断）
```

### 6.6 AgentPool — 多 Agent 会话池

REPL 支持切换多个 Agent 会话，每个有独立的 history 和 system prompt：

```
AgentPool
├── agents: []*namedAgent    // {label, agent}
├── current: int              // 当前活跃 index
├── Add(label, systemPrompt) → int    // 新建会话
├── Switch(idx) error                 // 切换会话
├── Current() → *Agent               // 获取当前
├── All() → []AgentSummary            // 列出所有
└── Chat/ChatStream → 透传到当前 Agent
```

### 6.7 WorkPlan 验证 (`workplan/validate.go`)

`Run()` 启动前自动调用 `Validate()`：

1. 检查至少有一个 node
2. Loop 必须有 bodyID + 终止条件（Until 或 MaxIter）
3. Fork 至少有一个 branch
4. 所有目标 node 引用必须存在（If/Switch/Loop/Fork 的 target IDs）
5. **DFS 三色算法**检测环（白/灰/黑），Loop body 边除外（循环体允许回边）
