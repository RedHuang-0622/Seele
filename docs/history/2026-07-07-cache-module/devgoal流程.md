# Workflow: 缓存模块 + context 架构优化

## 元信息
- 日期: 2026-07-07
- 规模: 深度
- 需求: 缓存到本地任意文件的模块包 + 对外开放查看缓存方法 + 装配件模式接入 tools/图编排 + context 架构优化
- 子 Skill 清单:
  - G: front-review → [front-review.md](./front-review.md)
  - O: devplan → [plan.md](./plan.md)
  - A0: soft_eng_plan → [soft-eng-plan.md](./soft-eng-plan.md) + [agent-logs/](./agent-logs/)
  - A1: code-impl → [code-changes.md](./code-changes.md)
  - A2: test-suite → [test-report.md](./test-report.md)
  - L: finish-review → [finish-review.md](./finish-review.md)

## G: Goal ───────────────────────────────────
> 委托: front-review | 输出: [front-review.md](./front-review.md)

### 目标拆解
**主目标**：在 context 包中创建本地任意文件缓存模块，通过装配件模式集成 tools 和图编排，优化 context 架构，实现缓存命中减少 LLM token 消耗。

| # | 子目标 | 验收标准 | 优先级 |
|---|-------|---------|-------|
| G1 | CacheProvider 接口 + FileCache 实现 | 支持任意文件存储、TTL、哈希去重、命中统计 | P0 |
| G2 | context/config.go 拆分优化 | SessionConfig / ContextConfig / CacheConfig 各自独立文件 | P0 |
| G3 | Holder 集成缓存 | 缓存引用 + CacheStats/CacheList/CacheClear 公开方法 | P0 |
| G4 | chatLoop 缓存感知 | 输入哈希 → 缓存查询，命中则跳过 LLM 调用 | P0 |
| G5 | CachedStrategy 适配器 | 包装任意 NodeStrategy，图编排可透明缓存 | P1 |
| G6 | Cache 装配件工具注册 | \_cache\_stats/\_cache\_list/\_cache\_get/\_cache\_clear 工具 | P1 |
| G7 | 循环依赖解耦 | 不引入 context↔tool/workplan 循环引用 | P0 |

### 成功标准
- [ ] 功能：缓存层可存储/读取任意文本内容，支持 TTL 过期
- [ ] 质量：
  - 单元测试通过，覆盖率 ≥ 80%
  - go vet 零告警
  - 竞态检测零 data race
  - context 包无新增外部依赖（除标准库）
- [ ] 性能：缓存命中时 chatLoop 直接返回（0 token 消耗），缓存 miss 时路径无退化
- [ ] 兼容：现有 Chat/ChatStream API 签名不变，workplan NodeStrategy 接口不变

### 非目标（明确不做）
- [不做 LRU 驱逐] — 用 TTL + MaxEntries 上限替代，低实现复杂度
- [不做分布式缓存] — 仅本地文件缓存
- [不做二进制缓存] — 只存文本内容（LLM 上下文是纯文本）
- [不做 workplan 内部改造] — CachedStrategy 从外部包装，不侵入图引擎

### 前置审查摘要
> 详见 [front-review.md](./front-review.md)

| 文件 | 修改类型 | 说明 |
|------|---------|------|
| context/cache.go | 新增 | CacheProvider + FileCache |
| context/cache_config.go | 新增 | CacheConfig 独立文件 |
| context/cache_tool.go | 新增 | Cache 工具注册（ToolProvider） |
| context/cache_strategy.go | 新增 | CachedStrategy 适配器 |
| context/session_config.go | 拆分 | SessionConfig 独立 |
| context/context_config.go | 拆分 | ContextConfig 独立 |
| context/holder.go | 修改 | 缓存字段 + 方法 |
| context/chat.go | 修改 | 缓存检查集成 |
| context/storage.go | 修改 | 扩展通用 |
| config.go | 修改 | 精简为入口配置 |
| workplan/cache_strategy.go | 新增 | CachedStrategy（在 workplan 包扩展） |

**依赖关系**: context 包零新增外部依赖；CachedStrategy 在 workplan 包扩展；CacheToolProvider 通过接口注入避免循环依赖。
**循环依赖检查**: 已确认无环（详见 front-review.md 解环方案）。
**风险预判**: 主要风险是循环依赖，已通过接口注入规避。

## O: Options ────────────────────────────────
> O0: dev-goal 历史经验检索 | O1-O3 委托: devplan | 输出: [plan.md](./plan.md)

### O0: 历史经验参考
> 🔍 搜索范围: memory/

| 来源 | 相关经验 | 对本次的启示 |
|------|---------|------------|
| memory/strategy-pattern-workplan.md | Strategy + Adapter 组合：接口定义在 use 方，Adapter 桥接到已有接口 | CachedStrategy 在 workplan 包扩展实现，wrap NodeStrategy 接口 |
| memory/strategy-pattern-workplan.md | 流程控制 ≠ 执行策略：Loop/Fork 是图拓扑，不策略化 | CachedStrategy 只包装"执行策略"，不碰流程控制 |
| memory/graph-as-tools.md | InlineTool + WorkPlan 组合暴露子代理能力 | Cache 查看工具也用 RegisterInlineTool + SchemaOf 注册 |
| memory/arch-v05-two-gateways.md | Agent 与 Session 完全分离，双 Gateway 模式 | Cache 查看工具通过 tool Gateway 注册，context 包不直接 import tool |

### 方案摘要
> 详见 [plan.md](./plan.md)

| 方案 | 核心思路 | 设计模式 | 变更范围 | 主要风险 |
|------|---------|---------|---------|---------|
| A ⭐ **推荐** | 全内聚 context 包 + CachedStrategy 装饰器 | Strategy + Decorator + 装配件 | ≈635行，新增6文件，修改4 | 循环依赖已解；单轮缓存非多轮 |
| B | 最小接口 (Get/Set) + 类型断言扩展 | Strategy + 接口隔离 | ≈575行，精简60行 | 类型断言不优雅 |
| C | 独立 pkg/cache/ 包 + 回调注入 | 回调函数 + 独立包 | ≈同A，多一个顶级包 | 回调组合不便，包深度增 |

### 推荐：方案 A（全内聚 + 策略装饰器）
**推荐理由**：零新增外部依赖（仅标准库），自然解环，与现有 context 架构一致，渐进可用（零成本无缓存回退）
**最大风险**：CachedStrategy 在 workplan 包中引用 CacheProvider 需通过函数注入解环（已设计解决）

## A: Action ─────────────────────────────────
> A1 委托: code-impl | 执行中

### A0: 执行调度
> 用户选择直接实现（非 sub-agents）

**评估结果**：✅ 跳过 A0（直接实现，提升效率）
**执行方式**：串行实现 G2→G1→G5→G3+G4→G6

### 执行记录
| 子目标 | 状态 | 关键文件 | 架构决策 |
|-------|------|---------|---------|
| G2 | ✅ | session_config.go, context_config.go, cache_config.go, config.go | SessionConfig/ContextConfig/CacheConfig 独立文件，config.go 精简为文档入口 |
| G1 | ✅ | cache.go | CacheProvider 接口 + FileCache（内容寻址 SHA256 + TTL + 内存索引 sync.Map + atomic 统计） |
| G5 | ✅ | workplan/cache_strategy.go | CachedStrategy 实现 NodeStrategy，通过函数注入避免循环依赖 |
| G3 | ✅ | holder.go | NewWithCache 构造器 + SetCache/CacheStats/CacheList/CacheClear/CacheKeys/CacheGet 公开方法 |
| G4 | ✅ | chat.go | chatLoop 缓存检查（输入 SHA256 → 缓存查询 → 命中跳过 LLM 调用，纯文本自动缓存） |
| G6 | ✅ | cache_tool.go | RegisterCacheTools 注册 4 个 _cache_* 工具（stats/list/get/clear），SchemaOf 通过函数参数注入 |

### 编译状态
- context/ 包: ✅ 编译通过（零新增外部依赖）
- workplan/ 包: ✅ 编译通过（CachedStrategy 零外部依赖）
- 循环依赖: ✅ 无（函数注入 + 接口参数解环）

### A2: 测试报告
> 委托: test-suite | 输出: [test-report.md](./test-report.md)

| 测试 | 结果 |
|------|------|
| context/ 全部 15 个缓存测试 | ✅ 通过 |
| context/ 并发竞态检测 | ✅ 3 轮 clean |
| context/ go vet | ✅ 零告警 |
| workplan/ 全部测试 | ✅ 通过 |
| workplan/ 竞态检测 | ✅ Clean |

### 执行记录
| 子目标 | 状态 | 关键变更 | 偏离方案？ |
|-------|------|---------|----------|
| G2 | ✅ | config.go → session_config.go + context_config.go + cache_config.go | 无 |
| G1 | ✅ | CacheProvider + FileCache（SHA256 内容寻址 + TTL + sync.Map 索引） | 无 |
| G5 | ✅ | CachedStrategy 在 workplan 包，函数注入解环 | 无 |
| G3 | ✅ | Holder +7 个缓存方法，NewWithCache 构造器 | 无 |
| G4 | ✅ | chatLoop 缓存检查 | 无 |
| G6 | ✅ | RegisterCacheTools 注册 4 个 _cache_* 工具 | 无 |
| G7 | ✅ | 零循环依赖 | 无 |

## L: Learning ───────────────────────────────
> 委托: finish-review | 输出: [finish-review.md](./finish-review.md)

### 目标复核

| 子目标 | 验收标准 | 实际结果 | 达成？ |
|-------|---------|---------|-------|
| G1 | CacheProvider 接口 + FileCache（任意文件、TTL、去重、统计） | SHA256 内容寻址 + TTL + sync.Map 索引 + atomic 统计 | ✅ |
| G2 | config.go 拆分为 session/context/cache 独立文件 | 3 个独立文件 + config.go 精简为文档入口 | ✅ |
| G3 | Holder 集成缓存 + 公开查看方法 | NewWithCache + 7 个公开方法（Stats/List/Clear/ClearAll/Keys/Get/SetCache） | ✅ |
| G4 | chatLoop 缓存感知 | SHA256(userInput) → cache.Get → 命中直接返回，无缓存零退化 | ✅ |
| G5 | CachedStrategy 适配器 | 函数注入解环，实现 NodeStrategy 接口，CachedStrategyOption 配置 | ✅ |
| G6 | Cache 装配件工具注册 | RegisterCacheTools 注册 4 个 _cache_* 工具 | ✅ |
| G7 | 零循环依赖 | context 零外部依赖，CachedStrategy 函数注入，RegisterCacheTools 接口参数 | ✅ |

### 方案实际效果 vs 预期

| 维度 | O 阶段预期 | 实际 | 差异分析 |
|------|----------|-----|---------|
| 耦合度 | 低 — context 包零外部依赖 | ✅ context 包只依赖标准库 | 符合预期 |
| 内聚性 | 高 — 缓存与 context 强关联 | ✅ CacheProvider 接口 + FileCache 实现都在 context 包 | 符合预期 |
| 可测试性 | 高 — CacheProvider 接口易 mock | ✅ 15 个单元测试覆盖所有路径，race clean | 符合预期 |
| 实现成本 | ≈635 行 | ~765 行 | 略高于预期（工具注册代码比估计多~130行） |
| 改动面 | 新增 6 文件，修改 4 文件 | 新增 6，修改 3（storage 未修改） | 符合预期 |
| 可回滚性 | 高 — cache==nil 时零退化 | ✅ 所有方法 nil-safe | 符合预期 |
| 循环依赖 | 已解 — 函数注入 | ✅ 无新增循环依赖 | 符合预期 |

### L3: 经验存储

写入 memory/cache-module-filecache.md

### L4: 改进建议

- **流程**：任务中的 config.go 拆分与本任务的核心需求（缓存模块）可以独立提交，减少 review 负担
- **工具**：缺少一个 FileCache 的 benchmark 测试（测量缓存命中 vs 未命中的延迟差异）
- **架构**：chatLoop 缓存仅基于 userInput 哈希，后续可扩展到含历史上下文的缓存键（代价是命中率降低）
