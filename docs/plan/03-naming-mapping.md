# 命名映射表：重构前 → 重构后

> sugar 层方法名 —— 不变。变的是内部实现名。
> 包含两处重构：工具层策略模式 + WorkPlan 图抽象。

---

## 0. ToolProvider 接口 → 策略模式

| 旧 | 新 | 说明 |
|----|-----|------|
| `ToolProvider` (4 方法: `Name/Tools/Dispatch/HasTool`) | `ToolProvider` (1 方法: `Name/Tools`) | 接口精简 |
| `ToolProvider.Dispatch(ctx, name, args)` | `ToolHandler.Execute(ctx, args)` | 执行逻辑下沉到策略 |
| `ToolProvider.HasTool(name)` | `holder.toolMap[name]` (map lookup) | 存在性检查由 map 替代 |
| `HubProvider.Tools() []types.Tool` | `HubProvider.Tools() []ToolEntry` | 返回带 handler 的条目 |
| `MCPProvider.Tools() []types.Tool` | `MCPProvider.Tools() []ToolEntry` | 返回带 handler 的条目 |
| — | `ToolHandler` 接口 | **新增**：策略接口 |
| — | `ToolEntry` 结构体 | **新增**：`{Definition, Handler}` |
| — | `HubToolHandler` | **新增**：gRPC 策略 |
| — | `MCPToolHandler` | **新增**：stdio/SSE 策略 |
| — | `InlineToolHandler` | **新增**：Go 函数策略 |
| — | `InlineProvider` | **新增**：第三种 provider |
| `tool_holder.Tools()` (迭代 providers) | `tool_holder.Tools()` (迭代 toolMap，过滤 `_`) | `_` 过滤集中 |
| `tool_holder.Dispatch()` (O(n) 遍历) | `tool_holder.Dispatch()` (O(1) map lookup) | 策略模式 dispatch |
| `tool_holder.HasTool()` 删除 | `_, ok := toolMap[name]` | 内联化 |
| `provider.ErrToolNotFound` | 删除（必要性消失，map miss 即 not found） | — |

---

## 1. 执行原语 → Runner

| 旧名 | 新名 | 旧位置 | 新位置 |
|------|------|--------|--------|
| `primitiveAuto()` | `autoRunner.Run()` | primitive.go | runner.go |
| `primitiveLoop()` | `loopRunner.Run()` | primitive.go | runner.go |
| `primitiveFork()` | `forkRunner.Run()` | primitive.go | runner.go |
| `primitiveEmit()` | `emitRunner.Run()` | primitive.go | runner.go |
| `primitiveRunNode()` | `graph.Execute()` (寻路) + `runner.Run()` (执行) | primitive.go | graph.go + runner.go |
| `primitiveNext()` | `graph.resolve()` | primitive.go | graph.go |
| `primitiveNewAgent()` | `runner.newAgent()` | primitive.go | runner.go (各 Runner 内部) |
| `primitiveRenderInput()` | `renderTemplate()` | primitive.go | runner.go (提升为独立函数) |
| `primitiveAddNode()` | `graph.AddNode()` + `graph.AddEdge()` | primitive.go | graph.go |
| `primitiveAutoID()` | `genID()` | primitive.go | sugar.go |

---

## 2. 数据结构

### 2.1 Node → NodeRunner（接口化）

| 旧 | 新 | 说明 |
|----|-----|------|
| `node struct` (12 字段，含所有 kind 的配置) | 各 `xxxRunner struct` (各自携带相关字段) | node 结构体逐步废弃 |
| `node.id` | `runner.ID()` | 接口方法 |
| `node.kind (NodeKind)` | Runner 的类型本身 | `kindAuto` → `*autoRunner` |
| `node.next` | `Edge{From, To, Condition: nil}` | 无条件边 |
| `node.ifTrueID` / `node.ifFalseID` | `Edge{From, To: trueID, Condition: cond}` + `Edge{From, To: falseID, Condition: !cond}` | 条件边 |
| `node.switchCases` | 多条 `Edge{Condition: matchFn, Priority: i}` | 按优先级排序的条件边 |
| `node.loopBodyID` | `loopRunner.bodyID`（Runner 内部字段） | 不经过 Edge |
| `node.loopExhaustedID` | `Edge{From: loopID, To: exhaustedID, Condition: exhausted}` | 条件边 |
| `node.systemPrompt` | `runner.systemPrompt`（构造参数） | 静态配置 |
| `node.input` | `runner.input`（构造参数） | 静态配置，运行时 `renderTemplate()` 渲染 |
| `node.toolFilter` | `runner.toolFilter`（构造参数） | 静态配置 |
| `node.loopSignal` | `loopRunner.signal` | Runner 内部管理 |
| `node.loopUntil` | `loopRunner.until` | Runner 内部管理 |
| `node.loopMaxIter` | `loopRunner.maxIter` | Runner 内部管理 |
| `node.forkBranches` | `forkRunner.branches` | Runner 内部管理 |
| `node.emitKey` | `emitRunner.key` | Runner 内部管理 |
| `node.approveOptions` | `approveRunner.options` | Runner 内部管理 |
| `node.approveKVS` | `approveRunner.kvs` | Runner 内部管理 |
| `node.checkpoint` | `checkpointRunner` + `ec.Result.Checkpoints[id]` | 写入 ExecutionContext |

### 2.2 NodeKind 枚举 → Runner 类型

| 旧 | 新 |
|----|-----|
| `kindAuto` | `*autoRunner` |
| `kindApprove` | `*approveRunner` |
| `kindIf` | `*controlRunner` (直接透传，不执行) |
| `kindSwitch` | `*controlRunner` (直接透传，不执行) |
| `kindLoop` | `*loopRunner` |
| `kindFork` | `*forkRunner` |
| `kindJoin` | `*joinRunner` |
| `kindCheckpoint` | `*checkpointRunner` |
| `kindEmit` | `*emitRunner` |

`NodeKind` 常量保留兼容，但不再用于路由分发。

### 2.3 状态 → ExecutionContext

| 旧 | 新 | 说明 |
|----|-----|------|
| `WorkPlan.vars map[string]string` | `ExecutionContext.Vars map[string]string` | Emit 写入的变量 |
| 隐式的 `prevJSON`（Run 局部变量） | `ExecutionContext.PrevOutput string` | 上一节点输出 |
| `WorkPlanResult`（Run 局部变量） | `ExecutionContext.Result *WorkPlanResult` | 累积结果 |
| — | `ExecutionContext.Metadata map[string]any` | 扩展字段（新增） |

### 2.4 新增类型

| 新名 | 位置 | 说明 |
|------|------|------|
| `Graph` | graph.go | 图引擎，持有 `nodes` + `edges` + `entry` |
| `Edge` | graph.go | 一等公民边：From / To / Condition / Priority / Label |
| `EdgeCondition` | graph.go | `func(*ExecutionContext) bool` |
| `NodeRunner` | graph.go | 接口：`ID() + Run(ctx, ec) (string, error)` |
| `ExecutionContext` | graph.go | 图执行期间传递的共享状态 |

### 2.5 保留不变的类型

| 名 | 说明 |
|----|------|
| `Signal` | Loop 的进度信号 |
| `Question` | 审批问题（Q-K-V 模型） |
| `ChoiceOption` | 审批选项 |
| `ForkBranch` | Fork 的分支定义 |
| `SwitchCase` | Switch 的分支定义（Match → NextID） |
| `NodeResult` | 单节点执行结果 |
| `WorkPlanResult` | 整图执行摘要 |
| `pauseSnapshot` | 审批暂停的断点 |
| `ExecState` | 执行状态机枚举 |
| `ApproveChoice` | 审批结果常量 |

---

## 3. 方法映射

### 3.1 公有方法（对外接口）—— 完全不变

| 方法 | 签名 | 备注 |
|------|------|------|
| `Auto()` | `(id, input string, opts ...NodeOption) *WorkPlan` | 不变 |
| `Approve()` | `(id, input string, options []ChoiceOption, opts ...NodeOption) *WorkPlan` | 不变 |
| `Gate()` | `(id, input string, opts ...NodeOption) *WorkPlan` | 不变 |
| `If()` | `(id string, cond func(string) bool, trueID, falseID string) *WorkPlan` | 不变 |
| `Switch()` | `(id string, cases ...SwitchCase) *WorkPlan` | 不变 |
| `Loop()` | `(id, bodyID string, opts ...LoopOption) *Signal` | 不变 |
| `Retry()` | `(id, bodyID string, maxIter int, until func(string) bool, exhaustedID string) *Signal` | 不变 |
| `Fork()` | `(id string, branches []ForkBranch, opts ...NodeOption) *WorkPlan` | 不变 |
| `Checkpoint()` | `(id string) *WorkPlan` | 不变 |
| `Emit()` | `(id, key string) *WorkPlan` | 不变 |
| `Pipeline()` | `(steps ...PipelineStep) *WorkPlan` | 不变 |
| `Run()` | `(ctx context.Context) (*WorkPlanResult, error)` | 不变 |
| `Resume()` | `(ctx context.Context) (*WorkPlanResult, error)` | 不变 |

### 3.2 新增公有方法（低级 API）

| 方法 | 签名 |
|------|------|
| `AddNode()` | `(runner NodeRunner) *WorkPlan` |
| `AddEdge()` | `(e Edge) *WorkPlan` |
| `SetEntry()` | `(nodeID string) *WorkPlan` |

---

## 4. 文件映射

```
旧文件                          新文件 / 变更
──────────────────────────────────────────────────────
workplan/plan.go       ──→     plan.go (Run 委托给 graph.Execute，Resume 不变)
workplan/primitive.go  ──→     runner.go (逻辑迁入各 Runner)
                               graph.go (路由逻辑迁入 resolve)
                               primitive.go → 逐步消解
workplan/sugar.go      ──→     sugar.go (内部改为 graph.AddNode/AddEdge)
workplan/node.go       ──→     node.go (保留类型定义，减少字段)
workplan/validate.go   ──→     graph.go (Validate 方法)
                               validate.go → 逐步消解

新增文件：
workplan/graph.go              Graph / Edge / ExecutionContext / Execute / resolve / Validate
workplan/runner.go             autoRunner / ifRunner / switchRunner / loopRunner / forkRunner /
                               approveRunner / controlRunner / checkpointRunner / emitRunner /
                               renderTemplate

不动文件：
workplan/gate.go               三种 Gate 实现（不变）
workplan/primitive.go          剩余共享逻辑（模板渲染、JSON 规范化）
```

---

## 5. 关键语义变化

| 概念 | 旧语义 | 新语义 |
|------|--------|--------|
| "节点" | `node struct`（数据结构） | `NodeRunner` 接口（行为 + 数据） |
| "边" | 隐式（node 的字符串字段） | 显式（Edge struct，一等公民） |
| "路由" | `primitiveNext` 的 switch-case | `graph.resolve` 按优先级匹配边 |
| "状态" | `WorkPlan.vars` + Run 局部变量 | `ExecutionContext` 统一持有 |
| "线性链" | `primitiveAddNode` 自动推导 `next` | `graph.AddEdge(Edge{From: lastNodeID, To: nodeID})` |
| "条件分支" | `node.ifTrueID` / `node.ifFalseID` | `Edge{Condition: cond}` |
| "多路分支" | `node.switchCases` | 多条 `Edge{Condition: match, Priority: i}` |
| "类型分发" | `primitiveRunNode` 的 switch `n.kind` | `graph.Execute` 调 `runner.Run()`（多态） |