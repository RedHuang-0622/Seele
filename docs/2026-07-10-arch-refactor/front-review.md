# 前置审查报告

## 需求摘要
对 Seele 框架进行 5 项架构级重构：Tracer OTel 化、WorkPlan 声明式 Plan、Engine chatLoop 解耦、NodeStrategy 接口清理、WorkPlan 糖→Agent 工具。

## 影响文件清单

### 1. Tracer OTel 化 — contexts/tracer 包重构
| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `contexts/tracer/tracer.go` | **修改** | L232-L366 SimpleTracer | 在 simpleSpan.End() 中增加 OTel span 发射，保持内部 Tree 结构不变用于 Export() |
| `contexts/tracer/tracer.go` | **修改** | L169-L189 Tracer 接口 | 可选：增加 Shutdown/ForceFlush 方法（OTel 生命周期） |
| `go.mod` | **修改** | 新增依赖 | 增加 `go.opentelemetry.io/otel` `go.opentelemetry.io/otel/sdk` `go.opentelemetry.io/otel/trace` |
| `contexts/tracer/tracer_test.go` | **修改** | 所有 test | 增加 OTel span 发射验证 |
| `engine/engine.go` | **不改** | — | Engine 只引用 Tracer 接口，无侵入 |
| `engine/loop.go` | **不改** | — | chatLoop 只调用 StartSpan/End，无侵入 |

**关键设计**：
- NoopTracer 继续零开销（不引入 OTel）
- SimpleTracer 在 End() 中**同步**发射 OTel span（通过 instance-level OTel TracerProvider）
- Export() 继续返回 Tree 用于调试/JSON 序列化（向后兼容）
- OTel 依赖为可选——只在创建 SimpleTracer 时引入

### 2. WorkPlan 声明式 Plan — workplan 包新增 Plan 类型
| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `workplan/plan.go` | **新增** | Plan 结构体 | JSON 可序列化的 Plan 定义：NodeSpec + EdgeSpec |
| `workplan/plan.go` | **修改** | WorkPlan | 新增 LoadPlan(plan) 方法，从 Plan 重建图 |
| `workplan/plan.go` | **修改** | WorkPlan | 新增 ToJSON() / FromJSON() 序列化方法 |
| `workplan/sugar.go` | **不改** | — | 糖 API 保持原样，作为 node/edge 封装 |
| `workplan/graph.go` | **不改** | — | Graph/Edge 结构体可直接组合进 Plan |
| `workplan/node.go` | **修改** | NodeSpec 导出类型 | node 的 JSON 可序列化版本（不含函数字段） |
| `workplan/strategy.go` | **不改** | — | Strategy 由 factory 重建 |
| `workplan/validate.go` | **修改** | Validate | 同时支持 Plan 和 WorkPlan 的校验 |

**关键设计**：
- Plan 是纯数据（不含 Go 函数引用），可 JSON 序列化
- Edge.Condition 在 Plan 中用字符串标签（如 `"on_pass"` `"on_fail"`），执行时映射到真实条件函数
- NodeStrategy 由 Plan 中的 NodeKind + config 重建（通过 strategy registry）
- 糖 API 内部：`Auto(id, input)` → 等价于 `plan.Add("id", kindAuto, input)` + 自动边

### 3. Engine chatLoop 解耦 — engine 包 Loop 策略化
| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `engine/loop.go` | **重构** | 全部重写 | 提取 Loop 接口 + ReActLoop 结构体 |
| `engine/loop.go` | **新增** | Loop 接口 | `Run(ctx, userInput, onChunk) (string, error)` |
| `engine/loop.go` | **新增** | ReActLoop | 当前 chatLoop 逻辑移入 ReActLoop |
| `engine/engine.go` | **修改** | Engine 结构体 | 持有 `loop Loop` 而非直接实现 |
| `engine/engine.go` | **增加** | WithLoop option | 允许外部注入不同 Loop 实现 |
| `engine/engine.go` | **增加** | 默认策略 | 不指定时默认使用 ReActLoop（向后兼容） |
| `engine/engine_test.go` | **不改** | — | 通过 Engine.Chat/ChatStream 调用，无需改 |

**关键设计**：
```go
type Loop interface {
    Run(ctx context.Context, userInput string, onChunk func(string)) (string, error)
}

type ReActLoop struct {
    agent    *agent.Agent
    llm      types.ChatCompleter
    history  []types.Message
    tracer   tracer.Tracer
    cfg      SessionConfig
    sessionID string
    cache    cache.Provider
    store    *storage.Store
}

type ReActLoopOption func(*ReActLoop)
// NewReActLoop(agent, opts...)
```

**依赖关系**：
- Engine → Loop (接口) + agent + llm
- ReActLoop → agent + llm + history + tracer + cache + storage
- Engine 作为外观层，不关心 Loop 内部实现

### 4. NodeStrategy 接口清理 — workplan/strategy.go
| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `workplan/strategy.go` | **修改** | L31-L38 NodeStrategy 接口 | 去掉 `input` 参数：`Execute(ctx, ec)` |
| `workplan/strategy.go` | **修改** | L56-L63 MethodStrategy | 模板渲染移入 strategyRunner，策略只接收 ec.PrevOutput |
| `workplan/strategy.go` | **修改** | L81-L93 LLMStrategy | 同上 |
| `workplan/strategy.go` | **修改** | L113-L128 AgentStrategy | 同上 |
| `workplan/runner.go` | **修改** | L30-L32 strategyRunner.Run | 框架层统一做 renderTemplate，传入 ec.PrevOutput |
| `workplan/cache_strategy.go` | **修改** | L95-L117 Execute | 适配新签名 |
| `workplan/strategy_test.go` | **修改** | 所有 test | 适配新接口 |

### 5. WorkPlan 糖 → Agent 工具
| 文件路径 | 修改类型 | 具体位置 | 修改原因 |
|---------|---------|---------|---------|
| `workplan/plan.go` | **修改** | L276-L302 ToTool | 增强：支持动态参数绑定、Plan 序列化作为工具元信息 |
| `workplan/plan.go` | **新增** | AsAgentTool | 将 WorkPlan 注册为 Agent 的内置工具 |
| `agent/agent.go` | **新增** | RegisterWorkPlan | 或扩展 RegisterTool 支持 WorkPlan 的 Plan 注入 |
| `agent/gateway/tool/gateway.go` | **不改** | — | 工具网关协议不需要改 |

## 依赖分析

### 依赖方向
```
types/ ← workplan/ ← engine/ ← agent/
   ↓         ↓          ↓
 contexts/  (无额外)   (无额外)
   ↓
 tracer/
```

### 关键依赖链
```
workplan.NodeStrategy
  └─ MethodStrategy  → 无外部依赖（纯 Go 函数）
  └─ LLMStrategy     → AgentFactory（接口，由 engine/agent 注入）
  └─ AgentStrategy   → AgentFactory（同上）
  └─ CachedStrategy  → 闭包注入 cache getter/setter

engine.chatLoop
  └─ tracer.Tracer
  └─ agent.Agent (VisibleTools, Dispatch)
  └─ llm.ChatCompleter (Complete, CompleteStream)
  └─ cache.Provider
  └─ storage.Store
  └─ seelectx (CompressHistory, EstimateHistoryTokens)
```

### 循环依赖检查
- [✅] **tracer 包**：零依赖，不引入外部引用，OTel 为新增依赖无循环风险
- [✅] **workplan 包**：只依赖 types（消息类型），无循环依赖风险
- [⚠️] **engine 包**：依赖 agent（完整编排器）+ seelectx + tracer + cache + storage — **重构后 Loop 实现仍引用这些包**，但 Engine 层只持有 Loop 接口，依赖方向不变
- [⚠️] **NodeStrategy 接口清理**需注意：framework 层 renderTemplate 在 runner.go 中执行，strategy.go 只依赖 `*ExecutionContext`，不引入循环

## 风险预估

| # | 风险 | 概率 | 严重程度 | 缓解措施 |
|---|------|------|---------|---------|
| R1 | OTel 依赖增加二进制体积 | 中 | 低 | 通过 build tags 做可选编译：`//go:build otel` |
| R2 | Plan JSON 序列化不能序列化 Edge.Condition 函数 | 高 | 中 | 用字符串标签 + ConditionRegistry 做执行时映射 |
| R3 | chatLoop 拆 Loop 接口时未覆盖所有场景（如流式） | 中 | 高 | Loop.Run 签名统一含 onChunk，ReActLoop 内部判断 |
| R4 | NodeStrategy 接口签名变更破坏现有用户自定义策略 | 高 | 中 | 旧接口标记 @Deprecated，提供迁移期两个接口并行 |
| R5 | ToTool 增强后与 agent.RegisterTool 的集成边界不清 | 中 | 中 | 明确 workplan 包不依赖 agent 包，通过回调注册 |

## 建议实施方案

### 实施顺序（先独立后耦合）
```
Phase 1: NodeStrategy 接口清理（范围最小、0 外部依赖）
  1.1 修改 NodeStrategy 接口去 input 参数
  1.2 strategyRunner 接管模板渲染
  1.3 更新策略实现 + 测试

Phase 2: WorkPlan 声明式 Plan（独立新增，不破坏现有糖）
  2.1 定义 Plan 结构体（NodeSpec + EdgeSpec）
  2.2 添加 Plan.Add / Plan.Edge 方法
  2.3 WorkPlan.LoadPlan(plan) 从 Plan 重建图
  2.4 Plan JSON 序列化/反序列化
  2.5 Edge.Condition 字符串标签 → ConditionRegistry

Phase 3: WorkPlan 糖 → Agent 工具（基于 Phase 2）
  3.1 增强 ToTool：Plan 序列化作为工具元信息
  3.2 agent.RegisterTool 扩展接受 WorkPlanTool
  3.3 冒烟测试：Agent 通过 tool_call 调用 WorkPlan

Phase 4: Engine chatLoop 解耦（最大风险、最后做）
  4.1 定义 Loop 接口
  4.2 提取 ReActLoop 结构体（从 engine/loop.go 移入）
  4.3 Engine 改为持有 Loop
  4.4 确保 Chat/ChatStream 向后兼容

Phase 5: Tracer OTel 化（依赖已稳定）
  5.1 新增 OTel 依赖
  5.2 SimpleTracer End() 发射 OTel span
  5.3 Export() 保持原有 JSON 输出向后兼容
  5.4 集成测试验证 OTel 输出
```
