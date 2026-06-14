# Seele v0.2 重构计划

> 目标：在不改变对外 API 的前提下，完成三处底层重构。改动完成后的版本记为 v0.2。

---

## 改动清单

| # | 文档 | 改动范围 | 对外影响 |
|---|------|---------|---------|
| 01 | [inline-provider.md](01-inline-provider.md) | ToolProvider 接口改为策略模式（ToolHandler + ToolEntry）+ tool_holder map 中转 + InlineProvider | ToolProvider 接口破性变更，但编排层零改动 |
| 02 | [graph-abstraction.md](02-graph-abstraction.md) | WorkPlan 底层重构为 Graph + NodeRunner + Edge | sugar 层零变更 |
| 03 | [naming-mapping.md](03-naming-mapping.md) | 命名对照表 | 参考文档 |

---

## 实施顺序

```
day 1    01-策略模式重构       ToolProvider 接口改 1 方法 + ToolHandler + tool_holder map
                               HubProvider / MCPProvider 适配新接口
                               此改动是 InlineProvider 和 Graph 重构的前提

day 1-2  01-InlineProvider     在新 ToolProvider 接口上加 InlineProvider
                               RegisterInlineTool → 构建 InlineToolHandler → 入 map

day 2-3  02-Graph 重构         graph.go + runner.go，sugar 委托给图引擎
                               暴露 AddNode / AddEdge 低级 API

day 3-4  回归测试              6 个 example 全部跑通
```

---

## 设计约束

1. **sugar 方法签名一个不改** — `wp.Auto().If().Fork()` 行为完全一致
2. **新老可以混用** — 糖和低级 API 可以在同一个 WorkPlan 里共存
3. **不引入新依赖** — Graph 引擎和策略模式都用纯 standard library
4. **不引入泛型** — 状态依然用 `map[string]string`，不做类型化的 State[T]
5. **`_` 前缀过滤统一到 tool_holder** — 不再分散在各 provider 实现

---

## 完成后 Seele 的架构

```
Dispatch 路径（重构后）：

  session.dispatchToolCalls()
    └─ tool_holder.Dispatch(name, argsJSON)
         └─ toolMap[name].Handler.Execute(ctx, argsJSON)   ← 策略模式
              ├─ HubToolHandler    → gRPC → Skill 进程
              ├─ MCPToolHandler    → stdio/SSE → MCP Server
              └─ InlineToolHandler → 本地 Go 函数调用

工具注册：

  HubProvider.Tools()      → []ToolEntry{ {Def, HubToolHandler{...}}, ... }
  MCPProvider.Tools()      → []ToolEntry{ {Def, MCPToolHandler{...}}, ... }
  InlineProvider.Tools()   → []ToolEntry{ {Def, InlineToolHandler{...}}, ... }
       │                          │
       └──────────┬───────────────┘
                  ↓
  tool_holder.rebuildLocked() → toolMap[name] = entry    ← 统一 map 中转
                  ↓
  Tools() → 迭代 map，过滤 _ 前缀 → 返回给 LLM
  Dispatch() → map[name].Handler.Execute()              ← O(1) 路由
```

---

## 完成后 Seele 的能力矩阵

```
工具层：
  ToolProvider 接口           — 精简为 1 方法（Tools() []ToolEntry）   ← v0.2
  ToolHandler 策略接口        — Execute(ctx, argsJSON) (string, error) ← v0.2
  ToolEntry 统一暴露结构      — {Definition + Handler}                 ← v0.2
  tool_holder map 中转        — O(1) Dispatch                          ← v0.2
  HubProvider (gRPC)          — 适配新接口，保留跨机器能力
  MCPProvider (stdio/SSE)     — 适配新接口，保留外部工具能力
  InlineProvider (Go func)    — 新增，本地函数直接注册为工具              ← v0.2
  _ 前缀工具隔离              — 集中到 tool_holder.Tools() 过滤         ← v0.2

WorkPlan 层：
  Auto/If/Switch/Loop/Fork/Approve/Gate  — 保留，sugar 不变
  NodeRunner 自定义节点                   — 新增，低级 API              ← v0.2
  Edge + Condition + Priority             — 新增，一等公民边           ← v0.2
  Graph Execute / ExecuteFrom             — 新增，图引擎               ← v0.2
```

---

## 两个改动的关系

```
策略模式重构 (01)                        Graph Abstraction (02)
───────────────────                      ──────────────────────
重构 ToolProvider 接口                    重构 WorkPlan 内部
新增：ToolHandler 策略 + map 中转         新增：Graph + NodeRunner + Edge
HubProvider/MCPProvider 适配新接口        sugar 层不变
InlineProvider 作为第三种策略加入          低级 API 暴露给高级用户

              ── 01 是 02 的前置条件 ──

原因：
  Graph 重构后的 ToolNode 需要调 Dispatch，
  策略模式重构后的 tool_holder.Dispatch() 已经是 O(1) map lookup，
  ToolNode 只需要 toolMap[toolName].Handler.Execute() 即可。
  两个改动在会合点自然衔接。
```

---

## 测试策略

```
01-策略模式 + InlineProvider 测试：
  ✅ InlineToolHandler / HubToolHandler / MCPToolHandler 实现 ToolHandler 接口
  ✅ tool_holder.Register() → map 正确构建
  ✅ tool_holder.Tools() → _ 前缀被过滤
  ✅ tool_holder.Dispatch("_decide", ...) → map 包含内部工具，路由成功
  ✅ tool_holder.Dispatch("nonexistent", ...) → error
  ✅ 同名工具冲突 → 先注册优先
  ✅ tool_holder.Unregister() → map 重建
  ✅ InlineProvider.Register / Unregister 并发安全
  ✅ HubProvider Retire/Restore 后 Tools() 返回变化 → map 反映变化
  ✅ ctx 超时 → handler 返回 error → 重试逻辑生效

02-graph-abstraction 测试：
  ✅ 6 个 example 全部跑通（sugar API 兼容性）
  ✅ AddNode + AddEdge 构建等价 WorkPlan
  ✅ Approve 暂停 + Resume 正常
  ✅ Loop 的 Signal + OnUpdate 正常
  ✅ Fork 并发正常
  ✅ 空图 → Execute 返回 error
  ✅ 单节点图 → Execute 正常
  ✅ 有向无环 → Validate 通过
  ✅ 有环（非 Loop）→ Validate 报错
  ✅ 边引用缺失节点 → Validate 报错
```
