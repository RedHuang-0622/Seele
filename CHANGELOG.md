# Seele Release Notes

---

## v0.1.0 (2026-06-06) — 首个里程碑版本

> **主题：架构定型 + 资源池升级**

经过 35 次迭代，Seele 框架的核心架构已稳定。本版本完成最后一批并发安全修复，并将底层资源池依赖升级至 `RedHuang-0622/TemplatePoolByGO v0.1.8`。

### 🏗️ 核心架构

| 层 | 包 | 职责 |
|----|-----|------|
| 编排 | `core/agent/` | Agent 生命周期、LLM+工具组装 |
| 会话 | `core/session/` | ReAct 循环、上下文管理、审批流转 |
| 工具 | `core/tool_holder/` | 多 Provider 聚合、瞬时重试 |
| Provider | `provider/` | HubProvider (gRPC)、MCPProvider (stdio/sse) |
| LLM | `llm/` | OpenAI 兼容 HTTP 客户端（stdlib） |
| 工作流 | `workplan/` | 声明式 DAG 引擎（9 种原语） |
| 部署 | `sdk/cluster/` | 多 Agent gRPC 服务化 |

### ✨ 核心能力

- **ReAct Agent**：Chat / ChatStream，支持多轮对话 + tool_call 并发
- **工具生态**：microHub (gRPC) + MCP (stdio/sse) 双 Provider，运行时热插拔
- **WorkPlan 工作流**：Auto / Approve / If / Switch / Loop / Fork / Checkpoint / Emit，声明式 DSL
- **人工审批**：Q-K-V 两段式协议，CLI / 网络 / 自动三种 Gate 实现
- **上下文管理**：LLM 压缩 + 硬截断 + Token 估算，防止上下文溢出
- **REPL**：交互式终端，支持审批 UI、Prompt 热加载 (fsnotify)
- **流式输出**：SSE 分帧解析，tool_call 思考文本实时推送
- **多层并发**：tool_call 并发 (max 5)、Fork Agent 并发 (max 3)

### 🔧 本次修复 (f8972a4)

| 问题 | 修复 |
|------|------|
| `MCP()` 无锁并发 → nil dereference | 新增 `mcpMu` + `shutdown` channel |
| health probe goroutine 不可停止 | 新增 `healthCancel`，Shutdown 时 cancel |
| `buildToolCalls` 零值 ToolCall 注入 history | 改用 append + index check |
| `parseApprovalQuestionID` 字符串解析 JSON | 改用 `json.Unmarshal` |
| `ChatStream` tool_call 时 onChunk 丢弃 | 现在推送思考文本到 onChunk |

### 📦 依赖升级

```
github.com/RedHuang-0622/microHub     v0.1.4 → github.com/RedHuang-0622/microHub     v0.1.5
github.com/RedHuang-0622/TemplatePoolByGO v0.1.7 → github.com/RedHuang-0622/TemplatePoolByGO v0.1.8
```

**TemplatePoolByGO v0.1.8 关键变更：**

| 变更 | 影响 |
|------|------|
| `ReconnectOnGet` 默认 `true` → `false` | 热路径不再默认 Ping+重连，需显式开启 |
| `MonitorInterval` 实际生效 | 新增定期扩容/缩容 goroutine |
| `bufferSize < 1` 防死锁 | `IdleBufferFactor=0` 时强制设为 1 |
| `Get()` 竞态修复 | Enqueue→Remove 窗口内资源不再丢失 |
| `expand` 重试参数化 | `MaxRetries`/`RetryInterval` 配置生效 |
| `shrink` 两阶段驱逐 | 优先关闭超龄连接 (SurviveTime) |
| `validateAndReturn` 实现 Ping+重连 | `ReconnectOnGet=true` 时真正生效 |

### ⚠️ 已知问题

3 个 🔴 严重 Bug（详见 [review.md](review.md)）：
- MCP `Attach()` 失败时 stdio 子进程泄漏
- `HubProvider.HasTool()` 对 `_` 前缀工具返回 false（影响 REPL 审批恢复）
- `Chat()` `*msg.Content` nil panic

### 📁 文件统计

| 组件 | 文件 | 代码行 |
|------|------|--------|
| `core/agent/` | 3 | ~350 |
| `core/session/` | 4 | ~530 |
| `core/tool_holder/` | 3 | ~140 |
| `provider/` | 4 | ~750 |
| `llm/` | 1 | ~370 |
| `history/` | 2 | ~360 |
| `workplan/` | 6 | ~2000 |
| `sdk/` | 5 | ~755 |
| `types/` `config/` | 3 | ~175 |
| **总计** | **~31** | **~5400** |

### 🔗 相关文档

- [架构文档](ARCHITECTURE.md)
- [代码审查](review.md)
- [使用指南](README.md)

---

> 首个 release 之后的版本记录将以此格式更新。
