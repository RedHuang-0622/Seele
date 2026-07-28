# 可观测性系统实现方案 — Trace Tree

## 设计目标

1. 以**节点树**（Span Tree）结构记录 Agent 每次 `Chat/ChatStream` 的全链路追踪
2. 最小侵入：通过 `engine.Option` 注入，默认 Noop 零开销
3. 粒度：ReAct 循环 → LLM 调用 → 工具调度 → 缓存操作，逐层嵌套
4. 关键指标：token 计数、延迟、状态、错误信息
5. 输出：结构化 JSON lines，可被外部系统消费

## 设计模式选择

| 模式 | 语言实现 | 应用位置 | 理由 |
|------|---------|---------|------|
| Strategy | Go interface | `tracer.Tracer` | 多实现可互换（Noop/Simple） |
| Builder | Functional Options | `engine.WithTracer()` | 非侵入式注入 |
| Composite | Tree + Node | `Tree` / `Node` | 树形数据结构 |
| Adapter | struct 实现接口 | ChatClient usage 适配 | 将响应体中的 usage 提取为统计 |

## 方案对比

| 维度 | 方案 A：contexts/tracer 独立包 | 方案 B：并入 engine 包 |
|------|------|------|
| 耦合度 | 低 — engine 仅引用 tracer.Tracer 接口 | 中 — tracer 与 engine 同包，被动依赖 |
| 内聚性 | 高 — tracer 职责完整封装 | 低 — span/树结构散落在 engine 里 |
| 可测试性 | 高 — tracer 可独立测试 | 中 — 需构造 engine 实例 |
| 可复用性 | 高 — 其他模块（如 WorkPlan）可直接引用 | 低 — 只能在 engine 内用 |
| 改动面 | 新增 contexts/tracer/ + engine 少量改 | 新增 engine/tracer.go + 改 engine |
| 实现成本 | 低 | 低 |

### 推荐：方案 A

理由：
1. tracer 是跨领域关注点，不应与 engine 绑定——WorkPlan 节点执行、Agent 工具调度都需要埋点
2. 独立包可独立测试，不依赖 engine 基础设施
3. 未来迁移到 OTel SDK 时只需换实现

**最大风险**：contexts 层已有 `cache/history/react/storage`，再加 `tracer` 使 contexts 包职责变宽 → 但 tracer 是通用的基础设施层，不算膨胀。

## 循环依赖检查

```
contexts/tracer  → 无外部依赖（仅标准库）
engine           → contexts/tracer（接口引用）
agent            → engine（不变）
agent/core/api   → types（不变）
```

零循环依赖。

## 核心接口定义

```go
// contexts/tracer/tracer.go

// SpanKind 标识 span 的类型
type SpanKind string

const (
    SpanReActLoop    SpanKind = "react_loop"
    SpanLLMCall      SpanKind = "llm_call"
    SpanToolDispatch SpanKind = "tool_dispatch"
    SpanCacheOp      SpanKind = "cache_op"
)

// SpanStatus 标识 span 的结束状态
type SpanStatus string

const (
    StatusOK    SpanStatus = "ok"
    StatusError SpanStatus = "error"
)

// Node 是追踪树中的一个节点
type Node struct {
    ID         string            `json:"id"`
    ParentID   string            `json:"parent_id,omitempty"`
    Name       string            `json:"name"`
    Kind       SpanKind          `json:"kind"`
    Start      time.Time         `json:"start"`
    End        time.Time         `json:"end,omitempty"`
    Duration   time.Duration     `json:"duration,omitempty"`
    Status     SpanStatus        `json:"status"`
    Attrs      map[string]string `json:"attrs,omitempty"`
    Events     []Event           `json:"events,omitempty"`
}

// Event 是 span 内部的注解（一个时间点上的附加信息）
type Event struct {
    Time  time.Time         `json:"time"`
    Name  string            `json:"name"`
    Attrs map[string]string `json:"attrs,omitempty"`
}

// Tree 是一次完整执行的全链路追踪树
type Tree struct {
    TraceID string `json:"trace_id"`
    Root    *Node  `json:"root"`
    // 内部字段
    nodes map[string]*Node
    mu    sync.Mutex
}

// Tracer 是可观测性接口
type Tracer interface {
    // NewTrace 创建新的追踪，返回 root span 的 context
    NewTrace(ctx context.Context, traceID string) context.Context
    
    // StartSpan 创建一个子 span
    StartSpan(ctx context.Context, name string, kind SpanKind, attrs map[string]string) context.Context
    
    // EndSpan 结束一个 span，记录结束时间和状态
    EndSpan(ctx context.Context, opts ...SpanOption)
    
    // AddEvent 给当前 span 添加一个事件
    AddEvent(ctx context.Context, name string, attrs map[string]string)
    
    // Export 导出当前 trace 的完整树
    Export(ctx context.Context) *Tree
}

// SpanOption 是 EndSpan 的可选参数
type SpanOption func(*Node)

func WithError(err error) SpanOption { ... }
func WithAttr(key, value string) SpanOption { ... }
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 |
|---|------|------|---------|
| 1 | 定义核心类型：Node, Event, Tree, SpanKind | `contexts/tracer/tracer.go` | — |
| 2 | 定义 Tracer 接口 + NoopTracer | `contexts/tracer/tracer.go` | Strategy |
| 3 | 实现 SimpleTracer（JSON lines + in-memory tree） | `contexts/tracer/tracer.go` | Composite |
| 4 | 定义 context key + 上下文工具函数 | `contexts/tracer/tracer.go` | — |
| 5 | Engine 加 WithTracer Option | `engine/engine.go` | Builder |
| 6 | Engine.chatLoop 埋点（root span） | `engine/loop.go` | — |
| 7 | Engine.callLLM 埋点（llm_call span） | `engine/loop.go` | — |
| 8 | Engine 工具调度循环埋点（tool_dispatch span） | `engine/loop.go` | — |
| 9 | Engine 缓存操作埋点（cache_op span） | `engine/loop.go` | — |
| 10 | ChatClient 加 token 用量统计 | `agent/core/api/client.go` | Adapter |
| 11 | 单元测试 + go race | `contexts/tracer/tracer_test.go` | — |
| 12 | 冒烟测试（mock + 真实 API） | `test/tracer_smoke_test.go` | — |

## 测试策略

### 单元测试（go race）
- NoopTracer 零损耗验证
- SimpleTracer 树构建正确性（父子关系、时序）
- SimpleTracer 并发安全（多 goroutine 同时 StartSpan/EndSpan）
- Engine 埋点验证（mock LLM 服务器）

### 冒烟测试（真实 API）
- 使用真实配置文件调用 OpenAI/Anthropic
- 验证 Trace Tree 结构完整
- 验证 token 计数存在

## 回滚方案

所有新增文件不影响现有代码。改动的 engine 文件通过 `WithTracer` 的默认值（nil = NoopTracer）确保向后兼容。删除 `contexts/tracer/` 和回退 engine 修改即可。
