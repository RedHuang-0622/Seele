# Seele v0.5 → v1.0 差距分析 & 路线图

> 基于 2026-07 主流 Agent 开发框架（LangGraph、CrewAI、Pydantic AI、Dify、OpenAI Agents SDK、Vercel AI SDK）横向对比，梳理 Seele 的竞争优势和短板，制定优先级路线图。
> 
> 状态：Draft | 更新：2026-07-08

---

## 1. 现状打分

```
你的 WorkPlan 编排能力       ■■■■■■■■■■ 10/10 — 和 LangGraph 同级的图引擎
你的架构整洁度               ■■■■■■■■■■ 10/10 — 零循环依赖，优于所有 Python 框架
你的 Go 性能优势             ■■■■■■■■■■ 10/10 — 编译型，无 GIL，goroutine 原生并发
你的 Human-in-the-Loop       ■■■■■■□□□□  6/10 — 有 Approve/Gate/Checkpoint，缺持久化
你的 Streaming 完整度        ■■■■□□□□□□  4/10 — text-only，无 StreamEvent 类型体系
你的 LLM Provider 覆盖面     ■■■□□□□□□□  3/10 — 只有 OpenAI 协议族
你的可观测性                 ■■□□□□□□□□  2/10 — log.Printf 无法上生产
你的状态后端多样性           ■□□□□□□□□□  1/10 — 只有 JSON 文件存储
你的 Guardrails              ■□□□□□□□□□  1/10 — 只有 Approve 审批节点
你的文档（英文）             ■□□□□□□□□□  1/10 — 中文为主，英文覆盖不足
你的 RAG/数据接入能力        □□□□□□□□□□  0/10 — 不存在
你的生态/社区                □□□□□□□□□□  0/10 — 单人项目
```

**核心判断**：WorkPlan 的编排能力是 Seele 的最强护城河，与 LangGraph 同级且远超 CrewAI/Pydantic AI。但 LLM 覆盖面、可观测性、状态持久化、RAG 四条腿缺了三条，导致这个引擎跑不动生产负载。

---

## 2. 差距项清单（按优先级排列）

### P0 — 必须补齐

| # | 差距项 | 现状 | 目标 | 预估工时 |
|---|--------|------|------|----------|
| G1 | **LLM Provider 接口化** | `api.ChatClient` 硬编码 OpenAI `/chat/completions` | Provider 接口 + OpenAI/Anthropic/Gemini/Ollama 实现 | 2-3 天 |
| G2 | **结构化可观测性** | `log.Printf` + 手动 `Infof`/`Errorf` | slog/zerolog 结构化日志 + 全链路追踪 + 指标导出 | 1-2 天 |
| G3 | **状态后端接口化** | JSON 文件硬编码写死（`contexts/storage/Store` + `FileCache`） | Store/Cache 接口 + SQLite 后端 + PostgreSQL 后端 | 2-3 天 |

### P1 — 重要能力

| # | 差距项 | 现状 | 目标 | 预估工时 |
|---|--------|------|------|----------|
| G4 | **StreamEvent 类型体系** | `ChatStream` 回调 `func(string)` 纯文本 | 事件类型：text / tool_call / reasoning / error / done | 0.5 天 |
| G5 | **输出 Guardrail / Validator** | 无 | 复用 `SchemaOf` 做 `ResultValidator`，LLM 输出不符合 schema 自动重试 | 1 天 |
| G6 | **表单化 Human-in-the-Loop** | Approve 节点内存态，不持久化 | Append → Save checkpoint → 外部 API 异步审批 + Resume | 1-2 天 |
| G7 | **基础 RAG pipeline** | 无 | `rag.Retriever` 接口 + chromem-go 嵌入式向量检索 + 上下文注入 | 2-3 天 |

### P2 — 值得做

| # | 差距项 | 现状 | 目标 | 预估工时 |
|---|--------|------|------|----------|
| G8 | **英文文档 + examples** | README 中文为主，example 不全 | 英文 README、godoc 全覆盖、每个 WorkPlan 节点可运行 example | 1-2 天 |
| G9 | **Streaming 暴露 tool/reasoning** | tool call 阶段静默 | `StreamEvent` 含 tool_use/rasoning 事件，前端可见中间状态 | 0.5 天 |
| G10 | **Agent eval 框架** | 无 | `eval.Scenario` + `eval.Runner` + `eval.Reporter` | 2-3 天 |
| G11 | **智能 LLM 路由** | round-robin + priority | latency-based / cost-based / fallback chain | 1 天 |

### P3 — 锦上添花

| # | 差距项 | 现状 | 目标 | 预估工时 |
|---|--------|------|------|----------|
| G12 | **Prompt 模板/版本化管理** | 无 | `prompt.Template` + 变量注入 + 版本标识 | 1 天 |
| G13 | **多模态输入** | text-only | image/audio/file attachment → Content 类型扩展 | 1-2 天 |
| G14 | **OpenTelemetry 集成** | 无 | OTLP trace/metric/log 三支柱导出 | 1 天 |
| G15 | **gRPC API Server** | library-only | `cmd/seele-server/` 提供 gRPC Agent 服务 | 2-3 天 |
| G16 | **WebSocket 流式 push** | HTTP Client 回调 | ws/wss 双向流式通信，支持 Server Push | 1 天 |

---

## 3. 各项详细分析

### G1 — ProviderStrategy 传输层策略

**背景**：当前 `api.ChatClient` 硬编码 OpenAI `/v1/chat/completions`，包括 endpoint 路径、请求/响应结构体、SSE 帧格式、认证头全部写死。`function.Strategy` 只覆盖 tool call 编解码格式，不解决传输层差异。Anthropic 的 `/v1/messages`（不同 endpoint、不同请求结构、`x-api-key` 认证、`event:` 行而非 `data:` 行的 SSE）、Gemini 的 `/v1/models/{model}:generateContent` 完全不通。

**方案**：延续代码库已有的 Strategy 模式（`function.Strategy`、`react.CompletionStrategy`），在 `api.ChatClient` 中注入传输层策略。ChatClient 保持 HTTP 生命周期管理（超时、重试、连接池），策略只处理协议差异。

```
ChatClient（HTTP 编排器，共享逻辑）
  │
  ├── strategy ProviderStrategy ← 按 Account.ProviderType 选择
  │       ├── BuildRequest(…) ([]byte, error)       ← 请求体序列化
  │       ├── ParseResponse([]byte) (Message, error) ← 响应体反序列化
  │       ├── SSEHeaders() map[string]string         ← 流式请求头
  │       ├── ParseSSEFrame(string) (SSEEvent, error)← SSE 帧解析
  │       ├── Endpoint() string                      ← /chat/completions vs /v1/messages
  │       └── AuthHeader(apiKey) (string, string)    ← Authorization vs x-api-key
  │
  ├── OpenAIStrategy（现有 ChatClient 逻辑提取）
  ├── AnthropicStrategy（新增）
  ├── GeminiStrategy（新增）
  └── OllamaStrategy（新增）
```

```go
// agent/core/api/strategy.go — 新增

// ProviderStrategy 处理 LLM API 传输层协议差异。
// ChatClient 负责 HTTP 编排，策略只处理格式转换。
type ProviderStrategy interface {
    Name() string                                                    // "openai" | "anthropic" | "gemini"
    Endpoint() string                                                // 相对路径，如 "/chat/completions"
    BuildRequest(model string, messages []Message, tools []Tool, stream bool) ([]byte, error)
    ParseResponse(body []byte) (Message, error)
    SSEHeaders() map[string]string                                   // 流式额外请求头
    ParseSSEEvent(eventType string, payload string) (SSEEvent, error)// event type + data → 结构化事件
    AuthHeader(apiKey string) (string, string)                       // key → header
}

type SSEEvent struct {
    Type    SSEEventType  // text / tool_call / reasoning / done / error
    Content string
    ToolCallIndex int
    Meta    map[string]any
}

// 全局注册（复用 function.Strategy 已有的 Register/Get/Nacos 模式）
var providerStrategies sync.Map

func RegisterProviderStrategy(s ProviderStrategy) { ... }
func GetProviderStrategy(name string) ProviderStrategy { ... }
```

**与 function.Strategy 的关系**：`function.Strategy` 保留，变成 `ProviderStrategy` 内部使用的子策略。`ProviderStrategy.BuildRequest` 内部调用 `function.Get(name).EncodeTools(tools)` 做 tool 格式转换。两层策略各自独立演化。

**注意**：现有 `api.Account.ProviderType`（`"openai" | "anthropic"`）直接对应 ProviderStrategy 注册名。号池 `GetByProvider()` 已经按 provider 筛选，ChatClient 按 account 的 ProviderType 选择策略：

```go
func (c *ChatClient) effectiveStrategy(acct *Account) ProviderStrategy {
    name := "openai"  // 默认
    if acct != nil && acct.Provider != "" {
        name = string(acct.Provider)
    }
    return GetProviderStrategy(name)
}
```

**为什么 Strategy 优于 Factory**（回应你的判断）：
- **与代码库已有模式一致**：`function.Strategy`、`react.CompletionStrategy` 都是同一套 `Register` + 按名查找。再加一层 ProviderStrategy 是自然扩展。
- **ChatClient 保持 HTTP 编排职责**：超时、重试、连接池、上下文传播——这些是跨 provider 共享的。Factory 模式会把它们复制到每个 Provider 实现里。
- **按需组合**：`ProviderStrategy` 内部可以选择不同的 `function.Strategy`。OpenAIStrategy 固定用 openai FC，AnthropicStrategy 用 anthropic FC，GeminiStrategy 初期先用 openai FC（Gemini 兼容 OpenAI 格式）——组合而非继承。

---

### G2 — 结构化可观测性

**背景**：当前 Logger 接口只有 `Infof`/`Errorf`，输出散落在标准错误流。生产环境无法关联追踪、无法聚合指标、无法回溯故障根因。

**方案**：替换为 `slog`（Go 1.21 标准库），最小破坏性：

```go
// 现状
type Logger interface {
    Infof(format string, args ...interface{})
    Errorf(format string, args ...interface{})
}
// → 替换为 slog.Logger，同时向后兼容

// 每条 Agent 调用输出结构化事件
slog.Info("agent_step",
    "session_id", sid,
    "step", step,
    "elapsed_ms", elapsed,
    "tokens", tokens,
    "tool_calls", len(tc),
    "error", err,
)
```

**监控维度**：
| 指标 | 来源 | 用途 |
|------|------|------|
| `step_latency_ms` | `NodeResult.StartedAt/EndedAt` | 瓶颈识别 |
| `tool_call_count` | dispatch 计数 | 工具使用分析 |
| `token_per_step` | LLM response 估算 | 成本归因 |
| `loop_exhaustion_rate` | ReAct loop maxIter 触发次数 | Agent 空转检测 |
| `error_rate_by_tool` | dispatch error | 工具稳定性 |

OTLP 导出是 P3 项，但结构化日志是生产"最低配置"，列为 P0。

---

### G3 — 状态后端接口化

**背景**：`contexts/storage/Store` 和 `contexts/cache/FileCache` 都硬编码文件系统。FileCache 的 `sync.Map` index 进程内唯一，多实例部署无法共享。

**方案**：

```go
// contexts/store.go — 已有雏形
type Store interface {
    Save(ctx context.Context, key string, data []byte) error
    Load(ctx context.Context, key string) ([]byte, error)
    List(ctx context.Context, prefix string) ([]string, error)
    Delete(ctx context.Context, key string) error
}

// 实现
- FileStore       ← 现有 JSON 文件实现（单机默认）
- SQLiteStore     ← modernc.org/sqlite，纯 Go 零 CGo（单机多进程）
- RedisStore      ← go-redis（多实例共享）
- PostgresStore   ← pgx（生产级）

// contexts/cache/provider.go 同理
type CacheProvider interface {
    Get(ctx context.Context, key string) ([]byte, bool)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    Clear(ctx context.Context) error
}
```

**价值**：补齐存储多样性后，Seele 才能支撑容器化多实例部署。这是上生产的前提。

---

### G4 — StreamEvent 类型体系

**背景**：`ChatStream` 的 `onChunk func(string)` 回调只接收 text delta，tool call 阶段和 reasoning 阶段完全静默。用户面对 10 秒黑屏不知道 Agent 在干什么。

**方案**：

```go
type StreamEventType int

const (
    EventText      StreamEventType = iota  // 文本 delta
    EventToolCall                           // 工具调用（start + delta + end）
    EventToolResult                         // 工具返回结果
    EventReasoning                          // 推理内容（reasoning_content）
    EventError                              // 错误
    EventDone                               // 完成
)

type StreamEvent struct {
    Type    StreamEventType
    Content string          // text delta 或 tool_name 或 error string
    Index   int             // tool_call index（多 tool call 并发时区分）
    Meta    map[string]any  // 扩展信息
}
```

**非破坏性迁移**：
```go
// 旧签名（保留，内部转调新签名）
func (e *Engine) ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)

// 新签名
func (e *Engine) ChatStreamV2(ctx context.Context, input string, onEvent func(StreamEvent)) (string, error)
```

---

### G5 — 输出 Guardrail / Validator

**背景**：当前 Agent 的最终输出无结构化约束。Pydantic AI 的核心竞争力之一就是 `result_validator`：定义 output schema → LLM 输出不满足时自动 retry → 返回类型安全的结果。

**方案**：复用已有的 `SchemaOf` 机制。

```go
type ResultValidator struct {
    Schema   map[string]interface{}  // JSON Schema（复用 SchemaOf 生成的格式）
    MaxRetry int                     // 默认 2
    OnInvalid func(output string, err error) string  // 修正提示
}
```

集成到 Engine ReAct loop：
```
LLM 原始输出 → Validate(Schema) → 通过 → 返回
                                → 不通过 → 重试 prompt（注入"输出必须符合以下 JSON Schema"）
```

**价值**：这是 Pydantic AI 的杀手锏之一，工程成本极低（已有 SchemaOf，缺的是 loop 集成），但能极大提升 Seele 的生产可信度。

---

### G6 — 表单化 Human-in-the-Loop

**背景**：当前 Approve 节点是内存态的，`Signal.Get()` 阻塞等待。进程重启后 pending approval 全部丢失。

**方案**：

```go
// WorkPlan.Approve 流程
// 1. 执行到 ApproveNode → Save checkpoint 到 Store
// 2. 设置状态为 AwaitingApproval（持久化，含 sessionID + nodeID + input snapshot）
// 3. 外部 API：POST /v1/approve { session_id, node_id, decision: "approve" | "reject" }
// 4. Resume(sessionID): Load checkpoint + 根据 decision 走 true/false 分支

// workplan/plan.go
func (wp *WorkPlan) Resume(ctx context.Context, sessionID string, decision ApprovalDecision) error
```

**Approve 持久化的价值**：这是 LangGraph 的 HITL 核心能力——你可以审批、回滚、重放。有了这个，Seele 才能做异步审批工作流（如"通知主管 → 主管半小时后审批 → 继续执行"）。

---

### G7 — 基础 RAG Pipeline

**背景**：没有数据接入能力是 Seele 与 LlamaIndex/Dify/LangChain 最大的功能鸿沟。

**方案**：嵌入式向量检索（零外部依赖）：

```go
// contexts/rag/retriever.go
type Retriever interface {
    Store(ctx context.Context, docs []Document) error
    Query(ctx context.Context, question string, topK int) ([]Document, error)
}

// Document 分块 + embedding + 检索
type Document struct {
    ID      string
    Content string
    Meta    map[string]string  // 来源、时间等
}
```

推荐后端：`chromem-go`（Go 原生嵌入式向量数据库，零外部依赖，纯 Go 实现）。

**集成到 Agent**：在 ReAct loop 的 system prompt 中主动注入检索结果，或作为隐式 Tool（`_retrieve`）由 LLM 按需调用。

**注意**：不要求一步到位做到 LlamaIndex 的 160+ 连接器。先做一个能跑通 "文档 → chunk → embedding → search → context injection" 的 pipeline，验证 RAG 在 Seele 下的体验。

---

### G8-G16

| 编号 | 项 | 说明 | 依赖 |
|------|----|------|------|
| G8 | 英文文档 + examples | README 英文版 + godoc + 每个节点类型的可运行 example | G1 G2 G3 完成后 |
| G9 | Streaming 暴露 tool/reasoning | G4 的 UX 层，把 StreamEvent 传到前端 | G4 |
| G10 | Agent eval 框架 | 定义 Scenario → 跑 → Report，用于回归和 benchmark | G1 |
| G11 | 智能 LLM 路由 | 不再 round-robin，按延迟/成本/成功率选号 | G1 |
| G12 | Prompt 模板 | `{{.Context}}` / `{{.Tools}}` 模板注入，支持版本追踪 | 无 |
| G13 | 多模态 | image/audio 作为 Message.Content 的子类型 | G1 |
| G14 | OTLP 集成 | 把结构化日志导出为 OTLP trace + metric | G2 |
| G15 | gRPC API Server | 提供 `cmd/seele-server` 让 Seele 作为独立服务运行 | G3 |
| G16 | WebSocket 流式 | Server Push 替代 HTTP polling | G4 |

---

## 4. 优先级执行路线图

```
Phase 1（P0 — 生产底座）               Phase 2（P1 — 竞争力）              Phase 3（P2 — 生态和体验）
──────────────────────────────          ──────────────────────              ──────────────────────────
┌─ G1: LLM Provider 接口化  ─┐         ┌─ G4: StreamEvent 类型体系  ─┐     ┌─ G8:  英文文档+examples  ─┐
│  G2: 结构化可观测性        │         │  G5: 输出 Guardrail       │     │  G10: Agent eval 框架    │
│  G3: 状态后端接口化        │         │  G6: HITL 表单化持久化    │     │  G11: 智能 LLM 路由      │
└────────────────────────────┘         │  G7: 基础 RAG pipeline    │     │  G14: OTLP 集成          │
                                       └────────────────────────────┘     └──────────────────────────┘

  Week 1-2                                Week 3-5                           Week 6-8
```

### Phase 1 依赖关系

```
G1 (Provider 接口) ──────────→ G11 (智能路由)
    │
    └──→ G10 (Eval 框架)      ← 需要 Provider 接口稳定
G2 (可观测性)     ──────────→ G14 (OTLP 导出)
G3 (状态后端接口) ──────────→ G6 (HITL 持久化)
    │
    └──→ G15 (gRPC Server)

G4 (StreamEvent)  ──────────→ G9 (UX 层暴露)
G5 (Guardrail)    ──────────→ 无依赖，可独立开发
G7 (RAG)          ──────────→ G3 的 CacheProvider 接口复用
```

---

## 5. 差异化竞争策略

Seele 不应该试图在所有维度上追平 LangGraph/Dify，而是强化以下"只有 Go 能做到"的独特定位：

### 核心卖点

| 卖点 | 说明 | 竞品对比 |
|------|------|----------|
| **WorkPlan ToTool** | 子图作为工具嵌套调用 | LangGraph 无原生支持，需手写 wrapper |
| **Fork 精确并发控制** | goroutine-pool-limited + semaphore | Python 框架做不到这个精度 |
| **Plugin 工具可见性** | include/exclude glob + `_hidden` 前缀 | CrewAI/LangGraph 无此粒度 |
| **单二进制部署** | 编译后 15MB，零依赖 | Python 框架 image 2GB+，cold start 5s+ |
| **CGo-free 嵌入** | Go 纯编译，可嵌入任意 Go 应用 | Python 框架需要完整 Python runtime |

### 建设优先级原则

1. **先做 P0 再做 P1** — G1/G2/G3 是生产底座，缺一个就是玩具
2. **RAG 做嵌入不做连接器矩阵** — chromem-go 200 行够用，不学 LlamaIndex 的 160 个连接器
3. **可观测性用 slog + OTLP，不自研看板** — 接 Grafana 就够，不做 Dify 式自带 UI
4. **Guardrail 复用 SchemaOf 不做自研规则引擎** — 借力 Go 的类型系统，比 Python 框架还干净
5. **文档只做英文，不做多语言** — 英文覆盖全球 90% 开发者

---

## 6. 附录：竞品对比基准

| 能力项 | Seele | LangGraph | CrewAI | Pydantic AI | Dify | OpenAI SDK |
|--------|-------|-----------|--------|-------------|------|-------------|
| 图编排 | 10/10 | 10/10 | 4/10 | 5/10 | 6/10 | 4/10 |
| 架构整洁 | 10/10 | 6/10 | 5/10 | 8/10 | 6/10 | 8/10 |
| 性能/延迟 | 10/10 | 7/10 | 4/10 | 7/10 | 5/10 | 8/10 |
| Provider 覆盖 | 3/10 | 9/10 | 6/10 | 8/10 | 8/10 | 4/10 |
| 可观测性 | 2/10 | 9/10 | 5/10 | 7/10 | 7/10 | 6/10 |
| 状态持久化 | 1/10 | 9/10 | 4/10 | 3/10 | 9/10 | 3/10 |
| RAG | 0/10 | 8/10 | 2/10 | 3/10 | 9/10 | 1/10 |
| HITL | 6/10 | 9/10 | 1/10 | 2/10 | 5/10 | 3/10 |
| Guardrails | 1/10 | 5/10 | 3/10 | 9/10 | 6/10 | 7/10 |
| 文档/生态 | 1/10 | 9/10 | 8/10 | 6/10 | 8/10 | 8/10 |
| 流式体验 | 4/10 | 7/10 | 4/10 | 5/10 | 6/10 | 7/10 |
| 部署便利 | 9/10 | 5/10 | 5/10 | 6/10 | 9/10 | 7/10 |

**结论**：Seele 缺乏的不是核心能力（编排 + 架构），而是**生产外围能力**（Provider 覆盖面 + 可观测性 + 状态持久化 + RAG）。补齐这四项 P0-P1 后，Seele 将从 "一个人写的酷引擎" 变成 "能上生产的 Go Agent 框架"。
