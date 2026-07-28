# 前置审查报告

## 需求摘要

对 `workplan/` 图编排模块做策略模式重构，使节点可组合多种执行策略（Method/LLM/Agent）；在示例中新增 graph-as-tools 模式，让 LLM 通过 tool_call 动态调度子 WorkPlan（fork/loop/pipeline）。

## 影响文件清单

### 模块 1：策略模式重构（workplan/）

| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `workplan/strategy.go` | **新增** | - | 定义 `NodeStrategy` 接口 + `MethodStrategy` / `LLMStrategy` / `AgentStrategy` 三种内置实现 |
| `workplan/runner.go` | **修改** | 全文件 (~320行) | `autoRunner` 重写为 `strategyRunner`，通过组合 `NodeStrategy` 委派执行；`controlRunner`/`loopRunner`/`forkRunner`/`checkpointRunner`/`emitRunner`/`approveRunner` 保留或逐步迁移 |
| `workplan/node.go` | **修改** | L276-L320 `node` struct | 新增 `strategy NodeStrategy` 字段；`NodeKind` 新增 `kindStrategy`；移除部分废弃字段 |
| `workplan/sugar.go` | **修改** | L48-L175 (Auto/Approve/Checkpoint/Emit) | 新增 `Strategy()` 糖方法；`Auto()` 内部改为构造 `AgentStrategy`；新增 `Method()` / `LLM()` 糖 |
| `workplan/plan.go` | **微调** | L127-L237 (`Run` 方法) | `runner.Run()` 调用路径不变，策略节点的选择逻辑由 `strategyRunner` 统一处理 |
| `workplan/graph.go` | **不修改** | - | 图引擎不感知策略，`NodeRunner` 接口不变 |
| `workplan/validate.go` | **不修改** | - | 校验逻辑不变 |

### 模块 2：Graph-as-Tools 示例

| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `example_Implement/05_graph_tools/` | **新增目录** | - | 全新示例：将图编排基础语法（fork/loop/pipeline）封装为 tools，展示子 WorkPlan 作为 tool 被 LLM 调用 |
| `example_Implement/05_graph_tools/main.go` | **新增** | - | 主入口 + 定义 tools + 注册到 Agent → 对话演示 |
| `example_Implement/05_graph_tools/README.md` | **新增** | - | 说明 graph-as-tools 的设计模式 |
| `workplan/sugar.go` | **修改** | 尾部 | 新增 `AsTool()` 方法将 WorkPlan 构建函数包装为 `ToolEntry`（可选，便于示例中直接使用） |
| `workplan/plan.go` | **修改** | 尾部 | 新增 `ToTool()` 方法将 WorkPlan 实例包装为可注册的 ToolEntry（可选） |

### 模块 3：前端影响（现有文件不破坏）

| 文件路径 | 修改类型 | 说明 |
|---------|---------|------|
| `example_Implement/03_workplan/main.go` | **不修改** | 现有示例保持向后兼容 |
| `core/agent/agent.go` | **不修改** | Agent 层不需要知道策略模式 |
| `core/tool/interface.go` | **不修改** | `ToolHandler` / `ToolEntry` 接口不变 |
| `core/tool_holder/holder.go` | **不修改** | 工具注册中心不变 |

## 依赖分析

### 上游依赖（哪些模块依赖 workplan/）

```
example_Implement/03_workplan/main.go  → workplan (使用 sugar API)
sdk/                                    → workplan (如有 SDK 封装)
```

**影响**：策略模式是后向兼容的，`sugar.go` 的公有方法签名不变，上游无需改动。

### 下游影响（workplan/ 依赖哪些模块）

```
workplan/  → 无外部包依赖（纯标准库 + workplan 内部类型）
```

**优势**：workplan 包无外部依赖（除标准库），修改风险完全局限在包内。

### 内部依赖链（workplan 包内）

```
sugar.go  → 构造 Runner + 调用 graph.AddNode/AddEdge
runner.go → 实现 NodeRunner 接口
graph.go  → 提供 Execute + resolve 引擎
plan.go   → Run()/Resume() 调用 runner.Run() + graph.resolve()
node.go   → 定义 NodeKind、node struct、Signal 等
validate.go → 校验 node 配置和拓扑
primitive.go → approve 节点的 prepare/execute（保留，不修改）
gate.go   → 审批门接口（保留，不修改）
```

**策略模式改造后**：
```
sugar.go  → 构造 Strategy → 构造 strategyRunner(NodeRunner 包装 Strategy)
strategy.go → 定义 NodeStrategy 接口 + 三种实现
runner.go → strategyRunner 持有 NodeStrategy，Run() 委派给 strategy.Execute()
```

## 循环依赖检查

- ✅ `workplan/` 包内无循环依赖：各文件职责清晰分离
- ✅ `workplan/` 不依赖 `core/agent/` 或 `core/tool/`，不会引入跨包循环
- ✅ `example_Implement/` 单向依赖 `workplan` + `core/agent`

**结论**：无新增循环依赖风险。

## 架构影响评估

### 当前架构（v0.4）

```
NodeRunner (interface)
  ├── autoRunner        ← Agent ReAct 循环
  ├── controlRunner     ← 控制节点（If/Switch/Gate 透传）
  ├── loopRunner        ← 循环体
  ├── forkRunner        ← 并发分支
  ├── checkpointRunner  ← 快照
  ├── emitRunner        ← 变量写入
  └── approveRunner     ← 人工审批
```

问题：扩展新的"执行行为"（如纯 LLM 调用、Go 函数计算）必须新增 Runner 实现，Runner 数量线性增长。

### 目标架构（策略模式）

```
NodeRunner (interface)  ← 图引擎只看到这个
  └── strategyRunner     ← 唯一的 Runner 实现，组合一个 NodeStrategy

NodeStrategy (interface) ← 策略接口，用户可自定义
  ├── MethodStrategy     ← 执行 Go 函数（纯本地计算）
  ├── LLMStrategy        ← 只调 LLM，无工具
  ├── AgentStrategy      ← 完整 ReAct 循环 + 工具（当前 autoRunner 的行为）
  └── ...                 ← 用户自定义 Strategy

# 保留独立的 Runner（不迁移到策略模式）：
  ├── controlRunner      ← If/Switch/Gate 是拓扑控制逻辑，非执行策略
  ├── loopRunner         ← 循环控制，策略模式难以表达迭代语义
  ├── forkRunner         ← 并发控制，策略模式难以表达多 Agent 管理
  ├── checkpointRunner   ← 基础设施
  ├── emitRunner         ← 基础设施
  └── approveRunner      ← 审批流程控制
```

### 取舍说明

| 保留 Runner | 原因 |
|------------|------|
| `controlRunner` | 不执行实质工作，只做路由透传，是 Edge.Condition 的配套逻辑 |
| `loopRunner` | 循环迭代 + Signal 通知属于流程控制，非"执行策略" |
| `forkRunner` | 并发管理 + Join 汇合属于流程控制 |
| `checkpoint/emit/approveRunner` | 基础设施/审批流程，不按执行策略变化 |

### 关键接口设计

```go
// NodeStrategy 是节点的执行策略接口。
// 节点通过组合不同的 Strategy 获得不同的执行行为。
type NodeStrategy interface {
    // Execute 执行策略逻辑，返回 JSON 输出。
    Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error)
}

// strategyRunner 是唯一的 NodeRunner 实现（对图引擎透明）。
type strategyRunner struct {
    id       string
    strategy NodeStrategy
}

func (r *strategyRunner) ID() string { return r.id }
func (r *strategyRunner) Run(ctx context.Context, ec *ExecutionContext) (string, error) {
    return r.strategy.Execute(ctx, "", ec)
}
```

### Graph-as-Tools 模式

新增示例展示的"图编排作为工具"的核心模式：

```go
// 将 WorkPlan 包装为 ToolHandler
engine.RegisterInlineTool("fork_analysis", "并发执行多个分析任务",
    provider.SchemaOf(ForkInput{}),
    func(ctx context.Context, argsJSON string) (string, error) {
        // 解析参数
        // 构建子 WorkPlan（使用 Fork 等语法）
        // 执行子 WorkPlan
        // 返回合并结果
    },
)
```

## 风险预估

| 风险 | 概率 | 严重程度 | 缓解措施 |
|------|------|---------|---------|
| 策略模式引入后，旧 `autoRunner` 用户需迁移 | 低 | 中 | `Auto()` 糖方法内部自动使用 `AgentStrategy`，对外无感知 |
| `loopRunner`/`forkRunner` 保留在策略模式外导致设计不一致 | 中 | 低 | 文档明确说明界限：循环/并发是流程控制，非执行策略 |
| 自定义 Strategy 的工厂/注入机制过于复杂 | 中 | 中 | 保持简单：直接用 `Strategy(myImpl)` 糖方法注入 |
| Graph-as-Tools 示例中 WorkPlan 构建参数解析复杂 | 中 | 低 | 限制示例的复杂度，只展示 fork/loop/pipeline 三种基本模式 |
| 现有 example_Implement/03_workplan 因重构被破坏 | 低 | 高 | 所有公有方法签名不变，`Run()` 之前一定有回归测试 |
| workplan 包新增对 `core/tool` 的依赖 | 低 | 中 | 保持 `ToTool()` 可选，不增加 workplan 的核心依赖 |

## 文件变更汇总

| 操作 | 文件 | 修改量 |
|------|------|-------|
| 新增 | `workplan/strategy.go` | ~150 行 |
| 修改 | `workplan/runner.go` | ~150 行（autoRunner 改造 + 新增 strategyRunner） |
| 修改 | `workplan/node.go` | ~20 行（新增 strategy 字段 + kindStrategy） |
| 修改 | `workplan/sugar.go` | ~60 行（新增 Strategy/Method/LLM 糖方法） |
| 修改 | `workplan/plan.go` | ~30 行（新增 ToTool 方法） |
| 新增 | `example_Implement/05_graph_tools/main.go` | ~250 行 |
| 新增 | `example_Implement/05_graph_tools/README.md` | ~30 行 |
| **无修改** | `workplan/graph.go` | 0 |
| **无修改** | `workplan/validate.go` | 0 |
| **无修改** | `workplan/primitive.go` | 0 |
| **无修改** | `workplan/gate.go` | 0 |
| **无修改** | `core/agent/agent.go` | 0 |
| **无修改** | `core/tool/interface.go` | 0 |
| **无修改** | `core/tool_holder/holder.go` | 0 |

## 建议方案

### 分步执行计划

1. **新建 `workplan/strategy.go`**：定义 `NodeStrategy` 接口 + 三种内置实现
2. **改造 `workplan/runner.go`**：新增 `strategyRunner`，保留现有 runner 不动
3. **修改 `workplan/node.go`**：`node` struct 新增 strategy 字段
4. **修改 `workplan/sugar.go`**：新增 `Strategy()` / `Method()` / `LLM()` 糖方法
5. **修改 `workplan/plan.go`**：新增 `ToTool()` 可选方法
6. **新增 `example_Implement/05_graph_tools/`**：完整示例，包含 fork/loop/pipeline 作为 tools
7. **回归测试**：跑通现有 03_workplan 示例
8. **接口契约验证**：确保糖方法签名 100% 后向兼容

### 接口契约（向后兼容承诺）

- `workplan.WorkPlan` 所有公有方法签名不变
- `NodeRunner` 接口不变（strategyRunner 实现同一接口）
- `Graph` / `Edge` / `ExecutionContext` 不变
- 新增的 `Strategy()` / `Method()` / `LLM()` 不影响已有链式调用
