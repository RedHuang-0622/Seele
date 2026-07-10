# 实现方案 — Seele v0.6 架构重构

## 设计目标

1. **零侵入**：所有 5 项重构在各自 Phase 完成后看齐零向后兼容破坏
2. **独立可合并**：5 个 Phase 独立可审查、可测试、可回滚
3. **sugar API 不动**：`Auto()/Method()/LLM()/Fork()/Pipeline()/Loop()` 保持原签名
4. **Tracer 可选**：OTel 依赖不污染 NoopTracer 路径

## 设计模式选择

| 模式 | 语言实现 | 应用位置 | 理由 |
|------|---------|---------|------|
| Strategy | Go interface | Phase 1 NodeStrategy / Phase 4 Loop | 可互换执行行为 |
| Adapter | struct implementing interface | Phase 1 strategyRunner | 桥接 NodeStrategy ↔ NodeRunner |
| Builder (Functional Options) | `func Option()` | Phase 2 Plan.Add opts | 复杂节点构造 |
| Facade | struct wrapping subsystems | Phase 4 Engine | Loop 上层的统一入口 |
| Decorator | wrapper struct | Phase 5 SimpleTracer → OTelTracer | 叠加 OTel 能力 |
| Memento | serializable snapshot | Phase 2 Plan struct | Plan 快照和恢复 |

---

## 方案 A：保守渐进（推荐）

**核心思路**：5 个 Phase 严格独立，按「内部→外部」「接口→实现」顺序推进。每个 Phase 完成后所有存量测试通过，不跳步。

### 方案 A 详细设计

#### Phase 1 — NodeStrategy 接口清理

```go
// 旧接口（标记 Deprecated，迁移期并行）
// Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)

// 新接口
type NodeStrategy interface {
    Execute(ctx context.Context, ec *ExecutionContext) (string, error)
}

// 迁移桥：DeprecatedNodeStrategy 兼容旧接口的自定义策略
type DeprecatedNodeStrategy interface {
    Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}

// strategyRunner 判断策略类型，自动适配
func (r *strategyRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
    // 框架层统一渲染
    rendered := renderTemplate(r.input, ec)
    
    // 清理后的 ec.PrevOutput 已包含渲染结果
    ec.PrevOutput = rendered
    
    // 判断策略实现了新接口还是旧接口
    if s, ok := r.strategy.(NodeStrategy); ok {
        return s.Execute(ctx, ec)
    }
    // 旧接口通过 DeprecatedNodeStrategy 桥接
    return r.strategy.(DeprecatedNodeStrategy).Execute(ctx, rendered, ec)
}
```

**变更**：
- `workplan/strategy.go` — 改 NodeStrategy 接口 + 更新三种内置策略
- `workplan/runner.go` — strategyRunner.Run 接管模板渲染
- `workplan/cache_strategy.go` — CachedStrategy 适配新接口
- `workplan/strategy_test.go` — 更新测试

**设计模式**：Strategy（NodeStrategy）+ Adapter（DeprecatedNodeStrategy 桥）

#### Phase 2 — WorkPlan 声明式 Plan

Plan 结构体（纯数据，JSON 可序列化）：

```go
// PlanNodeSpec 是可序列化的节点规格（无 Go 函数引用）
type PlanNodeSpec struct {
    ID          string            `json:"id"`
    Kind        string            `json:"kind"`        // "auto" | "method" | "llm" | "strategy" | ...
    Input       string            `json:"input,omitempty"`
    SystemPrompt string            `json:"system_prompt,omitempty"`
    ToolFilter  []string          `json:"tool_filter,omitempty"`
    Next        string            `json:"next,omitempty"`  // 默认下一节点
    
    // kindApprove
    ApproveOptions []ChoiceOption `json:"approve_options,omitempty"`
    ApproveTimeout Duration       `json:"approve_timeout,omitempty"`
    
    // kindFork
    ForkBranches []ForkBranchSpec `json:"fork_branches,omitempty"`
    
    // kindLoop
    LoopBodyID  string            `json:"loop_body_id,omitempty"`
    LoopMaxIter int               `json:"loop_max_iter,omitempty"`
    LoopExhaustedID string        `json:"loop_exhausted_id,omitempty"`
    
    // kindIf
    IfTrueID  string              `json:"if_true_id,omitempty"`
    IfFalseID string              `json:"if_false_id,omitempty"`
    
    // kindSwitch
    SwitchCases []SwitchCaseSpec  `json:"switch_cases,omitempty"`
    
    // kindEmit
    EmitKey string                `json:"emit_key,omitempty"`
}

// PlanEdgeSpec 是可序列化的边规格
type PlanEdgeSpec struct {
    From      string `json:"from"`
    To        string `json:"to"`
    Label     string `json:"label,omitempty"`     // "true" | "false" | "exhausted" | 用户定义
    Condition string `json:"condition,omitempty"` // 字符串标签，通过 ConditionRegistry 解析
}

// Plan 是可序列化的工作流定义
type Plan struct {
    Name        string          `json:"name,omitempty"`
    Description string          `json:"description,omitempty"`
    EntryNodeID string          `json:"entry_node_id"`
    Version     string          `json:"version,omitempty"` // 语义版本
    Nodes       []PlanNodeSpec  `json:"nodes"`
    Edges       []PlanEdgeSpec  `json:"edges"`
}

// ConditionRegistry 管理条件函数
type ConditionRegistry struct {
    conditions map[string]EdgeCondition
}
func NewConditionRegistry() *ConditionRegistry
func (r *ConditionRegistry) Register(name string, cond EdgeCondition)
func (r *ConditionRegistry) Resolve(name string) (EdgeCondition, bool)
```

WorkPlan 扩展：

```go
// 将 WorkPlan 导出为 Plan（序列化快照）
func (wp *WorkPlan) ToPlan() *Plan

// 从 Plan 加载到 WorkPlan（重建图）
func (wp *WorkPlan) LoadPlan(plan *Plan) error

// Plan 也支持链式构建
func (p *Plan) Add(id, kind string, opts ...PlanNodeOpt) *Plan
func (p *Plan) Edge(from, to string, opts ...PlanEdgeOpt) *Plan
```

sugar API 兼容方式：sugar 方法内部调用 `plan.Add().Edge()` 或直接操作 `graph.AddNode/AddEdge`。sugar.go 保持不变。

**变更**：
- `workplan/plan.go` — 新增 Plan/PlanNodeSpec/PlanEdgeSpec 结构体
- `workplan/plan.go` — WorkPlan.ToPlan() / LoadPlan() 方法
- `workplan/sugar.go` — 不改（保持 node/edge 封装）
- `workplan/graph.go` — Edge 加 json tag 支持序列化

#### Phase 3 — WorkPlan 糖→Agent 工具

```go
// 增强 WorkPlanTool（plan.go）
type WorkPlanTool struct {
    Name        string
    Description string
    InputSchema map[string]interface{}
    
    // PlanRef 序列化的 Plan 快照，用于工具元信息展示和调试
    PlanRef     *Plan              `json:"plan,omitempty"`
    
    Run         func(ctx context.Context, argsJSON string) (string, error)
}

// ToTool 增强：支持 Plan 快照绑定
func (wp *WorkPlan) ToTool(name, desc string, inputSchema map[string]interface{}) WorkPlanTool {
    return WorkPlanTool{
        Name:        name,
        Description: desc,
        InputSchema: inputSchema,
        PlanRef:     wp.ToPlan(),  // 绑定 Plan 快照
        Run: func(ctx context.Context, argsJSON string) (string, error) {
            // 可选：用 argsJSON 覆写 Vars 后再执行
            result, err := wp.Run(ctx)
            if err != nil {
                return "", err
            }
            return result.FinalOutput(), nil
        },
    }
}

// 在 agent 侧注册（agent/agent.go 新增方法）
func (a *Agent) RegisterWorkPlanTool(wpTool workplan.WorkPlanTool) error {
    return a.RegisterTool(wpTool.Name, wpTool.Description, wpTool.InputSchema, wpTool.Run)
}
```

#### Phase 4 — Engine chatLoop 解耦

```go
// Loop 接口（engine/loop.go）
type Loop interface {
    // Run 执行一次完整的 LLM 循环。
    // userInput 用户输入；onChunk 流式回调（nil=同步模式）。
    Run(ctx context.Context, userInput string, onChunk func(string)) (reply string, err error)
    
    // History 返回当前对话历史（只读副本）。
    History() []types.Message
    
    // ClearHistory 清空对话历史，保留 system 消息。
    ClearHistory()
}

// ReActLoopOption 配置 ReActLoop
type ReActLoopOption func(*ReActLoop)
func WithMaxLoops(n int) ReActLoopOption
func WithSessionID(id string) ReActLoopOption
func WithCache(c cache.Provider) ReActLoopOption
func WithStore(s *storage.Store) ReActLoopOption
func WithTracer(t tracer.Tracer) ReActLoopOption
func WithSystemPrompt(p string) ReActLoopOption

// NewReActLoop 创建 ReActLoop（从当前 chatLoop 提取）
func NewReActLoop(a *agent.Agent, llm types.ChatCompleter, opts ...ReActLoopOption) *ReActLoop

// Engine 改为持有 Loop 接口
type Engine struct {
    agent     *agent.Agent
    llm       types.ChatCompleter
    cfg       SessionConfig
    loop      Loop  // ← 新增
    tracer    tracer.Tracer
    lastTrace *tracer.Tree
}

// New 创建 Engine，默认使用 ReActLoop
func New(a *agent.Agent, opts ...Option) *Engine {
    e := &Engine{/* ... */}
    // 默认 ReActLoop
    e.loop = NewReActLoop(a, e.llm, 
        WithSessionID(e.sessionID),
        WithTracer(e.tracer),
        // ...继承当前配置
    )
    // ...
}

// WithLoop 注入自定义 Loop 实现
func WithLoop(l Loop) Option {
    return func(e *Engine) { e.loop = l }
}

// Chat/ChatStream 委托给 loop.Run
func (e *Engine) Chat(ctx context.Context, userInput string) (string, error) {
    reply, err := e.loop.Run(ctx, userInput, nil)
    e.lastTrace = e.tracer.Export(ctx)
    return reply, err
}
```

**chatLoop.go 所有逻辑移入 ReActLoop**，包括：
- restoreFromCache / saveToCache
- history 管理
- tracer 埋点
- callLLM / truncateResult

**注意**：Engine 不直接持有 history/loop state 了——Loop 实现内部管理自己的状态。

#### Phase 5 — Tracer OTel 化

```go
// SimpleTracer 新增 OTel TracerProvider 字段
type SimpleTracer struct {
    mu      sync.Mutex
    traceID string
    spans   map[string]*Node
    rootID  string
    seq     int
    
    // OTel 可选
    otelProvider trace.TracerProvider  // nil = 不发射 OTel span
}

// WithOTelTracerProvider 设置 OTel TracerProvider
func (t *SimpleTracer) WithOTelTracerProvider(provider trace.TracerProvider) {
    t.otelProvider = provider
}

// simpleSpan.End() 增加 OTel 发射
func (s *simpleSpan) End(opts ...SpanOption) {
    // 1. 更新本地 Node（与之前相同）
    s.tracer.mu.Lock()
    node, ok := s.tracer.spans[s.id]
    s.tracer.mu.Unlock()
    if !ok { return }
    
    now := time.Now()
    node.End = now
    node.Duration = now.Sub(node.Start)
    for _, opt := range opts { opt(node) }
    
    // 2. 发射 OTel span（如有 provider）
    if s.tracer.otelProvider != nil {
        s.emitOTelSpan(node)
    }
}

// emitOTelSpan 将本地 Node 映射为 OTel SpanData 并结束
func (s *simpleSpan) emitOTelSpan(node *Node) {
    // 创建 OTel span 并结束
    // span.End() 会触发 OTel SDK 导出
}
```

**NoopTracer 不受影响**：不引用 OTel 包，零开销不变。

---

## 方案 B：统一接口架构

**核心思路**：Phase 1 和 Phase 4 统一为 "LoopStrategy" 概念。Phase 2 和 Phase 3 统一为 "Plan-as-Root"。

```
LoopStrategy (包含 NodeStrategy 调用链)
  ├─ ReActStrategy      ← 当前 chatLoop
  ├─ PlanAndExecute     ← 未来
  └─ CustomStrategy     ← 用户自定义
```

```go
// LoopStrategy 是 NodeStrategy 的上层抽象
type LoopStrategy interface {
    Execute(ctx context.Context, ec *ExecutionContext) (string, error)
}
// NodeStrategy 成为 LoopStrategy 的编排单元
```

**优点**：统一抽象，未来扩展性强。**缺点**：改动面大，当前 chatLoop 的逻辑难以全部塞入 `ExecutionContext`。

| 维度 | 方案 B |
|------|--------|
| 耦合度 | 低 — 统一抽象 |
| 内聚性 | 高 — 全部 Loop 逻辑集中 |
| 可测试性 | 中 — 需要更多的 mock 准备 |
| 实现成本 | **高** — 比方案 A 多 2 倍工作量 |
| 改动面 | 大 — engine + workplan 耦合改写 |
| 可回滚性 | 低 — 耦合变更增加回滚风险 |

**结论**：方案 B 过于理想化，当前重构不适合把 LoopStrategy 和 NodeStrategy 统一抽象——它们在不同的抽象层级（引擎级 vs 节点级），强行合并会增加不必要的心智负担。

---

## 方案 C：最小可行变更

**核心思路**：只改最痛的点，不动完整的抽象。

- **Phase 1**：NodeStrategy 不拆 input 参数，只在 Execute 内把 input 设到 ec.Input field。接口不变。
- **Phase 2**：Plan 不做完整序列化，只提供 `MarshalJSON()/UnmarshalJSON()` 把现有 node 结构体序列化/反序列化。
- **Phase 4**：不引入 Loop 接口，只把 chatLoop 的主体逻辑提取为 `loopFunc func() (string, error)` 变量。Engine 不做结构性拆分。

| 维度 | 方案 C |
|------|--------|
| 耦合度 | 中 — 比现在略好 |
| 内聚性 | 低 — 逻辑仍混在 engine |
| 可测试性 | 高 — 改动小，存量测试无感 |
| 实现成本 | 低 — 2 天 |
| 改动面 | 小 — 5-8 个文件 |
| 可回滚性 | 高 — 小改 |
| **长期价值** | **低** — 无法解决根源问题 |

**结论**：方案 C 的短期成本看起来低，但 chatLoop 耦合和 NodeStrategy input 问题会复发。本次重构是深度架构变更，不追求最小改动。

---

## 方案对比

| 维度 | 方案 A （保守渐进） | 方案 B（统一接口） | 方案 C（最小变更） |
|------|-------------------|-------------------|-------------------|
| **耦合度** | 低 — 5 Phase 独立，Phase 间只通过上下游接口耦合 | 低 — 统一抽象层级 | 中 — history/loop/tracer 仍在 chatLoop |
| **内聚性** | 高 — 每 Phase 单一职责 | 高 — LoopStrategy 统一 | 低 — 耦合未解决 |
| **可测试性** | 高 — 每 Phase 独立测试，Mock 边界清晰 | 中 — 统一抽象需更多 mock | 高 — 不改结构 |
| **实现成本** | 🟢 中 — 5 Phase 共约 500-700 行新增/修改 | 🔴 高 — 约 1000-1500 行 | 🟢 低 — 约 150-200 行 |
| **改动面** | 🟢 受控 — 15-20 文件/Phase | 🔴 大 — 25-30 文件 | 🟢 小 — 5-8 文件 |
| **可回滚性** | 🟢 高 — 每 Phase 有独立回滚点 | 🟡 中 — Phase 间耦合 | 🟢 高 |
| **长期价值** | 🟢 高 — 5 个问题全部解决 | 🟢 高 — 抽象彻底 | 🔴 低 — 问题复发 |
| **后向兼容** | 🟢 高 — 每 Phase 有迁移桥 | 🟡 中 — 一次性破坏 | 🟢 高 |

---

## 推荐：方案 A（保守渐进）

**理由**：
1. **5 Phase 独立**，每 Phase 完成后可独立合并、独立测试、独立回滚
2. **sugar API 零改动**，Phase 2 只在 sugar 下层新增 Plan 结构，不动 sugar 代码
3. **NodeStrategy 迁移桥**（DeprecatedNodeStrategy）让自定义策略可以逐步迁移
4. **Loop 接口解耦不自伤**：ReActLoop 从现有 chatLoop 提取，原封不动迁移
5. **OTel 可选**：不引入条件编译，用字段 nil 判断是否发射，保持 NoopTracer 零开销

**最大风险**：
1. Phase 4 Loop 解耦时，`restoreFromCache / saveToCache` 的逻辑所有权归属（Loop 还是 Engine？）。**缓解**：cache/store 的逻辑随 history 归属 Loop 实现，Engine 只负责外部管理
2. Phase 2 Plan 序列化时 Edge.Condition 函数不能序列化。**缓解**：Plan.PlanEdgeSpec.Condition 用字符串标签，执行时通过 ConditionRegistry 映射到具体函数

---

## 循环依赖检查

- [✅] workplan 包 → types（单向，无环）
- [✅] engine 包 → workplan、tracer、agent（单向）
- [✅] Phase 4 Loop 接口定义在 engine/loop.go，不引入新包
- [✅] tree.AddNode/Edge 无外依赖

## 核心接口定义

### Phase 1：NodeStrategy（workplan/strategy.go）

```go
// NodeStrategy 是图节点的执行策略接口（v0.6 清理版）
// 模板渲染由框架层（strategyRunner）统一完成，策略只接收渲染后的 ec.PrevOutput。
type NodeStrategy interface {
    Execute(ctx context.Context, ec *ExecutionContext) (string, error)
}

// DeprecatedNodeStrategy 兼容 v0.5 旧接口的自定义策略
// 将旧自定义策略包装为此接口，框架自动适配：
//
//   var _ workplan.DeprecatedNodeStrategy = (*MyOldStrategy)(nil)
//   func (s *MyOldStrategy) Execute(ctx, input string, ec *ExecutionContext) (string, error) {
//       // ...
//   }
type DeprecatedNodeStrategy interface {
    Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}
```

### Phase 2：Plan（workplan/plan.go）

```go
type Plan struct {
    Name        string          `json:"name,omitempty"`
    Description string          `json:"description,omitempty"`
    EntryNodeID string          `json:"entry_node_id"`
    Version     string          `json:"version,omitempty"`
    Nodes       []PlanNodeSpec  `json:"nodes"`
    Edges       []PlanEdgeSpec  `json:"edges"`
}

type PlanNodeSpec struct {
    ID   string `json:"id"`
    Kind string `json:"kind"` // "auto" | "method" | "llm" | "strategy" | "approve" | ...
    
    Input        string   `json:"input,omitempty"`
    SystemPrompt string   `json:"system_prompt,omitempty"`
    ToolFilter   []string `json:"tool_filter,omitempty"`
    
    // 分支/循环/并发 配置（字段同 front-review.md 中 PlanNodeSpec）
    
    ApproveOptions []ChoiceOption `json:"approve_options,omitempty"`
    // ...
}

type PlanEdgeSpec struct {
    From      string `json:"from"`
    To        string `json:"to"`
    Label     string `json:"label,omitempty"`     // 描述性标签
    Condition string `json:"condition,omitempty"` // 条件标签，通过 registry 解析
}

type ConditionRegistry struct {
    mu         sync.RWMutex
    conditions map[string]EdgeCondition
}
```

### Phase 4：Loop（engine/loop.go）

```go
type Loop interface {
    Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
    History() []types.Message
    ClearHistory()
}

type ReActLoop struct {
    agent     *agent.Agent
    llm       types.ChatCompleter
    history   []types.Message
    cfg       SessionConfig
    sessionID string
    cache     cache.Provider
    store     *storage.Store
    modelName string
    tracer    tracer.Tracer
}

func NewReActLoop(a *agent.Agent, llm types.ChatCompleter, opts ...ReActLoopOption) *ReActLoop
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 |
|---|------|------|---------|
| P1.1 | NodeStrategy 接口去 input | workplan/strategy.go | Strategy |
| P1.2 | strategyRunner 接管 renderTemplate | workplan/runner.go | Adapter |
| P1.3 | CachedStrategy 适配 | workplan/cache_strategy.go | Decorator |
| P1.4 | 更新策略测试 | workplan/strategy_test.go | — |
| P2.1 | 定义 Plan/PlanNodeSpec/PlanEdgeSpec | workplan/plan.go | Memento |
| P2.2 | Plan.Add / Plan.Edge 构建 API | workplan/plan.go | Builder |
| P2.3 | ConditionRegistry | workplan/plan.go | Registry |
| P2.4 | WorkPlan.ToPlan / LoadPlan | workplan/plan.go | — |
| P2.5 | JSON 序列化测试 | workplan/plan_test.go | — |
| P3.1 | 增强 WorkPlanTool（plan ref） | workplan/plan.go | — |
| P3.2 | Agent.RegisterWorkPlanTool | agent/agent.go | Facade |
| P3.3 | 端到端冒烟测试 | example_Implement/ | — |
| P4.1 | Loop 接口 + ReActLoop | engine/loop.go | Strategy |
| P4.2 | Engine 持有 Loop | engine/engine.go | Facade |
| P4.3 | 配置传递（WithLoop / WithMaxLoops） | engine/engine.go | Builder |
| P4.4 | 测试无行为退化 | engine/engine_test.go | — |
| P5.1 | 加 OTel 依赖 | go.mod | — |
| P5.2 | SimpleTracer 可选的 OTel provider | contexts/tracer/tracer.go | Decorator |
| P5.3 | simpleSpan.End() OTel 发射 | contexts/tracer/tracer.go | — |
| P5.4 | Export() JSON 向后兼容 | contexts/tracer/tracer.go | — |
| P5.5 | 集成测试 | contexts/tracer/tracer_test.go | — |

## 测试策略

| 阶段 | go vet | go test -cover | go test -race -count=3 | bench | leak |
|------|--------|---------------|------------------------|-------|------|
| P1 | ✅ | ✅ strategy_test.go | ✅ | — | — |
| P2 | ✅ | ✅ plan_test.go | ✅ | — | — |
| P3 | ✅ | ✅ engine_test.go | ✅ | — | — |
| P4 | ✅ | ✅ engine_test.go | ✅ | ✅ Chat 循环 | ✅ goleak |
| P5 | ✅ | ✅ tracer_test.go | ✅ | ✅ span 分配 | — |

## 回滚方案

- **Phase 1**: 回退 NodeStrategy 接口 + strategy_test.go，保留 DeprecatedNodeStrategy 然后删除新接口
- **Phase 2**: 删除 Plan 类型和 LoadPlan/ToPlan，sugar API 不受影响
- **Phase 3**: 回退 agent.RegisterWorkPlanTool
- **Phase 4**: 回退 Loop 接口，Engine 恢复内联 chatLoop（这是回滚成本最高的 Phase）
- **Phase 5**: 删除 OTel 依赖 + 恢复 tracer.go（零外部依赖回归）
