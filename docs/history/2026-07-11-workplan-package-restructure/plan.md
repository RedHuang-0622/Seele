# 实现方案

## 设计目标

从零构建 workplan/ 三层架构（从零开始，不修改现有 workplan/ 文件），满足：

1. **绝对单向依赖**：`sugar → runtime → core`，`core` 零依赖
2. **无循环依赖**：设计阶段消灭任何环
3. **API 兼容**：顶层 `workplan.New/Auto/Loop/Fork/If/Switch/Emit/Approve/Step/Run` 签名不变
4. **零突破**：旧 worktree 代码（~1090 行）作为参考重用核心逻辑

## 设计模式选择

| 模式 | 应用位置 | 理由 |
|------|---------|------|
| Builder (Functional Options) | `workplan.New()`, Node 构建 | Go 惯用，参数可扩展，历史经验已验证 |
| Repository | `runtime/graph` — 节点和边的存取 | 封装原子操作（CAS），与 Node 解耦 |
| Strategy | `core/node` — 四种节点的执行行为 | 算法族可互换，驱动设计 |
| Adapter | `runtime/executor` → `core/node.Node` | 桥接执行器与具体节点实现 |
| Template Method | `runtime/runner.Run()` → `runtime/runner.Resume()` | 执行骨架固定，checkpoint 恢复可覆盖 |
| Memento | `runtime/checkpoint` | 快照存取，不破坏封装 |
| Visitor | `runtime/validate` — 遍历图结构检查 | DAG 校验、环检测、孤点检查 |

## 方案 A（推荐）：纯三层 + 分层构建（Bottom-Up）

### 核心思路

严格按 `core → runtime → sugar → 顶层` 自底向上逐层构建。每层内部按文件逐一实现，每 Task 独立编译验证。旧 worktree 代码作为参考，但不是修改目标——全部从零创建。

**关键架构决定**：

```go
// core/types/  —  纯数据类型，零依赖
package types
type WorkflowContext struct { ... }
type Status int               // Pending/Running/Completed/Failed/Aborted
type Snapshot struct { ... }

// core/node/  —  Node 接口 + 四种节点，依赖 types
package node
type Node interface { ID() string; Kind() NodeKind; Run(ctx, *types.WorkflowContext) (string, error) }
type DefaultNode struct { ... }
type LLMNode struct { ... }      // 依赖 LLMProvider 接口（在 node 包内定义）
type AgentNode struct { ... }    // 依赖 Agent 接口（在 node 包内定义）
type FunctionNode struct { ... } // 包装 Go func

// core/edge/  —  Edge 结构体 + 路由逻辑，依赖 types
package edge
type Edge struct { From, To string; Condition func(*types.WorkflowContext) bool }
func Resolve(edges []Edge, currentID string, wc *types.WorkflowContext) string

// runtime/graph/  —  Graph 构建与管理，依赖 node + edge
package graph
type Graph struct { ... }
func (g *Graph) AddNode(n node.Node) ...
func (g *Graph) AddEdge(e edge.Edge) ...
func (g *Graph) Resolve(currentID string, wc *types.WorkflowContext) string

// runtime/validate/  —  图校验，依赖 graph
package validate
func ValidateDAG(g *graph.Graph) error  // 环检测 + 孤点检测

// runtime/executor/  —  节点执行，依赖 node
package executor
type Executor struct { ... }
func (e *Executor) RunNode(ctx context.Context, n node.Node, wc *types.WorkflowContext) (string, error)

// runtime/scheduler/  —  调度逻辑，依赖 graph + executor + validate
package scheduler
type Scheduler struct { ... }
func (s *Scheduler) Run(ctx context.Context, entryID string) (*types.Snapshot, error)

// runtime/checkpoint/  —  快照存取，依赖 types
package checkpoint
type Store interface { Save(id string, s *types.Snapshot) error; Load(id string) (*types.Snapshot, error) }

// runtime/runner/  —  入口层，依赖 scheduler + checkpoint
package runner
type Runner struct { ... }
func (r *Runner) Run(ctx context.Context) (*types.Snapshot, error)
func (r *Runner) Resume(ctx context.Context, snapshotID string) (*types.Snapshot, error)

// runtime/serialize/  —  Plan ↔ Graph 互转，依赖 graph + types
package serialize
func ToPlan(g *graph.Graph) *types.Plan
func FromPlan(p *types.Plan, reg types.ConditionRegistry) (*graph.Graph, error)

// sugar/{auto,loop,fork,switch,approve,emit,checkpoint}/  —  DSL 构建函数，依赖 runtime/graph
package auto
func Add(g *graph.Graph, id, input string, factory types.AgentFactory) ...
package loop
func Add(g *graph.Graph, id, bodyID string, factory types.AgentFactory, opts ...func(*LoopNode)) *Signal
// ... 其他 sugar 类似
```

### 变更范围

| 操作 | 数量 | 说明 |
|------|------|------|
| 新建文件 | 24+ | 按 Milestone 逐文件创建 |
| 复用逻辑 | ~1090行 | 旧 worktree 代码参考，重写而非复制 |
| 修改旧文件 | 0 | 不修改现有 workplan/*.go |
| 最终替换 | 全部 | 重构完成后旧文件整体替换 |

### Milestone 实施策略

| Milestone | 文件数 | 估算 | 策略 |
|-----------|--------|------|------|
| M1: core/types | 3 | ~80行 | 一次性创建，零依赖，`go build` 即通过 |
| M2: core/node | 4 | ~200行 | 依赖 M1，完成后 `go vet` |
| M3: core/edge | 1 | ~60行 | 依赖 M1，路由逻辑单元测试 |
| M4: runtime | 6 | ~400行 | graph → validate → executor → scheduler → checkpoint → runner 逐步构建 |
| M5: serialize | 1 | ~80行 | 依赖 M4 + M1 |
| M6: sugar | 7 | ~400行 | 7个子包并行构建，各自依赖 M4 |
| M7: 顶层+测试 | 2 | ~200行 | 最后整合，验证全部 API |

> 旧 workplan/ 文件**在新构建完成前不动**。完成后用新文件整体替换旧文件。

## 方案 B（备选）：先拆旧文件再重构

### 核心思路

先在原地把现有 workplan.go 拆成 core/runtime/sugar 子包，再逐步完善每个子包的内容。利用 Go 编译器做重构助手——每次移动一个类型/函数，编译过了再移下一个。

### 关键差异

- 从现有文件出发，逐步把类型/函数移动到新位置
- Go 编译器即时验证移动是否正确
- 适合对现有代码熟悉、想逐步迁移的场景

### 对比方案 A 的优缺点

| 维度 | 方案 A（推荐） | 方案 B（备选） |
|------|------|------|
| 耦合度 | 低 — 架构先行，设计即约束 | 中 — 逐步迁移，可能有临时耦合 |
| 内聚性 | 高 — 每个包按职责创建 | 中 — 迁移过程中包职责可能模糊 |
| 可测试性 | 高 — 逐层构建即时可测 | 低 — 迁移过程中测试中断 |
| 实现成本 | 中 — 24 个文件全新建 | 高 — 拆分过程需反复编译验证 |
| 改动面 | 单次整体替换 | 多次增量调整，回归成本高 |
| 可回滚性 | 高 — 旧文件完整保留 | 中 — 拆分过程不可逆 |
| 团队适配 | 高 — 一次性学新结构 | 低 — 长期处于过渡态 |

**用户明确要求"从零开始" → 方案 A。**

## 方案 C（参考）：Monorepo 子模块化

### 核心思路

不同于在 `workplan/` 内部分包，而是将 core/runtime/sugar 提升为独立 Go module，独立版本管理。`workplan` 顶层通过 `go.mod` replace 引用。

### 适用场景
- 需要被多个独立的 Go 项目引用 core 类型
- 需要独立版本迭代

### 不采用理由
- 当前 Seele 是单体项目，不需要独立版本管理
- 引入了 replace 指令的维护成本
- 用户未要求多 module

## 推荐：方案 A（纯三层 + 分层构建）

### 推荐理由
1. 用户明确要求"从零开始"——方案 A 满足
2. Bottom-Up 构建，每 Task 可独立编译验证
3. 旧文件完整保留，无回滚风险
4. 最终一次性替换，AI 执行效率最高（全自动，无需人工增量验证）

### 最大风险
- **R1**：24 个 Task 间 import 路径一致性——全部使用 `github.com/RedHuang-0622/Seele/workplan/` 前缀
- **R2**：最终替换时旧引用全部断裂——确保 `test/` 和 `example_Implement/` 中的 import 同步更新
- **R3**：旧 worktree 代码是 v0.6 设计，当前代码已有更新的 API（如 StreamAgent）——需确保新 Node 接口兼容

### 风险缓解
- **R1**：在 `go.mod` 中 module name 不变，所有新 package 用 `github.com/RedHuang-0622/Seele/workplan/core/types` 格式
- **R2**：最终替换前用 `go build ./...` 全局验证 import
- **R3**：`core/node` 的 Agent 接口定义 `Chat(ctx, input) (string, error)` + 可选 `ChatStream` 接口（如现有 workplan.StreamAgent）

## 核心接口定义

```go
// ===== core/types/ =====
package types

type NodeKind int
const (
    KindMethod    NodeKind = iota
    KindLLM
    KindAgent
    KindAuto      // 旧兼容：≈ KindAgent
    KindStrategy
    KindApprove
    KindIf
    KindSwitch
    KindLoop
    KindFork
    KindJoin
    KindCheckpoint
    KindEmit
)

type WorkflowContext struct {
    PrevOutput string
    Vars       map[string]string
    Result     *WorkPlanResult
    Metadata   map[string]any
}

type WorkPlanResult struct {
    NodeResults  []*NodeResult
    Vars         map[string]string
    Checkpoints  map[string]string
    Aborted      bool
    TotalElapsed time.Duration
}

func (r *WorkPlanResult) FinalOutput() string
func (r *WorkPlanResult) FinalOutputString() string

type NodeResult struct {
    NodeID, Kind, Output string
    Err       error
    StartedAt, EndedAt time.Time
    Skipped, Aborted   bool
}

type Snapshot struct {
    NodeID    string
    Context   *WorkflowContext
    Timestamp time.Time
}

type Status int
const (
    StatusPending   Status = iota
    StatusRunning
    StatusCompleted
    StatusFailed
    StatusAborted
)

// ===== core/node/ =====
package node

type Node interface {
    ID() string
    Kind() types.NodeKind
    Run(ctx context.Context, wc *types.WorkflowContext) (string, error)
}

type BaseNode struct { id string; kind types.NodeKind }
func (n *BaseNode) ID() string       { return n.id }
func (n *BaseNode) Kind() types.NodeKind { return n.kind }

// LLMProvider 接口
type LLMProvider interface {
    Chat(ctx context.Context, input string) (string, error)
    ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error)
}

// Agent 接口
type Agent interface {
    Chat(ctx context.Context, input string) (string, error)
}

type AgentFactory interface {
    NewAgent(systemPrompt string) Agent
}

// ===== core/edge/ =====
package edge

type EdgeCondition func(wc *types.WorkflowContext) bool

type Edge struct {
    From, To string
    Condition EdgeCondition
    Priority  int
    Label     string
}

func Resolve(edges []Edge, currentID string, wc *types.WorkflowContext) string

// ===== runtime/graph/ =====
package graph

type Graph struct { ... }  // atomic.Pointer[map[string]node.Node] + atomic.Pointer[[]edge.Edge]

func New() *Graph
func (g *Graph) AddNode(n node.Node)
func (g *Graph) AddEdge(e edge.Edge)
func (g *Graph) GetNode(id string) node.Node
func (g *Graph) AllNodes() []string
func (g *Graph) AllEdges() []edge.Edge
func (g *Graph) Resolve(currentID string, wc *types.WorkflowContext) string
func (g *Graph) SetEntry(id string)
func (g *Graph) Entry() string

// ===== runtime/validate/ =====
package validate

func Graph(g *graph.Graph) error          // 入口节点 + 边引用 + 环检测 + 孤点
func Cyclic(g *graph.Graph) error         // 环检测
func Orphan(g *graph.Graph) error         // 孤点检测

// ===== runtime/executor/ =====
package executor

type Executor struct { ... }
func New() *Executor
func (e *Executor) RunNode(ctx context.Context, n node.Node, wc *types.WorkflowContext) (string, error)
// RunNode 负责：模板渲染(PreRun) → 调用 Node.Run → 结果处理(PostRun)

// ===== runtime/scheduler/ =====
package scheduler

type Scheduler struct { ... }
func New(g *graph.Graph, exec *executor.Executor) *Scheduler
func (s *Scheduler) Run(ctx context.Context) (*types.WorkPlanResult, error)
// Run 负责：获取 entry → 顺序/并发调度 → 边解析 → 下一节点 → 直到无后继

// ===== runtime/runner/ =====
package runner

type Runner struct { ... }
func New(g *graph.Graph, factory types.AgentFactory, opts ...Option) *Runner
func (r *Runner) Run(ctx context.Context) (*types.WorkPlanResult, error)
func (r *Runner) Resume(ctx context.Context, snapshotID string) (*types.WorkPlanResult, error)

// ===== runtime/checkpoint/ =====
package checkpoint

type Store interface {
    Save(id string, s *types.Snapshot) error
    Load(id string) (*types.Snapshot, error)
}

type Manager struct { store Store }
func NewManager(store Store) *Manager
func (m *Manager) Save(wc *types.WorkflowContext, nodeID string) (*types.Snapshot, error)
func (m *Manager) Load(id string) (*types.WorkflowContext, string, error)

// ===== runtime/serialize/ =====
package serialize

type Plan struct {
    Name, Description, Version string
    EntryNodeID string
    Nodes       []PlanNodeSpec
    Edges       []PlanEdgeSpec
}

func ToPlan(g *graph.Graph) *Plan
func FromPlan(p *Plan, reg types.ConditionRegistry) (*graph.Graph, error)

// ===== sugar 子包（7个）=====
package auto    // func Add(g *graph.Graph, id, input string) ...
package loop    // func Add(..., opts...) *Signal
package fork    // func Add(..., branches []types.ForkBranch, maxConcurrent int) ...
package switchpkg  // func If(g, id, cond, trueID, falseID) / func Switch(g, id, cases...) ...
package approve // func Add(..., gate ApprovalGate, factory types.AgentFactory) ...
package emit    // func Add(g, id, key string) ...
package checkpoint // func Add(g, id string) ...
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 | 依赖 |
|---|------|------|---------|------|
| 1 | core/types/context.go | ~~WorkflowContext, WorkPlanResult, NodeResult~~ | DTO | 无 |
| 2 | core/types/status.go | Status 枚举 + 字符串转换 | 枚举 | 无 |
| 3 | core/types/snapshot.go | Snapshot 纯数据类型 | DTO | 无 |
| 4 | core/node/base_node.go | Node 接口 + BaseNode + NodeKind | 接口抽象 | types |
| 5 | core/node/llm_node.go | LLMNode + LLMProvider 接口 | Strategy | node |
| 6 | core/node/agent_node.go | AgentNode + Agent/AgentFactory 接口 | Strategy | node |
| 7 | core/node/function_node.go | FunctionNode | Adapter | node |
| 8 | core/edge/edge.go | Edge + EdgeCondition + Resolve + SwitchCase/ForkBranch | Strategy | types |
| 9 | runtime/graph.go | Graph 结构体 + CRUD + Resolve | Repository | node, edge |
| 10 | runtime/validate.go | ValidateGraph + Cyclic + Orphan | Visitor | graph |
| 11 | runtime/scheduler.go | Scheduler 执行循环 | Template Method | graph, executor |
| 12 | runtime/executor.go | Executor.RunNode + renderTemplate | Adapter | node |
| 13 | runtime/checkpoint.go | Checkpoint Manager + Store | Memento | types |
| 14 | runtime/runner.go | Runner.Run + Runner.Resume | Facade | scheduler, checkpoint |
| 15 | runtime/serialize.go | Plan/PlanNodeSpec + ToPlan/FromPlan | Memento | graph, types |
| 16 | sugar/auto.go | Auto() 构建函数 + StrategyNode | Builder | graph, node, types |
| 17 | sugar/loop.go | Loop() + LoopNode + Signal | Builder | graph, node |
| 18 | sugar/fork.go | Fork() + ForkNode | Builder | graph, node |
| 19 | sugar/switch.go | If()/Switch() + ControlNode | Builder | graph, edge |
| 20 | sugar/approve.go | Approve() + ApproveNode + ApprovalGate | Builder + Strategy | graph, node |
| 21 | sugar/emit.go | Emit() + EmitNode | Builder | graph, node |
| 22 | sugar/checkpoint.go | Checkpoint() + CheckpointNode | Builder | graph, node |
| 23 | gate.go | CLI/HTTP/SDK ApprovalGate | Facade | approve |
| 24 | workplan.go + workplan_test.go | 顶层 WorkPlan + 集成测试 | Facade | runtime, sugar |

## 测试策略

| 层级 | 策略 | 覆盖目标 |
|------|------|---------|
| 单元测试 (package 级) | `*_test.go` 包内测试 | 每个包 ≥80% 语句覆盖 |
| 集成测试 (package test) | `workplan_test.go` | 端到端 WorkPlan.Run() |
| 验证测试 | `go vet ./workplan/...` | 零警告 |
| 竞态检测 | `go test -race ./workplan/...` | 零 data race |
| 循环依赖检测 | `go list -e ./workplan/...` | 零循环导入错误 |

## 回滚方案

| 阶段 | 回滚方式 |
|------|---------|
| M1-M6 构建期 | 删除新建子目录即可，不影响现有 workplan/ |
| M7 替换期 | `git checkout -- workplan/` 恢复旧文件 |
| 全部完成 | `git revert` 最后一个 commit |

## 实施路径确认

根据用户要求，**从零开始**意味着：

1. 新文件全部在 `workplan/` 下新建子目录
2. 不触及当前 `workplan/*.go` 这 5 个文件
3. 旧 worktree 代码只做逻辑参考，不作为文件复制源
4. 所有新文件编译通过后，整体替换旧文件
