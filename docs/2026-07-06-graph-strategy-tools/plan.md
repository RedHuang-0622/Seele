# 实现方案

## 设计目标

1. **策略模式重构图编排执行层**：将 "节点执行什么" 从固定 Runner 实现抽离为可组合的 `NodeStrategy`，让节点可以是 Method / LLM / Agent
2. **Graph-as-Tools 示例**：展示图编排基础语法（fork/loop/pipeline）作为 LLM 可调用的 tool，使 LLM 能动态调度子 WorkPlan

## 设计模式选择

| 模式 | Go 实现 | 应用位置 | 理由 |
|------|--------|---------|------|
| **Strategy** | `NodeStrategy interface` + DI | `workplan/strategy.go` + `runner.go` | 执行算法族可互换 |
| **Adapter** | `strategyRunner` 包装 `NodeStrategy` 实现 `NodeRunner` | `workplan/runner.go` | 让图引擎无感知策略变化 |
| **Builder** | Functional Options + 链式调用 | `workplan/sugar.go` | 复杂节点构造（Method/LLM/Strategy 糖） |
| **Factory Method** | 内置 `NewMethodStrategy` / `NewLLMStrategy` / `NewAgentStrategy` | `workplan/strategy.go` | 按意图创建策略 |

## 方案对比（3 种）

### 方案 A（推荐）：Strategy + Adapter 最小侵入

**核心思路**：新增 `NodeStrategy` 接口，`strategyRunner` 作为 `NodeRunner` 的唯一适配器。`autoRunner` 保留但内部改用 `AgentStrategy`。loop/fork/control 等流程 Runner 保持原样。

**变更范围**：
```
+ workplan/strategy.go    ← 新增 (~150行)
~ workplan/runner.go      ← 修改：+strategyRunner, autoRunner 内部委派给 AgentStrategy (~100行变更)
~ workplan/node.go        ← 修改：node.strategy 字段, kindStrategy (~20行)
~ workplan/sugar.go       ← 修改：+Method/LLM/Strategy 糖 (~60行)
~ workplan/plan.go        ← 修改：+ToTool 可选 (~30行)
+ example_Implement/05_graph_tools/  ← 新增 (~300行)
```

**关键接口**：
```go
// strategy.go
type NodeStrategy interface {
    Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}

type MethodStrategy struct {
    fn func(ctx context.Context, input string) (string, error)
}
func (s *MethodStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) {
    return s.fn(ctx, renderTemplate(input, ec))
}

type LLMStrategy struct {
    systemPrompt string
    factory AgentFactory
}
func (s *LLMStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) {
    agent := s.factory.NewAgent(s.systemPrompt)
    return toJSON(agent.Chat(ctx, renderTemplate(input, ec)))
}

type AgentStrategy struct {
    systemPrompt string
    toolFilter []string
    factory    AgentFactory
}
func (s *AgentStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) {
    // 当前 autoRunner.Run() 的逻辑——ReAct 循环 + 工具调用
    agent := s.factory.NewAgent(s.systemPrompt)
    if f, ok := agent.(interface{ SetToolFilter([]string) }); ok && len(s.toolFilter) > 0 {
        f.SetToolFilter(s.toolFilter)
    }
    out, err := agent.Chat(ctx, input)
    if err != nil { return "", err }
    return toJSON(out), nil
}

// runner.go: 新增 strategyRunner
type strategyRunner struct {
    id       string
    strategy NodeStrategy
}
func (r *strategyRunner) ID() string { return r.id }
func (r *strategyRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
    return r.strategy.Execute(ctx, r.id, ec) // id 用于 MethodStrategy 的上下文传递
}

// sugar.go: 新增糖方法
func (wp *WorkPlan) Method(id string, fn func(ctx context.Context, input string) (string, error), opts ...NodeOpt) *WorkPlan {
    n := &node{id: wp.resolveID(id, "method"), kind: kindMethod, strategy: &MethodStrategy{fn: fn}}
    applyOpts(n, opts)
    runner := &strategyRunner{id: n.id, strategy: n.strategy}
    return wp.registerNode(n, runner)
}
func (wp *WorkPlan) LLM(id string, opts ...NodeOpt) *WorkPlan {
    // ...
    runner := &strategyRunner{id: n.id, strategy: &LLMStrategy{...}}
    return wp.registerNode(n, runner)
}
func (wp *WorkPlan) Strategy(id string, strategy NodeStrategy, opts ...NodeOpt) *WorkPlan {
    // ...
    runner := &strategyRunner{id: n.id, strategy: strategy}
    return wp.registerNode(n, runner)
}
```

### 方案 B：纯策略模式（所有 Runner 统一为 strategyRunner）

**核心思路**：连 loopRunner / forkRunner 也通过策略模式实现，`NodeRunner` 只有一个 `strategyRunner` 实现。

**缺点**：Loop 的 Signal 机制、Fork 的并发管理会污染 Strategy 接口，Strategy 不再是纯"执行策略"而混入流程控制语义。接口膨胀，得不偿失。

**变更范围**：同方案 A 但 `runner.go` 删除所有独立 Runner，全部迁入策略。

### 方案 C：Decorator 链式策略

**核心思路**：NodeStrategy 不变，但在 strategyRunner 外层叠加 Decorator（日志/重试/缓存/超时），通过 `WithRetry()` / `WithTimeout()` 链式装饰基础策略。

**优点**：扩展性好，AOP 风格。
**缺点**：Decorator 的入参/出参类型不同时接口泛化困难；Go 无方法组合语法糖，Decorator 链构造较啰嗦。

## 定性对比

| 维度 | 方案 A（推荐） | 方案 B | 方案 C |
|------|--------------|--------|--------|
| **耦合度** | 低 — Strategy 接口在 use 方（sugar.go）定义，Runner 实现 NodeRunner | 中 — Strategy 接口需同时承载执行+流程控制，职责不单一 | 低 — 同方案A，Decorator 层隔离 |
| **内聚性** | 高 — 执行策略在 strategy.go 内聚，流程控制在 runner.go 保留 | 低 — 流程控制逻辑分散在各种 Strategy 实现中 | 高 — 同方案A |
| **可测试性** | 高 — 可 mock NodeStrategy 单独测试；可 mock AgentFactory 测试策略实现 | 中 — loop/fork 的测试需要构造完整 Strategy 配置 | 高 — 可逐层测试 Decorator |
| **实现成本** | **低** — ~350行新增 + ~150行修改 | 高 — 需重新设计 loop/fork 的策略接口 | 中 — ~400行新增 + Decorator 链 |
| **改动面** | 小 — sugar 签名不变，graph/validate 不修改 | 大 — 删除现有 Runner 需全面回归 | 小 — sugar 签名不变，新增可选 Decorator |
| **可回滚性** | 高 — 不影响原有 node 路径 | 低 — 全量替换难定点回滚 | 高 — Decorator 可选 |
| **团队适配** | 容易 — 熟悉 Go interface 即可 | 难 — 需要接受流程控制=策略的前提 | 中 — Decorator 模式在 Go 中不常见 |

## 推荐：方案 A

**理由**：
- 最小侵入：graph.go/validate.go/plan.go 核心执行引擎零修改
- 完全向后兼容：`Auto()` 内部自动使用 `AgentStrategy`，用户无感知
- 渐进采用：用户可混用 `Auto()`（传统方式）和 `Method()`/`LLM()`（新策略方式）
- 易测试：Strategy 是纯接口，可单独 mock 测试
- 易扩展：新增自定义策略只需实现 `NodeStrategy` 接口

**最大风险**：
- Strategy 的 `Execute` 签名中 `input` 参数与 `ec.PrevOutput` 的关系需文档明确：`input` 来自 sugar 调用时的模板文本，`ec.PrevOutput` 由图引擎自动注入
- 用户可能困惑"什么时候用 `Auto()` 什么时候用 `Strategy()`"——文档需要清晰的决策树

## 循环依赖检查

```
workplan/
  ├── strategy.go  → 依赖: AgentFactory (定义在 plan.go) + ExecutionContext (定义在 graph.go)
  ├── runner.go    → 依赖: strategy.go (NodeStrategy), graph.go (NodeRunner, ExecutionContext)
  ├── sugar.go     → 依赖: runner.go (strategyRunner), node.go (node), strategy.go (策略构造)
  ├── graph.go     → 依赖: 无内部依赖
  ├── plan.go      → 依赖: graph.go, node.go, runner.go
  ├── validate.go  → 依赖: node.go
  └── primitive.go → 依赖: plan.go (WorkPlan), node.go
```

✅ **无循环依赖**。strategy.go 在最底层（只依赖 agent.go 的 AgentFactory），runner.go 依赖 strategy.go，sugar.go 依赖 runner.go。

## 核心接口定义

### NodeStrategy（workplan/strategy.go）

```go
// NodeStrategy 是图节点的执行策略接口。
//
// 图引擎(Executor)看到的是 NodeRunner，strategyRunner 是 NodeRunner 的唯一实现，
// 内部组合一个 NodeStrategy。用户通过实现 NodeStrategy 自定义节点行为。
//
// 内置策略：
//   - MethodStrategy   — 执行 Go 函数
//   - LLMStrategy      — 纯 LLM 调用（无工具）
//   - AgentStrategy    — 完整 ReAct 循环 + 工具调用
type NodeStrategy interface {
    // Execute 执行策略逻辑。
    //   input:  构建节点时传入的模板文本（通过 renderTemplate 已渲染）
    //   ec:     图执行上下文（含 PrevOutput、Vars 等）
    // 返回: JSON 字符串
    Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}
```

### WorkPlan 新增糖方法

```go
// Method 注册一个 Go 函数作为节点（纯本地计算，无 LLM 调用）。
func (wp *WorkPlan) Method(id string, fn func(ctx context.Context, input string) (string, error), opts ...NodeOpt) *WorkPlan

// LLM 注册一个纯 LLM 节点（无工具调用）。
func (wp *WorkPlan) LLM(id string, opts ...NodeOpt) *WorkPlan

// Strategy 注册一个自定义策略节点。
func (wp *WorkPlan) Strategy(id string, strategy NodeStrategy, opts ...NodeOpt) *WorkPlan
```

### ToTool 包装（可选，workplan/plan.go）

```go
// ToolEntry 包装（不引入 core/tool 包依赖，由调用方适配）
type WorkPlanTool struct {
    Definition types.Tool
    Run        func(ctx context.Context, argsJSON string) (string, error)
}

func (wp *WorkPlan) ToTool(name, desc string) WorkPlanTool {
    // 将 WorkPlan 包装为可执行的工具
}
```

## Graph-as-Tools 示例设计

### 示例 05_graph_tools 的核心模式

```go
// 1. 定义 fork 工具：让 LLM 可以调度多个并发子代理
engine.RegisterInlineTool(
    "fork_agents",
    "并发启动多个 Agent 执行不同任务，适合需要多角色并行分析的场景",
    provider.SchemaOf(ForkToolInput{}),
    func(ctx context.Context, argsJSON string) (string, error) {
        // 解析 input → 构建 WorkPlan(Fork) → 执行 → 返回合并结果
    },
)

// 2. 定义 pipeline 工具：让 LLM 可以编排流水线
engine.RegisterInlineTool(
    "pipeline",
    "按顺序执行多个步骤",
    provider.SchemaOf(PipelineToolInput{}),
    func(ctx context.Context, argsJSON string) (string, error) {
        // 解析 input → 构建 WorkPlan(Pipeline) → 执行 → 返回结果
    },
)

// 3. 定义 loop 工具：让 LLM 可以执行迭代任务
engine.RegisterInlineTool(
    "loop_task",
    "反复执行一个任务直到满足条件",
    provider.SchemaOf(LoopToolInput{}),
    func(ctx context.Context, argsJSON string) (string, error) {
        // 解析 input → 构建 WorkPlan(Loop) → 执行 Signal → 返回结果
    },
)
```

### 对话流程

```
User: "帮我同时调研 Go 1.22 的新特性和 Rust 的生态发展"

LLM: → 调用 fork_agents 工具 →
  - 分支1: "调研 Go 1.22 新特性"
  - 分支2: "调研 Rust 生态发展"
→ 合并结果 → 输出综合报告
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 | 验收 |
|---|------|------|---------|------|
| 1 | 定义 NodeStrategy 接口 + 三种内置实现 | `workplan/strategy.go` | Strategy + Factory Method | 编译通过，单元测试 |
| 2 | 新增 strategyRunner（NodeRunner 适配器） | `workplan/runner.go` | Adapter | 图引擎可执行 strategyRunner |
| 3 | node 结构新增 strategy 字段 + kindMethod/kidStrategy | `workplan/node.go` | - | 编译通过 |
| 4 | 新增 Method/LLM/Strategy 糖方法 | `workplan/sugar.go` | Builder | 链式调用可用 |
| 5 | Auto 内部使用 AgentStrategy（保留后向兼容） | `workplan/sugar.go` | - | 03_workplan 示例不改动可运行 |
| 6 | 新增 ToTool 包装（可选） | `workplan/plan.go` | Adapter | 可包装为 ToolEntry |
| 7 | 新建 05_graph_tools 示例 | `example_Implement/05_graph_tools/main.go` | - | 编译通过 |
| 8 | 回归测试：03_workplan 编译运行 | 测试 | - | 零报错 |
| 9 | go vet + go build 全量 | 测试 | - | 零告警 |

## 测试策略

| 测试层级 | 内容 | 工具 |
|---------|------|------|
| 单元测试 | MethodStrategy / LLMStrategy / AgentStrategy 各自 Execute 行为验证 | `go test ./workplan/` |
| 单元测试 | strategyRunner ↔ NodeRunner 接口兼容性 | `go test ./workplan/` |
| 集成测试 | sugar 方法构建后 graph.Validate() + Execute 正常 | `go test ./workplan/` |
| 回归测试 | 03_workplan 示例不改动直接运行 | `go run ./example_Implement/03_workplan/` |
| 编译检查 | 05_graph_tools 编译通过 | `go build ./example_Implement/05_graph_tools/` |

## 回滚方案

1. **部分回滚**：如果策略模式影响现有流程，删除 `strategy.go` + 回退 `runner.go` 中 `strategyRunner` 部分即可。sugar.go 的 Method/LLM/Strategy 糖方法删除不影响 Auto 等现有方法。
2. **全量回滚**：回退 workplan/ 下所有变更文件，保留新增示例目录（不编译不影响）。
3. **增量提交**：按子目标 G1→G2→G3→G4→G5→G6 顺序提交，每步独立 commit，可定点回滚。
