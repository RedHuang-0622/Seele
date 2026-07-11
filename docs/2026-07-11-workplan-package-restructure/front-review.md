# 前置审查报告

## 需求摘要

将 `workplan/` 从扁平包重构为三层架构：
- **core/** → 拆分为 `core/node/`、`core/edge/`、`core/types/` 三个子包（描述层）
- **runtime/** → 新建包，含 `graph`、`scheduler`、`executor`、`runner`、`checkpoint`、`serialize`、`validate` 七个模块
- **sugar/** → 7个子包：`auto`、`loop`、`fork`、`switch`、`approve`、`emit`、`checkpoint`
- **顶层**：`gate.go`、`workplan.go`、`workplan_test.go`

依赖规则：`sugar → runtime → core`（绝对单向），`core` 零依赖。

## 影响文件清单

### 现有需要重定位的文件（5个）

| 文件 | 修改类型 | 新位置/归属 | 修改原因 |
|------|---------|------------|---------|
| `workplan/workplan.go` | 重写 | `workplan/workplan.go`（顶层） | 现引用 `core` 包类型，需改为引用 `core/types/` + `runtime/` |
| `workplan/plan.go` | 删除→新建 | `workplan/workplan.go`（合并） | Plan 序列化逻辑归入 `runtime/serialize`，Plan 类型归入 `core/types/` |
| `workplan/gate.go` | 保留+重写 | `workplan/gate.go`（顶层） | 审批门实现保留在顶层，但依赖 `sugar/approve` → 改为依赖 `runtime/runner` |
| `workplan/validate.go` | 删除 | → `runtime/validate/validate.go` | 验证逻辑下放到执行层 |
| `workplan/tracer_internal.go` | 保留 | `workplan/tracer_internal.go`（顶层） | Tracer 接口是顶层概念，与 core/runtime 无关 |

### 需要新建的文件（24+个，按 Milestone）

#### Milestone 1 — core/types/（3个）
| 文件 | 内容 |
|------|------|
| `workplan/core/types/context.go` | `WorkflowContext` — 避免与 stdlib `context.Context` 冲突 |
| `workplan/core/types/status.go` | `Status` 枚举（Pending/Running/Completed/Failed/Aborted） |
| `workplan/core/types/snapshot.go` | `Snapshot` + `Checkpoint` 纯数据类型 |

#### Milestone 2 — core/node/（4个）
| 文件 | 内容 |
|------|------|
| `workplan/core/node/base_node.go` | `Node` 接口 + `DefaultNode` + `Builder` + `MockNode` |
| `workplan/core/node/llm_node.go` | `LLMNode` + `LLMProvider` 接口 |
| `workplan/core/node/agent_node.go` | `AgentNode` + `Agent` 接口 |
| `workplan/core/node/function_node.go` | `FunctionNode` + builder |

#### Milestone 3 — core/edge/（1个）
| 文件 | 内容 |
|------|------|
| `workplan/core/edge/edge.go` | `Edge` 结构体 + 状态机 + `Condition` |

#### Milestone 4 — runtime/（6个）
| 文件 | 内容 |
|------|------|
| `workplan/runtime/graph.go` | `Graph` 定义 + 构建 |
| `workplan/runtime/validate.go` | DAG 校验 + 环检测 + 孤点检测 |
| `workplan/runtime/scheduler.go` | 调度逻辑（顺序/并发/等待） |
| `workplan/runtime/executor.go` | 实际调用 `Node.Run` |
| `workplan/runtime/checkpoint.go` | 快照存取 + Resume 逻辑 |
| `workplan/runtime/runner.go` | `Run()` / `Resume()` 入口 |

#### Milestone 5 — runtime/serialize（1个）
| 文件 | 内容 |
|------|------|
| `workplan/runtime/serialize.go` | `Plan ↔ Graph` 互转 |

#### Milestone 6 — sugar/（7个，可并行）
| 文件 | 内容 |
|------|------|
| `workplan/sugar/auto.go` | `Auto()` 构建函数 |
| `workplan/sugar/loop.go` | `Loop()` + `Signal` 构建函数 |
| `workplan/sugar/fork.go` | `Fork()` 构建函数 |
| `workplan/sugar/switch.go` | `If()` / `Switch()` 构建函数 |
| `workplan/sugar/approve.go` | `Approve()` 构建函数 + `ApprovalGate` 接口 |
| `workplan/sugar/emit.go` | `Emit()` 构建函数 |
| `workplan/sugar/checkpoint.go` | `Checkpoint()` 构建函数 |

#### Milestone 7 — 顶层（2个）
| 文件 | 内容 |
|------|------|
| `workplan/gate.go` | CLI/HTTP/SDK 统一入口（保留重写） |
| `workplan/workplan_test.go` | 集成测试 |

### 现有测试文件影响

| 文件 | 影响类型 | 说明 |
|------|---------|------|
| `test/workplan_test.go` | 需要更新 | 引用了 `workplan/core` 包类型，`core.Split*` 需改为新路径 |
| `test/helpers_test.go` | 需要更新 | 引用了 `workplan` 和 `workplan.Agent`，需适配新包路径 |
| `example_Implement/03_workplan/main.go` | 需要更新 | 引用了 `workplan` + `workplan/core` |

## 旧 worktree 代码复用分析

| 来源 | 代码量 | 复用策略 |
|------|--------|---------|
| `.claude/worktrees/wf_463ba6a7-468-1/core/node.go` | ~144行 | **复用核心**：`Node` 接口 + `BaseNode` + `NodeKind` + `ExecutionContext` + `WorkPlanResult` |
| `.claude/worktrees/wf_463ba6a7-468-2/core/edge.go` | ~60行 | **复用核心**：`Edge` + `EdgeCondition` + `Resolve` + `SwitchCase` + `ForkBranch` |
| `.claude/worktrees/wf_463ba6a7-468-3/core/graph.go` | ~154行 | **复用核心**：`Graph` 原子操作 + `AddNode`/`AddEdge`/`Resolve`/`Validate` |
| `.claude/worktrees/wf_463ba6a7-468-6/core/core.go` | ~126行 | **概念参考**：最小子集版 `ExecutionContext` + `ForkBranch` + `Agent`/`AgentFactory` + `RenderTemplate` |
| `.claude/worktrees/wf_0d87fa1d-c83-2/sugar/auto/auto.go` | ~193行 | **复用核心**：`NodeStrategy` + `MethodStrategy`/`LLMStrategy`/`AgentStrategy` + `StrategyNode` + `RenderTemplate` |
| `.claude/worktrees/wf_0d87fa1d-c83-3/sugar/loop/loop.go` | ~97行 | **复用核心**：`LoopNode` + `Signal` + `WithUntil`/`WithMaxIter`/`WithOnExhausted` |
| `.claude/worktrees/wf_0d87fa1d-c83-4/sugar/fork/fork.go` | ~66行 | **复用核心**：`ForkNode` + `Run` + 并发控制 + 结果合并 |
| `.claude/worktrees/wf_0d87fa1d-c83-5/sugar/switch/switch.go` | ~56行 | **复用核心**：`ControlNode` + `If`/`Switch`/`Contains` |
| `.claude/worktrees/wf_0d87fa1d-c83-6/sugar/approve/approve.go` | ~153行 | **复用核心**：`ApproveNode` + `ApprovalGate` + `Question` + `ChoiceOption` |
| `.claude/worktrees/wf_0d87fa1d-c83-7/sugar/emit/emit.go` | ~27行 | **复用核心**：`EmitNode` + `Run` |
| `.claude/worktrees/wf_0d87fa1d-c83-7/sugar/checkpoint/checkpoint.go` | ~25行 | **复用核心**：`CheckpointNode` + `Add` |
| `.claude/worktrees/wf_463ba6a7-468-7/node/checkpoint.go` | ~27行 | **参考**：另一种 CheckpointNode 实现 |

> **策略**：旧 worktree 代码可以直接复制到新结构，但需要适配：
> 1. 分拆 `core/types/` 子包：`NodeKind` → `core/types/`，`ExecutionContext` → `core/types/`
> 2. `Node` 接口移入 `core/node/`（`core` 不再有 flat `core.go`）
> 3. `Edge` + `EdgeCondition` 移入 `core/edge/`
> 4. `Graph` 移入 `runtime/graph/`
> 5. 旧 worktree core.go 中的 `Agent`/`AgentFactory` → `core/node/agent_node.go`

## 依赖关系分析

### 新架构依赖链

```
workplan (顶层)
  ├── runtime/runner
  │     ├── runtime/scheduler
  │     │     ├── runtime/executor
  │     │     │     └── core/node (Node 接口)
  │     │     └── runtime/validate
  │     │           └── core/types (Status)
  │     ├── runtime/checkpoint
  │     │     └── core/types (Snapshot)
  │     └── runtime/serialize
  │           └── core/types (Plan/EdgeSpec/NodeSpec)
  ├── sugar/
  │     ├── sugar/auto
  │     │     ├── core/node
  │     │     └── runtime/runner (Graph 引用)
  │     ├── sugar/loop → core/node + runtime/graph
  │     ├── sugar/fork → core/node + runtime/graph
  │     ├── sugar/switch → core/edge + runtime/graph
  │     ├── sugar/approve → core/node + runtime/graph
  │     ├── sugar/emit → core/node + runtime/graph
  │     └── sugar/checkpoint → core/node + runtime/graph
  └── gate.go (CLI/HTTP/SDK)
        └── sugar/approve (ApprovalGate 接口)
```

### 关键依赖约束

| 约束 | 状态 |
|------|------|
| `core/` 不依赖 `runtime/` | ✅ 可保证 — core 只定义类型和接口 |
| `core/` 不依赖 `sugar/` | ✅ 可保证 |
| `runtime/` 依赖 `core/` | ✅ 必需 — `runtime` 使用 `core` 类型 |
| `sugar/` 依赖 `runtime/` | ✅ 必需 — sugar 构建函数操作 runtime Graph |
| 顶层 `workplan` 依赖 `runtime` + `sugar` | ✅ 当前就是，需拆包适配 |

### 核心挑战：sugar 包引用 Graph

旧 worktree 中的 sugar 包直接引用 `core.Graph`（`g *core.Graph Add*`），但新架构中 `Graph` 在 `runtime/graph` 中。这意味着：
- **sugar 必须依赖 runtime**（违反"零依赖"？不——这是设计允许的：`sugar → runtime → core`）
- 或者 sugar 仍然引用 `core.Graph` 接口定义，但实际导入 `runtime/graph`
- 根据架构规则：`sugar → runtime → core`，所以 sugar 直接 import `runtime/graph` 是正确的

## 循环依赖检查

### 高风险的循环依赖隐患

1. **core/node ←→ core/types**：`Node` 接口的 `Run(ctx, *ExecutionContext)` 依赖 `types.ExecutionContext`；`types` 如果有 `NodeResult` 包含 `Node` 引用则形成环
   - **解决方案**：`types` 只定义纯数据类型，不包含 `Node` 接口引用。`NodeResult` 用 `nodeID string` 而非 `Node` 引用

2. **runtime/graph ←→ core/node**：`Graph` 包含 `map[string]Node`；如果 `Node` 接口引用 `Graph` 则成环
   - **解决方案**：`Node` 不得知道 `Graph` 存在。`Graph.Resolve` 使用 `core/edge.Resolve` 函数

3. **runtime/scheduler ←→ runtime/executor**：Scheduler 调用 Executor，Executor 回传结果给 Scheduler
   - **解决方案**：通过 `Scheduler.Run(ctx) → results` 单向调用，Executor 作为 Scheduler 的依赖注入

4. **sugar/auto ←→ runtime/graph**：`auto.Add*` 函数调用 `graph.AddNode`；`runtime/graph` 也可能引用 `auto.StrategyNode`
   - **解决方案**：`graph.AddNode` 接受 `core/node.Node` 接口，不直接依赖具体节点类型。sugar 包构造的具体节点调用 `graph.AddNode`，但 graph 不反向引用 sugar

### 确认无循环依赖的路径

```
core/node:  imports core/types  ✓
core/edge:  imports core/types  ✓ 
core/types: imports nothing     ✓（零依赖）
runtime/graph:   imports core/node, core/edge, core/types  ✓
runtime/scheduler: imports runtime/graph, runtime/executor, core/types  ✓
runtime/executor:  imports core/node  ✓
runtime/runner:    imports runtime/scheduler, runtime/checkpoint, runtime/serialize  ✓
sugar/*:  imports runtime/graph, core/node, core/edge, core/types  ✓
workplan (顶层): imports runtime/runner, sugar/*  ✓
```

**结论：严格遵循单向依赖链则无循环依赖。**

## 风险预估

| # | 风险 | 概率 | 严重度 | 说明 |
|---|------|------|--------|------|
| R1 | sugar 包 split 后的 import 路径膨胀 | 高 | 中 | 7个子包每个需要独立 import，DSL 构建函数的链式调用 API 可能需适配 |
| R2 | 现有 workplan.go 的全部方法（Auto/Loop/Fork/If/Switch/Emit/Approve/Step）需移植到新架构 | 高 | 高 | WorkPlan 结构体上下位引用关系复杂，移入 runtime 后需保持 API 兼容 |
| R3 | test/workplan_test.go 中 `core.ForkBranch`、`core.Split*` 等引用需全局替换 | 中 | 中 | 全局替换 + 编译验证可解决 |
| R4 | `sugar/approve` 包被 `gate.go` 和 `workplan.go` 同时引用，分包后需确认无循环 | 中 | 中 | approve 是单独子包，只引用 `core/node` 和 `runtime/graph`，风险可控 |
| R5 | `core` 现有 flat 包会被直接引用，如果通过 `core.` 前缀引用 `NodeKind` 等，改为 `types.` 后所有调用方需修改 | 高 | 高 | 所有调用方（含外部 example）都要同步更新 |
| R6 | `runtime` 包中的 `Graph` 原子操作（CAS Pointer）在新包路径下可能争用条件 | 低 | 高 | 现有实现已用 `atomic.Pointer`，语义不变；只在包级测试中有提升 |
| R7 | `workplan_test.go` 集成测试依赖 mock LLM Server + simpleAgent，这些基础设施在 `test/` 包中 | 低 | 中 | 基础测试设施不动，只需更新 import 路径 |

### 风险缓解策略

| 风险 | 缓解 |
|------|------|
| R1 (路径膨胀) | 顶层 workplan 包提供 re-export 或类型别名，用户只需 `import "workplan"` |
| R2 (方法移植) | Milestone 4 (Runtime) 先完成，Milestone 6 (Sugar) 再迁移 DSL 方法 |
| R3 (全局替换) | 每 Task 完成后 `go build ./...` 立即验证 |
| R5 (core 引用) | 先删除旧 `core/` flat 包，再创建新子包，编译错误索引所有修改位 |
| R7 (测试) | 测试文件最后修改，编译通过即可 |

## 建议方案

### 实施策略

**Bottom-Up 逐个 Milestone 构建**：

1. **Milestone 1 (core/types) 先行**：零依赖，可独立编译验证
2. **Milestone 2 (core/node) 跟上**：依赖 types，完成后可编译
3. **Milestone 3 (core/edge) 紧随**：依赖 types
4. **Milestone 4 (runtime) 逐步构建**：graph → validate → executor → scheduler → checkpoint → runner
5. **Milestone 5 (serialize)**：依赖 runner 和 types
6. **Milestone 6 (sugar) 并行**：7个子包可同时构建，每个依赖 runtime/graph
7. **Milestone 7 (顶层)**：整合所有子包，清理旧文件，更新测试

### 关键决策

- **决定1**：在 `workplan/` 根目录建 `core/` `runtime/` `sugar/` 子目录，每个目录一个 `package xxx`，但初期保留顶层 `workplan.go` 并提供类型别名/桥接
- **决定2**：旧 worktree 代码直接复用，不做重大逻辑修改——目标是**目录重构 + 包拆分**，不是重写功能
- **决定3**：`sugar/` 的 7 个子包改为 7 个独立的 package 目录以支持独立导入和测试
- **决定4**：实施期间保持 `go build ./workplan/...` 和 `go test ./...` 持续绿色
