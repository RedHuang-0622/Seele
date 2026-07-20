# Effort 等级设计文档

> **状态**: 设计稿（未实现）
> **日期**: 2026-07-11  
> **领域**: 引擎层 / ReAct 循环 / WorkPlan 编排

---

## 1. 背景

当前 Seelex 的回复流程只有一种模式：完整 ReAct 循环（MaxLoops=25）。  
缺少按需调节**响应速度 / 工具深度 / Token 消耗**的能力。

引入 **Effort 等级**（Lite / Medium / Max / Ultra），让用户可以在不同场景下选择合适档位：

| 场景 | 推荐 Effort |
|------|-------------|
| 闲聊、快速问答 | Lite |
| 日常编程辅助 | Medium |
| 复杂多步推理 | Max |
| 大规模 WorkPlan 编排 | Ultra |

---

## 2. 当前 Baseline 架构

现状回复流程：

```
User Input
    │
    ▼
Engine.ChatStream()
    │
    ▼
ReActLoop.Run()
    │
    ├─ 1. 追加 user message 到 history
    ├─ 2. 检查 history token → 触发 CompressHistory
    │
    └─ 3. ReAct 循环（MaxLoops=25）：
         ├─ GetVisibleTools()
         ├─ callLLM(history + tools)
         ├─ 追加 assistant message
         ├─ 无 tool_calls → return
         └─ 有 tool_calls → DispatchTool() → 追加结果 → 继续
```

核心可调参数（`SessionConfig`）：

| 参数 | 默认值 | 位置 |
|------|--------|------|
| `MaxLoops` | 25 | `engine/config.go` |
| `MaxToolResultChars` | 4000 | `engine/config.go` |
| 压缩阈值 | ~6144 tokens | `engine/loop.go CompressHistory` |
| VisibleTools | 全部可用 | `engine/context.go` |

---

## 3. 四个 Effort 等级详细设计

### 3.1 Lite — 极简直通

**目标**: 最快响应，最小成本，零工具调用。

```
User Input
    │
    ▼
Engine.ChatStream(effort=Lite)
    │
    ├─ 跳过 CompressHistory
    ├─ History 窗口裁剪为最近 4 条
    ├─ MaxLoops = 0
    ├─ VisibleTools = []（不传给 LLM）
    │
    └─ 单次 callLLM() → onChunk 流式返回
```

**参数配置**:

| 参数 | 值 | 说明 |
|------|----|------|
| MaxLoops | 0 | 禁止工具调用 |
| MaxToolResultChars | — | 不适用 |
| 压缩阈值 | 不压缩 | — |
| History 窗口 | 最近 4 条 | 之前历史丢弃 |
| VisibleTools | 空列表 | 不参与 tool call |
| Plugin 切换 | 禁止 | — |
| Tracer | 关闭 | — |
| ResponseCache | 不读写 | — |

**架构影响**:
- `ReActLoop` 退化为 `callLLM(ctx, nil, onChunk)`
- 跳过 `CompressHistory`、`GetVisibleTools`、`DispatchTool` 全部步骤
- **预计延迟**: ~1-3s
- **预计 Token**: ~500-2K

---

### 3.2 Medium — 标准 ReAct

**目标**: 平衡速度与工具能力，有限工具调用。

```
User Input
    │
    ▼
Engine.ChatStream(effort=Medium)
    │
    ├─ 追加 user message
    ├─ 条件压缩（token > 8192 触发，宽松阈值）
    │
    └─ 受限 ReAct 循环（MaxLoops=8）：
         ├─ VisibleTools = default plugin 工具
         ├─ callLLM(history + tools)
         ├─ 无 tool_calls → return
         └─ 有 tool_calls → DispatchTool → 追加结果 → 继续
              └─ MaxToolResultChars = 2000
```

**参数配置**:

| 参数 | 值 | 说明 |
|------|----|------|
| MaxLoops | 8 | 有限轮次 |
| MaxToolResultChars | 2000 | 结果截断更短 |
| 压缩阈值 | ~8192 tokens | 减少压缩频率 |
| History 窗口 | 最近 20 条 | — |
| VisibleTools | default plugin 仅读工具 | 不含 switch_mode |
| Plugin 切换 | 禁止 | — |
| Tracer | 轻量（仅记录轮次/耗时） | — |
| ResponseCache | 不读写 | — |

**相比 Lite 新增**:
- ReAct 循环（最多 8 轮）
- 上下文压缩（高阈值）
- 有限工具调用（仅读，不切换 Plugin）
- 轻量 Tracer

**相比 Max 削减**:
- MaxLoops: 25 → 8
- MaxToolResultChars: 4000 → 2000
- 不可切换 Plugin
- 压缩阈值更宽松
- 无 ResponseCache

**预计延迟**: ~5-15s  
**预计 Token**: ~2K-10K

---

### 3.3 Max — 完整 ReAct + Plugin 切换

**目标**: 最大化工具使用能力，支持 Plugin 切换。

```
User Input
    │
    ▼
Engine.ChatStream(effort=Max)
    │
    ├─ 追加 user message
    ├─ 条件压缩（token > 6144 触发，默认阈值）
    │
    └─ 完整 ReAct 循环（MaxLoops=25）：
         ├─ VisibleTools（支持 switch_mode 切换 Plugin）
         │    └─ 每次 switch 后 tools 集合变化
         ├─ callLLM(history + tools)
         ├─ 无 tool_calls → return
         └─ 有 tool_calls → DispatchTool → 追加结果 → 继续
              └─ MaxToolResultChars = 4000
    │
    └─ 后处理：
         ├─ ExportTrace() → 完整 trace 树
         └─ ResponseCache 写入缓存
```

**参数配置**:

| 参数 | 值 | 说明 |
|------|----|------|
| MaxLoops | 25 | 默认最大值 |
| MaxToolResultChars | 4000 | 默认长度 |
| 压缩阈值 | ~6144 tokens | 默认阈值 |
| History 窗口 | 全部 history | — |
| VisibleTools | 全部工具（含 switch_mode） | — |
| Plugin 切换 | ✅ | default ↔ read ↔ write ↔ git ↔ shell ↔ plan |
| Tracer | 完整 span 树 | 每轮 + 每个 dispatch |
| ResponseCache | 写入 | 缓存最终响应 |

**相比 Medium 新增**:
- Plugin 切换（`switch_mode` 工具可用）
- MaxLoops: 8 → 25
- MaxToolResultChars: 2000 → 4000
- 完整 Tracer span 树
- ResponseCache 写入
- 默认压缩阈值（更积极压缩）

**预计延迟**: ~15-60s  
**预计 Token**: ~10K-50K

---

### 3.4 Ultra — WorkPlan DAG 编排

**目标**: 最深度的多步推理，WorkPlan DAG 执行，Checkpoint 断点续跑。

```
User Input
    │
    ▼
Engine.ChatStream(effort=Ultra)
    │
    ├─ 追加 user message
    ├─ 条件压缩（token > 4096 触发，最积极压缩）
    ├─ 快照管理（snapshot 保存中间状态）
    │
    ├─ WorkPlan 检测：
    │    ├─ 输入包含 plan 关键词 / 显式触发？
    │    │    └─ YES → plan_load 构建 DAG → plan_run 执行
    │    └─ NO  → 降级为 Max 模式但扩大 Loops
    │
    ├─ 扩展 ReAct 循环（MaxLoops=50）：
    │    ├─ Plan 模式：
    │    │    ├─ plan_run 执行 DAG 节点
    │    │    ├─ 支持 fork（并行）、loop（循环）、switch（分支）、approve（审批）
    │    │    └─ checkpoint 断点续跑
    │    │
    │    └─ 非 Plan 模式：
    │         └─ 同 Max 但 MaxLoops=50，MaxToolResultChars=8000
    │
    └─ 后处理：
         ├─ 完整 trace 树 + checkpoint 状态
         ├─ ResponseCache 读写
         ├─ MCP stack 持久化快照
         └─ Skill 系统注册/调用
```

**参数配置**:

| 参数 | 值 | 说明 |
|------|----|------|
| MaxLoops | 50 | 最大扩展 |
| MaxToolResultChars | 8000 | 最长结果 |
| 压缩阈值 | ~4096 tokens | 最积极压缩 |
| History 窗口 | 全部 + snapshot | 可回退 |
| VisibleTools | 全部（含 switch_mode） | — |
| Plugin 切换 | ✅ | — |
| Tracer | 完整 + checkpoint | 每个节点状态可恢复 |
| ResponseCache | 读写 | — |
| WorkPlan | ✅ plan_load / plan_run | DAG 图编排 |
| MCP Snapshot | ✅ | 持久化快照 |
| Skill 系统 | ✅ | 完整加载 |

**相比 Max 新增**:
- WorkPlan DAG 图编排
- 节点级执行（LLMNode / AgentNode / FunctionNode）
- Sugar 原语：fork、loop、switch、approve
- Checkpoint 断点续跑
- MaxLoops: 25 → 50
- MaxToolResultChars: 4000 → 8000
- 最积极压缩策略
- MCP stack 快照持久化
- Skill 系统完整加载

**预计延迟**: ~30s-5min  
**预计 Token**: ~50K-200K+

---

## 4. Effort 等级对比总表

| 维度 | Lite | Medium | Max | Ultra |
|------|------|--------|-----|-------|
| **MaxLoops** | 0 | 8 | 25 | 50 |
| **MaxToolResultChars** | — | 2000 | 4000 | 8000 |
| **压缩阈值 (tokens)** | 不压缩 | ~8192 | ~6144 | ~4096 |
| **History 窗口** | 最近 4 条 | 最近 20 条 | 全部 | 全部 + snapshot |
| **工具调用** | ❌ | ✅ default | ✅ 全 plugin | ✅ 全 plugin |
| **Plugin 切换** | ❌ | ❌ | ✅ | ✅ |
| **WorkPlan DAG** | ❌ | ❌ | ❌ | ✅ |
| **Tracer** | ❌ | 轻量 | 完整 span | 完整 + checkpoint |
| **ResponseCache** | ❌ | ❌ | 写入 | 读写 |
| **MCP Snapshot** | ❌ | ❌ | ❌ | ✅ |
| **Skill 系统** | ❌ | ❌ | ❌ | ✅ |
| **预计延迟** | ~1-3s | ~5-15s | ~15-60s | ~30s-5min |
| **预计 Token** | ~500-2K | ~2K-10K | ~10K-50K | ~50K-200K+ |

---

## 5. 实现方案

### 5.1 核心改动点

#### (1) SessionConfig 增加 Effort 字段

**文件**: `engine/config.go`

```go
type EffortLevel int

const (
    EffortLite   EffortLevel = iota // 0
    EffortMedium                    // 1
    EffortMax                       // 2
    EffortUltra                     // 3
)

type SessionConfig struct {
    // ... 现有字段 ...

    Effort EffortLevel `json:"effort"` // 新增

    // 派生字段（运行时根据 Effort 计算）
    maxLoops          int
    maxToolResultChars int
    compressThreshold int
}
```

#### (2) 新增 Effort 参数绑定

```go
func (c *SessionConfig) BindEffort(e EffortLevel) {
    switch e {
    case EffortLite:
        c.maxLoops = 0
        c.maxToolResultChars = 0
        c.compressThreshold = math.MaxInt // 不压缩
    case EffortMedium:
        c.maxLoops = 8
        c.maxToolResultChars = 2000
        c.compressThreshold = 8192
    case EffortMax:
        c.maxLoops = 25
        c.maxToolResultChars = 4000
        c.compressThreshold = 6144
    case EffortUltra:
        c.maxLoops = 50
        c.maxToolResultChars = 8000
        c.compressThreshold = 4096
    }
}
```

#### (3) ReActLoop.Run() 增加 Effort 分支

**文件**: `engine/loop.go`

```go
func (l *ReActLoop) Run(ctx context.Context, effort EffortLevel) (*ReActResult, error) {
    switch effort {
    case EffortLite:
        return l.runLite(ctx)    // 直接 callLLM，无循环
    case EffortMedium:
        return l.runStandard(ctx) // 有限循环，仅 default tools
    case EffortMax:
        return l.runFull(ctx)    // 完整循环 + Plugin 切换
    case EffortUltra:
        return l.runUltra(ctx)   // 完整循环 + WorkPlan + Checkpoint
    }
}
```

#### (4) Engine.ChatStream() 暴露 Effort 参数

**文件**: `engine/engine.go`

```go
func (e *Engine) ChatStream(
    ctx context.Context,
    req ChatRequest,
    effort EffortLevel,
    onChunk func(Chunk),
) (*ChatResult, error)
```

#### (5) TUI 层暴露 Effort 切换

**文件**: `seelex/tui/xxx.go`

```
快捷键: Ctrl+E / Ctrl+Shift+E 循环切换
状态栏显示: [Effort: Max] 或 [E:Max]
```

### 5.2 各等级代码差异

```
┌────────────────────────────────────────────────────────┐
│ Lite     → engine/loop.go:    runLite()                │
│             跳过循环, 直接 callLLM                      │
├────────────────────────────────────────────────────────┤
│ Medium   → engine/loop.go:    runStandard()             │
│             loops=8, default tools only                 │
├────────────────────────────────────────────────────────┤
│ Max      → engine/loop.go:    runFull() [现有逻辑不变]  │
│             loops=25, 全工具 + switch_mode              │
├────────────────────────────────────────────────────────┤
│ Ultra    → engine/loop.go:    runUltra()                │
│             loops=50, + plan_load/run, + checkpoint     │
│             + mcp_snapshot, + skill                     │
└────────────────────────────────────────────────────────┘
```

---

## 6. 风险与注意事项

1. **Lite 模式下 `MaxLoops=0`**：需要确保 `loop.go` 不会因空工具列表崩溃
2. **Medium 的 Plugin 限制**：`switch_mode` 不应在 Medium 下暴露给 LLM
3. **Ultra 的 Checkpoint**：需要引入持久化层，序列化/反序列化 ReActLoop 状态
4. **压缩阈值差异**：不同 Effort 使用不同阈值，需确保 `CompressHistory` 函数接受动态参数
5. **History 窗口裁剪**：Lite 需要主动裁剪 history slice，避免 token 膨胀
6. **向后兼容**：默认 Effort = Max，现有行为不变

---

## 7. 未来扩展

- **Auto Effort**: 根据用户输入复杂度自动选择 Effort 等级
- **Per-Query Effort**: 单次会话内不同 query 可用不同 Effort
- **Effort Budget**: 为会话设定 Token 预算，自动降级 Effort
- **Effort Profile**: 用户可自定义各维度的数值（类似预设配置）
