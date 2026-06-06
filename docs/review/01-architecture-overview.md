# 架构总览

> 生成时间: 2026-06-06 · Seele v0.1.0

---

## 1. 项目定位

Seele 是一个 **AI Agent 编排框架**，核心能力：

- 组装 LLM（大语言模型）+ 工具（tools）为可自主决策的 AI Agent
- 通过 ReAct 循环实现多轮推理 + 工具调用
- 提供声明式 WorkPlan DSL 构建复杂工作流
- 支持单机 CLI 和多 Agent 集群两种部署模式
- 工具来源支持 microHub (gRPC) 和 MCP (stdio/sse) 双协议

## 2. 分层架构

```
┌──────────────────────────────────────────────────┐
│              应用层 (model_agent/cmd)             │
│       quick_start / work_flow / cli               │
└─────────────────────┬────────────────────────────┘
                      │
┌─────────────────────┴────────────────────────────┐
│              SDK 层 (sdk/)                        │
│  sdk/api      — 类型别名 (Engine = Agent)         │
│  sdk/cli      — REPL + 审批交互                   │
│  sdk/cluster  — 多 Agent 部署框架                  │
└─────────────────────┬────────────────────────────┘
                      │
┌─────────────────────┴────────────────────────────┐
│            编排层 (core/agent/)                    │
│  Agent — 持有 LLM + tool_holder + Hub            │
│  Pool  — 多会话管理                               │
└───────┬───────────────┬──────────────────────────┘
        │               │
        ▼               ▼
┌──────────────┐ ┌────────────────┐
│   会话层       │ │   工具层        │
│ core/session/ │ │ core/tool_     │
│              │ │   holder/      │
│ Holder —     │ │ Holder —       │
│ ReAct 循环   │ │ 路由 + 重试    │
│ 上下文管理   │ │ 多 Provider    │
└───────┬──────┘ └───────┬────────┘
        │                │
        ▼                ▼
┌──────────────┐ ┌────────────────┐
│   LLM 层      │ │  Provider 层   │
│  llm/        │ │  provider/     │
│ ChatClient   │ │ HubProvider    │
│ (stdlib)     │ │ MCPProvider    │
└───────┬──────┘ └───────┬────────┘
        │                │
        ▼                ▼
┌──────────────────────────────────────────────────┐
│              基础设施层                            │
│  types/    — 纯数据结构（零内部依赖）               │
│  history/  — 上下文压缩 + Token 估算               │
│  config/   — YAML 配置加载                        │
└──────────────────────────────────────────────────┘

   ┌──────────────┐
   │   workplan/  │ ← 独立岛（零框架依赖，自定接口）
   │ 声明式 DAG   │
   │ 9 种执行原语  │
   └──────────────┘
```

## 3. 依赖规则

```
types/              ← 零内部依赖，所有包依赖它
  ↓
llm/  history/  config/  provider/   ← 仅依赖 types/
  ↓
core/tool_holder/                    ← 依赖 provider/ + types/
  ↓
core/session/    core/agent/         ← 依赖上层所有包
  ↓
sdk/*                                ← 依赖 core/
  ↓
应用层                                ← 依赖 sdk/

workplan/         ← 完全独立，自定 Agent 接口
```

**准则：依赖方向单向向下，零循环依赖。**

## 4. 核心数据流

### 4.1 ReAct 循环

```
用户: "北京天气怎么样？"
  │
  ▼
session.Holder.Chat(ctx, "北京天气怎么样？")
  │
  ├─ 1. 追加 user message 到 history
  │
  ├─ 2. 检查是否需要压缩上下文（token 超 CompressThreshold）
  │     ├─ Y → LLM 压缩旧历史为摘要
  │     └─ N → 跳过
  │
  └─ 3. FOR loop = 0; loop < maxLoops; loop++:
       │
       ├─ llm.Complete(history, tools)
       │    → 模型返回: {tool_calls: [{name: "GetWeather", args: {city:"北京"}}]}
       │
       ├─ 追加 assistant(tool_calls) 到 history
       │
       ├─ dispatchToolCalls()  ← 并发执行（信号量 max 5）
       │    ├─ tool_holder.Dispatch("GetWeather", `{"city":"北京"}`)
       │    │    ├─ HubProvider.HasTool("GetWeather") → true
       │    │    ├─ HubProvider.Dispatch() → gRPC 调用 weather skill
       │    │    └─ 结果: `{"temp": 25, "condition": "晴"}`
       │    │
       │    └─ 追加 tool_result 到 history
       │
       ├─ LLM 继续推理: "北京今天 25°C，晴天" → 无 tool_calls
       │
       └─ return "北京今天 25°C，晴天"
```

### 4.2 审批流程

```
WorkPlan.Run()
  ├─ Auto("需求分析", ...) → LLM 自主执行
  ├─ Fork("并行开发", ...) → 多 Agent 并发
  └─ Approve("代码审查", ...)
       │
       ├─ Plan Agent 生成执行计划
       ├─ pauseSnapshot ← 保存断点上下文
       ├─ buildQuestion → {status: "awaiting_approval", question_id, options}
       ├─ return PausedWorkPlan
       │
       │  ┌─── 用户看到审批提示 ───┐
       │  │  选项: [执行] [跳过] [终止] │
       │  │  用户选择: execute        │
       │  └────────────────────────┘
       │
       ├─ _decide({question_id, choice: "execute"})
       ├─ SetDecision("execute") → Resume()
       └─ executeApprove() → 继续后续节点
```

## 5. 文件组织

```
Seele/
├── core/                    ← 框架核心
│   ├── agent/               ← 编排层 (3 files, ~350 lines)
│   │   ├── agent.go         ← Agent struct, New(), Shutdown()
│   │   ├── session.go       ← NewSession(), QuickChat(), DirectDispatch()
│   │   └── pool.go          ← Pool 多会话管理
│   ├── session/             ← 会话层 (4 files, ~530 lines)
│   │   ├── interface.go     ← ToolDispatcher, ApprovalCallback
│   │   ├── session.go       ← Holder struct, 历史/配置
│   │   ├── chat.go          ← Chat() / ChatStream() ReAct 循环
│   │   └── dispatch.go      ← dispatchToolCalls() + 审批流转
│   └── tool_holder/         ← 工具层 (3 files, ~140 lines)
│       ├── holder.go        ← Holder, New()
│       ├── provider.go      ← Register() / Unregister()
│       └── tools.go         ← Tools() / Dispatch()（含瞬时重试）
│
├── provider/                ← ToolProvider 实现 (4 files, ~750 lines)
│   ├── tool_provider.go     ← ToolProvider 接口 + ErrToolUnavailable
│   ├── Hub_provider.go      ← HubProvider (gRPC 工具)
│   ├── mcp_provider.go      ← MCPProvider (stdio/sse 工具)
│   └── hub_router.go        ← gRPC 路由器
│
├── workplan/                ← 工作流引擎 (6 files, ~2000 lines)
│   ├── plan.go              ← WorkPlan, Run(), Resume()
│   ├── node.go              ← Node, Question, ChoiceOption
│   ├── primitive.go         ← 9 种执行原语
│   ├── sugar.go             ← 声明式 DSL
│   ├── gate.go              ← 3 种审批 Gate
│   └── validate.go          ← 拓扑校验（DFS 三色环检测）
│
├── sdk/                     ← SDK 层 (5 files, ~755 lines)
│   ├── api/seele_api.go     ← Engine = Agent 类型别名
│   ├── cli/repl.go          ← 交互式 REPL
│   ├── cli/prompt_loader.go ← Prompt 热加载 (fsnotify)
│   └── cluster/             ← 多 Agent 部署
│       ├── harness.go       ← Run() 一站式启动
│       └── handler.go       ← gRPC Handle + 暂停/恢复
│
├── llm/chat_client.go       ← OpenAI 兼容客户端 (~370 lines)
├── history/                 ← 上下文管理 (2 files, ~360 lines)
├── config/loader.go         ← YAML 加载 (~80 lines)
└── types/model.go           ← 共享类型 (~95 lines)
```

## 6. 关键规模指标

| 指标 | 数值 |
|------|------|
| 核心代码行数 | ~5,400 |
| Go 源文件数 | ~31 |
| 包数量 | 13 |
| 外部依赖 | 3 (mcp-go, viper, yaml) |
| 内部依赖 (microHub) | 1 |
| 循环依赖 | 0 |
