# 代码变更摘要

## 策略模式重构 workplan + Graph-as-Tools 示例

### 新增/修改/删除文件

| 文件 | 类型 | 说明 | 设计模式 |
|------|------|------|---------|
| `workplan/strategy.go` | **新增** | NodeStrategy 接口 + 三种内置策略（Method/LLM/Agent） | Strategy + Factory Method |
| `workplan/runner.go` | **修改** | 新增 strategyRunner（NodeStrategy → NodeRunner 适配器） | Adapter |
| `workplan/node.go` | **修改** | 新增 kindMethod/kindLLM/kindStrategy + node.strategy 字段 | - |
| `workplan/sugar.go` | **修改** | 新增 Method()/LLM()/Strategy() 糖方法；Auto() 改用 AgentStrategy | Builder |
| `workplan/plan.go` | **修改** | 新增 ToTool() 包装 + WorkPlanTool 结构体 | Adapter |
| `example_Implement/05_graph_tools/main.go` | **新增** | 完整 Graph-as-Tools 示例：fork/pipeline/loop 作为工具 | Adapter（WorkPlan→ToolHandler） |

### 不修改（零影响）

| 文件 | 原因 |
|------|------|
| `workplan/graph.go` | 图引擎不感知策略，NodeRunner 接口不变 |
| `workplan/validate.go` | 校验逻辑不变 |
| `workplan/primitive.go` | approve prepare/execute 逻辑不变 |
| `workplan/gate.go` | 审批门接口不变 |
| `core/agent/agent.go` | Agent 层不需要知道策略模式 |
| `core/tool/interface.go` | ToolHandler 接口不变 |

## API 变更

| API | 变更 | 兼容性 |
|-----|------|--------|
| `wp.Auto(id, input, opts...)` | 内部改用 AgentStrategy + strategyRunner | ✅ 完全兼容，签名不变 |
| `wp.Method(id, fn, opts...)` | **新增** | 新 API |
| `wp.LLM(id, input, opts...)` | **新增** | 新 API |
| `wp.Strategy(id, strategy, opts...)` | **新增** | 新 API |
| `wp.ToTool(name, desc, schema)` | **新增** | 新 API |
| `WorkPlanTool` struct | **新增** | 新类型，不破坏现有 |

## 设计模式使用

| 模式 | 文件 | 效果 |
|------|------|------|
| **Strategy** | `strategy.go` | NodeStrategy 接口使执行算法族可互换 |
| **Adapter** | `runner.go: strategyRunner` | 桥接 NodeStrategy 和 NodeRunner，图引擎零感知 |
| **Builder** | `sugar.go: Method/LLM/Strategy` | 链式构造策略节点 |
| **Factory Method** | `strategy.go: NewMethodStrategy/NewLLMStrategy/NewAgentStrategy` | 按意图创建策略 |

## 接口抽象

| 接口 | 定义在 | 实现方 | 使用方 |
|------|--------|--------|--------|
| `NodeStrategy` | `workplan/strategy.go` | MethodStrategy, LLMStrategy, AgentStrategy（用户自定义） | strategyRunner |
| `NodeRunner` | `workplan/graph.go` | strategyRunner, controlRunner, loopRunner, forkRunner... | graph.Execute |

## 循环依赖检查

- [x] 确认无新增循环依赖
- `strategy.go` → 依赖 `plan.go`(AgentFactory) + `graph.go`(ExecutionContext) — 读写依赖，无环
- `runner.go` → 依赖 `strategy.go` + `graph.go` — 单向
- `sugar.go` → 依赖 `runner.go` + `node.go` + `strategy.go` — 单向

## Commit 记录

| Commit | Type | 子目标 | Message |
|--------|------|-------|---------|
| (待提交) | feat | G1 | feat(workplan): add NodeStrategy interface + Method/LLM/Agent strategies |
| (待提交) | feat | G2 | feat(workplan): add strategyRunner adapter in runner.go |
| (待提交) | feat | G2 | feat(workplan): add kindMethod/kindLLM/kindStrategy + strategy field to node struct |
| (待提交) | feat | G3 | feat(workplan): add Method/LLM/Strategy sugar methods; update Auto to use AgentStrategy |
| (待提交) | feat | G5 | feat(workplan): add ToTool wrapper for WorkPlan |
| (待提交) | feat | G4 | feat(example): add 05_graph_tools — fork/pipeline/loop as tools |

## 编译验证

- [x] `go build ./workplan/...` ✅
- [x] `go build ./...` ✅
- [x] `go vet ./...` ✅
- [x] `go build ./example_Implement/03_workplan/...` ✅（回归测试）
- [x] `go build ./example_Implement/05_graph_tools/...` ✅（新示例）
