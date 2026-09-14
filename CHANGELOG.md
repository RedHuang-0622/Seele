# Seele Release Notes

---

## v0.2.0 (2026-09-14) — 多模态附件与限流装配层

> **主题：`limits` 准入装配层 + `types` 附件载体（`FilePart` / `FileKind` / `FileID`）+ 非 2xx 可分类错误**

### 🏗️ 01 — `limits`：限流与并发的路数装配层

把「现在允不允许发、发了算多少额度」从 `agent/core/api` 里已标 Deprecated 的 `Account.MaxRPM` 抽出来，做成可装饰任意 `types.ChatCompleter` 的独立层。与 `accountpool` 的分工保持清晰：账号池回答「用哪个账号」，`limits` 回答「现在允不允许发、发了算多少额度」。参数、装配与边界语义见 [`limits/README.md`](limits/README.md)。

- `Params` / `RetryPolicy` / `DefaultParams()`：YAML/JSON tag 齐全的参数模型（RPM、TPM、加权在途、排队超时、重试）；`0 = 不限`，`Normalize()` 只补无法表达的派生值（桶容量），推荐值只在 `DefaultParams()` 里给。
- `Assembly` 与 `Wrap` / `WrapFor` / `WrapAll` / `WrapComplete`：按 key（账号、角色、节点）装配 gate；`WrapComplete` 接受只保证 `Complete` 的窄接口（如 seelex 的 `agent.Completer`），具体实现具备流式能力时仍走真流式。
- `Gate` / `Permit` / `Stats`：准入顺序为「加权在途槽位 → 请求速率桶 → 令牌速率桶」，三者共享同一 deadline；先占槽位再扣桶，速率等待失败退还槽位；`Release()` 幂等，`Settle(usage)` 用真实用量结算估算差额。
- 失败一律返回可 `errors.Is` 匹配的哨兵错误（`ErrConcurrencyExceeded`、`ErrRateLimited`，以及包裹两者的 `ErrQueueTimeout`），语义都是「稍后可以重试」。
- `SetParams` / `SetRate` / `SetConcurrency` / `SetBurst` / `SetKeyParams` / `SetEnabled` / `Snapshot`：参数全运行时可改，不打断在途请求；`SetParams` 是全局覆盖。
- `Cost` / `CostEstimator` / `DefaultEstimator`：图片按 512×512 tile 折算 token（尺寸未知走兜底值，绝不返回 0），`Cost.Images` 只计加权在途权重。

### 🏗️ 02 — `types`：消息级多模态附件

- `ImagePart` → `FilePart`，新增 `FileKind`（`image` / `document`）与 `FilePart.FileID`；`Message.Images` → `Message.Files`、`WithImages` → `WithFiles`。
- 纯文本消息的 JSON 形状逐字节不变（`"content":"…"`），只有携带附件才展开为 content parts 数组：OpenAI 形态走 `image_url` 或 `file.file_data`，Anthropic 策略按 kind 走 `image` 或 `document`。
- Files API 引用按端点实测走**扁平** `{"type":"file","file_id":"file-api-..."}`（与内联载荷互斥）；回读时保留 `file_id`——旧行为直接丢弃，重发历史会静默少一张附件。
- 反解新增 OpenAI `file`、Responses `input_file`（含顶层 `file_data` / `file_url`）与 Anthropic `document`；`Validate()` 在请求前拦下「种类与 MIME 打架」「既无字节又无地址」「内联与 file_id 同时存在」。

### ✨ 03 — 非 2xx 成为可分类错误

- 新增 `types.HTTPStatusError`（含 `Retry-After` 解析；错误文本仍是历史形状 `HTTP %d: …`，日志与既有测试不受影响）。
- `agent/core/api` 的同步与流式两处不再把非 2xx 交给 `ParseResponse`（那会把 429 当成协议解析失败），重试层因此能拿到状态码与 `Retry-After`。
- 新增可单测的重试分类：`StatusOf` / `RetryAfterOf` / `IsRetryable` / `RetryDelay`，默认尊重 provider 的 `Retry-After`。

### 💥 破坏性变更

| 旧 | 新 |
| --- | --- |
| `types.ImagePart` | `types.FilePart`（新增 `Kind`、`FileID`） |
| `types.Message.Images` | `types.Message.Files` |
| `Message.WithImages` | `Message.WithFiles` |

### ⚠️ 已知缺口

- 文档 / PDF 附件只有单测覆盖：可用账号均为 openai 兼容端点，尚无 Anthropic / Gemini / 官方 OpenAI 账号做真机验证。
- 各家的数值型限额（PDF 页数与字节、图片像素上限）与 MIME 白名单尚未编码。
- 流式按估算计费（Seele 现有 SSE 处理不暴露 usage）；需要精确计费的产品应改用 `Gate.Acquire` 自行准入与 `Permit.Settle`。
- Anthropic 侧引用 `file_id` 需 `anthropic-beta: files-api-2025-04-14` 头，该策略暂未发送，故该分支跳过而不是发空 source。
- `limits` 目前是可选层，尚未接入产品装配点。

### 📊 变更统计

34 文件，+5860 / −13；新增 `limits` 包（18 个文件）。验证：`go build ./...`、`go vet ./...` 通过，`go test ./limits/... ./types/... ./agent/core/api/... -count=1`（含 `-race`）全绿。

---

## v0.3.0 (2026-06-14) — 架构重构

> **主题：策略模式工具层 + WorkPlan 图引擎 + SchemaOf**

### 🏗️ 01 — 工具层策略模式重构

ToolProvider 接口从 4 方法精简为 1 方法，引入 Handler 策略接口：

```
旧: ToolProvider{Tools, Dispatch, HasTool, ProviderName}
新: ToolProvider{Tools} → ToolEntry{Definition + ToolHandler}
```

- 新增 `ToolHandler` 策略接口（1 方法 `Execute`）
- 三种实现：`HubToolHandler`（gRPC）、`MCPToolHandler`（stdio/SSE）、`InlineToolHandler`（Go 函数）
- 新增 `InlineProvider` — 纯 Go 函数工具管理（零网络开销）
- `tool_holder` 改用 `map[string]ToolEntry` O(1) 分发，替代 O(n) 遍历
- `_` 前缀工具隔离下沉到 `tool_holder.Tools()` 统一处理

新增文件：`hub_handler.go`、`mcp_handler.go`、`inline_handler.go`、`inline_provider.go`

### 🏗️ 02 — WorkPlan 图引擎抽象

线性链表 → Graph + Edge + NodeRunner 图引擎：

- 新增 `Graph`、`Edge`（一等公民，含 Condition/Priority/Label）、`NodeRunner` 接口、`ExecutionContext`
- 6 种 Runner：`autoRunner`、`loopRunner`、`forkRunner`、`approveRunner`、`checkpointRunner`、`emitRunner`、`controlRunner`
- `resolve()` 统一路由：无条件边优先 → 条件边按 Priority 匹配
- Sugar 层完整适配：所有方法内部委托 `graph.AddNode`/`AddEdge`
- 外部 API 零变化

新增文件：`graph.go`、`runner.go`

### ✨ SchemaOf — struct → JSON Schema 自动生成

- `SchemaOf(v)` 用反射从 Go struct 自动生成 `map[string]interface{}` JSON Schema
- 支持 5 个标签：`json`（属性名/omitempty）、`desc`（description）、`enum`（枚举约束）、`default`（默认值）
- 类型自动映射 + 嵌套递归 + 指针解引用

新增文件：`schema.go`、`schema_test.go`（10/10 PASS）

### 🗑️ 示例重写

删除旧示例，新增 4 个结构化示例 + 共享配置：

```
01_hello_seele → 02_inline_tools → 03_workplan → 04_mcp
```

### 🐛 Bug 修复

- `NodeResult.Output` 从未赋值 → `FinalOutput()` 永远空（race test 发现）
- `Shutdown()` 未停止 hub gRPC（资源泄漏）
- `New()` 失败时 hub goroutine 泄漏 → 启动顺序重排
- 35 个文件 `sukasukasuka123` → `RedHuang-0622` 用户名迁移

### 📊 变更统计

53 文件，+4095 / -1586 行

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
