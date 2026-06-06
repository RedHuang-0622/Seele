# 设计决策与取舍

> Seele 每个关键设计背后的"为什么"

---

## 1. 三层命名区分

**决策**：编排层用 `Agent`，会话层用 `session.Holder`，工具层用 `tool_holder.Holder`

**问题**：早期版本所有逻辑堆在一个 `Runtime` 结构体里，`Agent` 一词同时指代"AI 代理""对话会话""工具注册"三个概念。

**取舍**：
- 拆成三层后新人理解成本略高（需要知道哪个"Holder"在哪层）
- 换取：每层职责单一，测试可独立 mock，代码行数可控（最大文件 530 行）

**选型理由**：Go 社区习惯用小写包名区分，但 `session.Holder` 和 `tool_holder.Holder` 在同一个调用点可能出现命名冲突。通过 import alias (`tool_holder "..."`) 解决。

---

## 2. LLM 由 Agent 直接持有

**决策**：`Agent.llm *llm.ChatClient`（直接持有），而非通过 Runtime 中转

**问题**：早期每个 `Chat()` 调用都要 `runtime.Complete(messages, tools)` → `llm.Complete()`，Runtime 只是一个无逻辑的中间人。

**取舍**：
- Agent 构造时需要多传一个 `*llm.ChatClient`
- 换取：调用链缩短一层，Agent 的依赖更显式

**选型理由**：如果 LLM 调用路径需要中间件（如计费、限流），可以在 `llm.ChatClient` 上包装，不需要 Runtime。

---

## 3. 两个接口独立注入

**决策**：`session.Holder` 接收 `ChatCompleter`（LLM）和 `ToolDispatcher`（工具）两个独立接口

```go
type Holder struct {
    llm   types.ChatCompleter
    tools ToolDispatcher
}
```

**取舍**：
- Agent 构造时需传入两个依赖
- 换取：测试时可以独立 mock LLM（返回预定义回复）和工具（模拟 tool_call 结果），无需启动真实服务

**设计准则**：接口应该只暴露调用方需要的方法。`ChatCompleter` 只有 `Complete` + `CompleteStream`，`ToolDispatcher` 只有 `Tools` + `Dispatch`。

---

## 4. 工具瞬时重试下沉到 tool_holder

**决策**：`tool_holder.Dispatch()` 内部处理 `ErrToolUnavailable` 重试（默认 3 次 / 2s），session 层不感知

**问题**：gRPC 连接池偶发性满、网络抖动等瞬时错误，调用方如果每次自己写重试逻辑会污染 ReAct 循环代码。

**取舍**：
- 所有 provider 共用相同重试策略（无法按 provider 定制）
- 换取：session 层代码干净，只关心"工具成功"或"工具永久失败"

**设计准则**：瞬时错误属于基础设施层，不应泄漏到业务层。

---

## 5. hubRouter 下沉到 provider

**决策**：`hubRouter`（microHub gRPC 路由）定义在 `provider/` 包，不在 `core/`

**问题**：早期 hubRouter 在 core 包中，导致 core 依赖 microHub 的内部协议（`pb.ToolRequest`、`pb.ToolResponse`）。

**取舍**：
- provider 包承担了 gRPC 协议适配的全部职责
- 换取：如果未来换掉 microHub，只需改 provider 包，编排层代码零改动

---

## 6. `_` 前缀工具对 LLM 不可见

**决策**：`HubProvider.Tools()` 过滤掉 `_` 开头的工具，但 `HasTool()` 仍返回 true（修复 B2 后）

**问题**：框架内部工具（如 `_decide`）被 LLM 看到后，LLM 可能自主调用它，破坏审批流程的安全性。

**双重设计**：
- `Tools()` 返回列表不含 `_` 前缀 → LLM 看不见，不会自主调用
- `HasTool()` 包含 `_` 前缀 → `tool_holder.Dispatch("_decide", ...)` 能正确路由到 HubProvider
- `HubProvider.Dispatch()` 直接查 registry，不经过 `toolIndex` → 双重保险

---

## 7. 审批结果不入 LLM context

**决策**：`awaiting_approval` 响应不追加到 history，LLM 完全不知道审批过程

**问题**：如果让 LLM 感知审批（如追加 system 消息"用户选择了 execute"），会浪费 token（审批 JSON 可能很大）且 LLM 可能产生幻觉（编造审批结果）。

**取舍**：
- LLM 不知道"有人干预过"，可能继续推理时不够灵活
- 换取：token 节省，审批结果不会被 LLM 曲解

**实现**：`dispatchToolCalls()` 检测 `awaiting_approval` → 调用 `OnApproval` 回调 → 用 `_decide` 恢复工作流 → 最终业务结果才注入 history。

---

## 8. WorkPlan 零框架依赖

**决策**：`workplan/` 包不导入任何 Seele 包，定义自己的 `Agent` 接口

```go
type Agent interface {
    Chat(ctx context.Context, input string) (string, error)
}
```

**问题**：如果 WorkPlan 导入 `core/session`，会形成循环依赖（core 可能未来引用 WorkPlan）。

**取舍**：
- WorkPlan 的 `Agent` 接口只能 Chat（不支持 ChatStream）
- 换取：workplan 可独立测试、独立发布、被其他框架复用

**适配方式**：`sdk/cluster/harness.go` 的 `EngineFactory` 实现 `workplan.AgentFactory`，桥接 Seele Agent 和 WorkPlan。

---

## 9. MCP 延迟初始化

**决策**：`MCPProvider` 在首次调用 `Agent.MCP()` 时才创建，而非 `New()` 中

**问题**：大部分 Seele 用户只用 Hub 工具，不需要 MCP。在 New() 中创建空的 MCPProvider 虽无害但多余。

**取舍**：
- 首次 MCP() 调用有轻微延迟（创建空 provider + 注册）
- 换取：非 MCP 场景零开销
- 并发安全通过 `mcpMu` + `shutdown` channel 保证

---

## 10. shutdown channel 机制

**决策**：用 `chan struct{}` 而非 `atomic.Bool` 表达"是否已关闭"

**问题**：`MCP()` 和 `Shutdown()` 存在竞态——Shutdown 中间 `MCP()` 可能在 Shutdown 关停 MCP provider 后又创建新的。

**解法**：
```go
// MCP() 中
select {
case <-a.shutdown:
    return nil   // 已关闭，不创建
default:
}
a.mcpProvider = provider.NewMCPProvider()  // 安全：shutdown 未开始
```

**为什么不用 atomic.Bool**：channel 的 `close` 是不可逆的广播信号，天然支持多 goroutine 等待。`atomic.Bool` 需要轮询或配合条件变量。

---

## 11. 重试参数可配置

**决策**：`tool_holder.Holder` 暴露 `DispatchRetries` 和 `DispatchRetryDelay` 字段

**问题**：早期硬编码 3 次 / 2s。不同场景需求不同（测试要短，生产要长）。

**取舍**：
- 调用方需要显式设置（不设置走默认值）
- 换取：不引入配置系统复杂度（无 config.HolderConfig 等中间结构）

---

## 12. Prompt 热加载

**决策**：`sdk/cli/prompt_loader.go` 用 fsnotify 监听 prompt 文件变化，修改即生效

**问题**：调试 prompt 时每次改完都要重启 REPL，反馈周期长。

**取舍**：
- 引入 fsnotify 依赖（一个间接依赖）
- 换取：改 prompt 后 `/reload` 立即生效，无需重启

**注意**：这是一个**开发者体验优化**，生产环境不应依赖文件监听（应用层应自行管理 prompt）。

---

## 13. CPU 密集型调度的"够用就好"

**决策**：
- `dispatchToolCalls` 并发上限 `maxConcurrent = 5`
- `primitiveFork` 并发上限 `maxConcurrentFork = 3`

**问题**：这两个值都是硬编码的"够用"值，没有配置入口。

**取舍**：
- 不够灵活（无法根据机器配置调整）
- 换取：API 简洁，避免了过度设计。这些值实际够用——工具调用通常只有 2-3 个并行，Fork 分支同理

**设计哲学**：先硬编码，有人反馈不够用再加配置。避免"万一需要"式的过度设计。

---

## 14. 编码风格约定

| 约定 | 示例 | 理由 |
|------|------|------|
| ASCII 分隔线分区 | `// ── Options ──` | 长文件导航清晰 |
| primitive vs sugar | `primitiveAuto` / `Auto` | 执行逻辑与声明式 DSL 分离 |
| `_` 前缀工具 | `_decide` | 框架内用工具对 LLM 不可见 |
| `Holder` 后缀 | `session.Holder`, `tool_holder.Holder` | 统一表示"持有者/管理容器"语义 |
| defer + named return | `(content string, ... err error)` | SSE 解析的错误能清晰返回 |
