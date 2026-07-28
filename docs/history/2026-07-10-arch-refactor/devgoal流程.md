# Workflow: Seele 架构重构 v0.6

## 元信息
- 日期: 2026-07-10
- 规模: 深度
- 需求: 5 项架构重构（Tracer OTel / Plan 声明式 / Engine 解耦 / NodeStrategy 清理 / 糖→工具）
- 子 Skill 清单:
  - G: front-review → [front-review.md](./front-review.md)
  - O: devplan → [plan.md](./plan.md)
  - A0: soft_eng_plan → (条件式) + agent-logs/
  - A1: code-impl → code-changes.md（含 commit 记录）
  - A2: test-suite → test-report.md
  - L: finish-review → finish-review.md

## G: Goal -------------------------------
> 委托: front-review | 输出: [front-review.md](./front-review.md)

### 目标拆解
**主目标**：重构 Seele 框架 5 项架构缺陷，实现 OTel 标准可观测性、声明式 WorkPlan、可替换 Loop 策略、干净的 NodeStrategy 接口、Agent 内置工作流工具。

| # | 子目标 | 验收标准（可测量） | 优先级 |
|---|-------|------------------|-------|
| G1 | NodeStrategy 接口清理 | `Execute(ctx, ec)` 无 input 参数，模板渲染在 runner 层统一做，存量测试通过 | P0 |
| G2 | WorkPlan 声明式 Plan | Plan 结构体可 JSON 序列化/反序列化，LoadPlan 重建图，sugar API 不变 | P0 |
| G3 | Plan 糖→Agent 工具 | WorkPlan.ToTool 可注册到 Agent 的 tool holder，通过 tool_call 调用执行 | P1 |
| G4 | Engine chatLoop 解耦 | Loop 接口 + ReActLoop 实现，Engine 持有 Loop，Chat/ChatStream 无行为退化 | P0 |
| G5 | Tracer OTel 化 | SimpleTracer.End() 发射 standard OTLP span，Export() 保持 JSON 兼容 | P1 |

### 成功标准
- [ ] 功能：Chat/ChatStream 行为完全向后兼容
- [ ] 功能：sugar API（Auto/Method/LLM/Fork/Pipeline/Loop）行为不变
- [ ] 功能：WorkPlan.ToTool 可被 Agent 调度
- [ ] 质量：单元测试通过，覆盖率 ≥ 80%
- [ ] 质量：go vet 零告警
- [ ] 质量：竞态检测零 data race
- [ ] 质量：无 goroutine/channel/文件句柄泄漏
- [ ] 性能：chatLoop 路径无退化，tracer Noop 路径零分配

### 非目标（明确不做）
- [PlanAndExecuteLoop 实现] — 只提取 Loop 接口 + 保留 ReActLoop 实现，PlanAndExecute 等新 Loop 后续开发
- [条件函数序列化] — Edge.Condition 仍为 Go 函数，Plan 中用字符串标签映射，不尝试函数级序列化
- [其他 Provider 的 OTel 集成] — 只做 SimpleTracer 的 OTel span 发射，不涉及 tracing exporter 的 UI 配置

### 前置审查摘要
> 详见 [front-review.md](./front-review.md)

### 方案摘要
> 详见 [plan.md](./plan.md)

| 方案 | 核心思路 | 设计模式 | 变更范围 | 主要风险 |
|------|---------|---------|---------|---------|
| **A: 保守渐进** ✅ 推荐 | 5 Phase 独立实施，逐 Phase 向后兼容 | Strategy/Adapter/Builder/Facade/Decorator/Memento | 15-20 文件，每 Phase 独立可合并 | Phase 4 cache 归属模糊；Plan 条件函数难序列化 |
| B: 统一接口 | Phase 1+4 统一 LoopStrategy抽象的，Phase 2+3 统一 Plan-as-Root | Strategy（全统一） | 25-30 文件，一次性大量改动 | 改动面过大，当前 chatLoop 难全塞入 | 
| C: 最小变更 | NodeStrategy 不改接口只加字段；Plan 只做薄序列化；chatLoop 不拆 | 最少侵入 | 5-8 文件 | 长期价值低，问题复发 |

### 推荐：方案 A（保守渐进）
**推荐理由**：5 Phase 独立，每 Phase 可合并/测试/回滚；sugar API 零改动；NodeStrategy 迁移桥保后向兼容；OTel 可选不污染 Noop。
**最大风险**：Phase 4 cache/store 归属 Loop 还是 Engine 需要明确；Plan Edge.Condition 函数序列化靠标签映射。

### O0: 历史经验参考
> 搜索范围: memory/ (7 条条目)

| 来源 | 相关经验 | 对本次的启示 |
|------|---------|------------|
| [strategy-pattern-workplan.md](../../memory/strategy-pattern-workplan.md) | Strategy + Adapter 组合是最小侵入方式；流程控制 != 执行策略 | NodeStrategy 清理时保持 controlRunner 不变，只改 strategyRunner 和 strategy |
| [graph-as-tools.md](../../memory/graph-as-tools.md) | InlineTool + WorkPlan 是最轻量子代理调度；ToTool 提供可选包装 | Phase 3 糖→工具应参考已有 example_Implement/05_graph_tools/ 示例 |
| [tracer-module.md](../../memory/tracer-module.md) | Tracer 接口 + NoopTracer 零开销；context 传播 span ID；Export 自动重置 | Phase 5 OTel 改造要保持 NoopTracer 零开销，simpleSpan.End() 做双发射 |
| [engine-v05-architecture.md](../../memory/engine-v05-architecture.md) | Engine 作为最大编排层持有 Agent + LLM | Phase 4 解耦时保持 Engine 作为外观，Loop 内部引用 agent/llm |
| [arch-v05-two-gateways.md](../../memory/arch-v05-two-gateways.md) | 双网关架构（api + tool） | 不直接相关，但工具注册机制会影响 Phase 3 |
| [cache-module-filecache.md](../../memory/cache-module-filecache.md) | CacheProvider 闭包注入避免循环依赖 | Phase 1 NodeStrategy 清理参考此模式 |
| [Provider 策略模式](../../memory/provider-strategy-pattern.md) | 三层策略体系的设计决策 | 验证了策略模式在 Seele 中的推广路径 |

## A: Action --------------------------------
> A0 委托: soft_eng_plan（内联） | A1 委托: Workflow 子代理 | A2 委托: test-suite

### A0: 执行调度
**评估结果**：启用（子目标数=5，关键路径长度=2）
**最大并行度**：4 个子代理（P1/P2/P4/P5 并行）
**关键路径**：P2 -> P3（2 个串行阶段）
**后端**：claude 子代理（Workflow 编排）

| 子代理 | Phase | 状态 | 产物 |
|--------|-------|------|------|
| #1 | P1 NodeStrategy 清理 | ✅ 完成 | [agent-logs/P1/output.md](./agent-logs/P1/output.md) |
| #2 | P2 Plan 声明式 | ✅ 完成 | [agent-logs/P2/output.md](./agent-logs/P2/output.md) |
| #3 | P3 糖->工具 | ✅ 完成（依赖 P2 后执行） | [agent-logs/P3/output.md](./agent-logs/P3/output.md) |
| #4 | P4 Loop 解耦 | ✅ 完成 | [agent-logs/P4/output.md](./agent-logs/P4/output.md) |
| #5 | P5 Tracer OTel | ✅ 完成 | [agent-logs/P5/output.md](./agent-logs/P5/output.md) |

### A1: 编码变更

> 详见 [finish-review.md](./finish-review.md)

**全量验证结果**：
- `go build ./...` — 0 errors, 0 warnings
- `go vet ./...` — 0 issues
- `go test -race -count=3 ./...` — ALL PASS, 0 races
- 6 个带测试包全部通过

#### Phase 1: NodeStrategy 接口清理
- **strategy.go**: NodeStrategy.Execute(ctx, ec) 去掉 input 参数，内置策略改用 ec.PrevOutput
- **runner.go**: strategyRunner 接管模板渲染（input 字段 + Run 中 renderTemplate）
- **cache_strategy.go**: CachedStrategy 适配新签名
- **sugar.go**: strategyRunner 构造加 input 字段
- **DeprecatedNodeStrategy**: 旧接口桥接

#### Phase 2: WorkPlan 声明式 Plan
- **plan.go**: 新增 Plan/PlanNodeSpec/PlanEdgeSpec/ConditionRegistry 类型
- **plan.go**: Plan.Add/Edge/ToPlan/LoadPlan 方法
- **graph.go**: Edge 增加 json tag
- **sugar.go**: 完全不动

#### Phase 3: WorkPlan 糖->Agent 工具
- **plan.go**: WorkPlanTool 增加 PlanRef 字段，ToTool 增强
- **agent/agent.go**: 新增 RegisterWorkPlanTool 方法

#### Phase 4: Engine chatLoop 解耦
- **loop.go**: 新增 Loop 接口 + ReActLoop 实现
- **engine.go**: Engine 持有 Loop，Chat/ChatStream 委托给 loop.Run
- 存量测试全部通过（不改测试代码）

#### Phase 5: Tracer OTel 化
- **tracer.go**: SimpleTracer 可选 OTel TracerProvider
- **go.mod**: 新增 go.opentelemetry.io/otel 依赖
- NoopTracer 零开销未受影响

## L: Learning ───────────────────────────────
> 委托: finish-review | 输出: [finish-review.md](./finish-review.md)

### 目标复核
| 子目标 | 验收标准 | 实际结果 | 达成？ | 偏差 |
|-------|---------|---------|-------|------|
| G1: NodeStrategy 清理 | Execute(ctx,ec) 无 input，模板渲染在 runner 层 | ✅ 完成 + DeprecatedNodeStrategy 桥接 | ✅ | 无 |
| G2: Plan 声明式 | Plan 可 JSON 序列化，sugar API 不变 | ✅ Plan/PlanNodeSpec/EdgeSpec 定义 + LoadPlan | ✅ | 无 |
| G3: 糖→Agent 工具 | ToTool 可注册到 Agent | ✅ RegisterWorkPlanTool + PlanRef | ✅ | 无 |
| G4: Engine 解耦 | Loop 接口 + ReActLoop | ✅ Loop/ReActLoop + 存量测试零改 | ✅ | 无 |
| G5: Tracer OTel | End() 发射 OTLP span | ✅ 低精度模式 + Export() 兼容 | ✅ | OTel span 树为平级（低精度妥协） |

### 方案实际效果 vs 预期
| 维度 | O 阶段预期 | 实际 | 差异分析 |
|------|----------|-----|---------|
| 耦合度 | 低 — 5 Phase 独立 | ✅ 每个 Phase 独立改动，不互相影响 | 无差异 |
| 内聚性 | 高 — 每 Phase 单一职责 | ✅ NodeStrategy/Plan/Loop/Tracer 各司其职 | 无差异 |
| 可测试性 | 高 | ✅ 6 包全 PASS，engine 存量测试不改 | 无差异 |
| 实现成本 | 中 | 实际约 700 行改动 | 子代理并行执行有效 |
| 改动面 | 15-20 文件 | 实际 15 个文件 | 无差异 |
| 可回滚性 | 高 | 每 Phase 独立 commit，可逐个回滚 | 无差异 |
| 风险命中 | Phase 4 cache 归属 | Engine 保留配置字段并传给 ReActLoop，未冲突 | 缓解措施有效 |

### 改进建议
- **流程**：子代理并行执行的 Workflow 脚本需要更严谨的变量命名检查，避免 `p3Out` 类拼写错误导致失败
- **架构**：Tracer OTel 的低精度模式（平级 span）是已知妥协，后续可在 StartSpan 时创建 OTel span 以获得完整树结构
- **测试**：建议增加跨 Phase 的集成测试（如 Plan→LoadPlan→ToTool→RegisterWorkPlanTool→Agent.Chat 全链路）
