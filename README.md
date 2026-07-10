# Seele — Go AI Agent 框架

[![Go Build](https://img.shields.io/badge/go%20build-passing-brightgreen)](#)
[![Go Vet](https://img.shields.io/badge/go%20vet-passing-brightgreen)](#)
[![Go Test](https://img.shields.io/badge/go%20test-passing-brightgreen)](#)
[![Go Version](https://img.shields.io/badge/go-1.25.5-blue)](./go.mod)
[![License](https://img.shields.io/badge/license-MIT-green)](./LICENSE)

Seele 是一个 Go AI Agent 框架，核心差异化是 **WorkPlan 图编排 + 三态 NodeStrategy**——你可以在同一个工作流里混合执行全量 Agent、纯 LLM 调用和本地函数，精准控制 token 消耗。不是 LangChainGo 的 Go 移植，是一个从头设计的 Go 原生框架。

## 架构

```
┌───────────────────────────────────────────────────────┐
│  Engine (ReAct 循环)                                   │
│  ├── Chat / ChatStream                               │
│  ├── loop: LLM → tool_calls → dispatch → repeat      │
│  ├── history: session 级对话历史管理                    │
│  └── tracer: 全链路追踪（可选注入，默认零开销）           │
├───────────────────────────────────────────────────────┤
│  Agent (工具路由层)                                    │
│  ├── tool.Holder: 工具注册中心                          │
│  ├── tool.Gateway: 可见性过滤 + 调度                     │
│  ├── MCP / Hub: 外部工具协议接入                        │
│  └── InlineTool: Go 函数工具                            │
├───────────────────────────────────────────────────────┤
│  WorkPlan (图编排 — 核心差异化)                         │
│  │  Method / LLM / Auto / If / Switch / Loop           │
│  │  Fork / Join / Approve / Checkpoint / Emit          │
│  │  三态 NodeStrategy：Method(零token) / LLM(纯文本) /  │
│  │  Agent(全量 ReAct + 工具) 混合编排                    │
│  └── ToTool: 子图包装为工具                             │
├───────────────────────────────────────────────────────┤
│  Contexts (LLM 会话层)                                 │
│  ├── ChatClient: HTTP 生命周期 + ProviderStrategy       │
│  │     ├── ProviderStrategy("openai")  ← 传输层格式    │
│  │     └── function.Strategy("openai") ← 工具编码格式   │
│  ├── cache: TTL + 内容寻址缓存                          │
│  ├── storage: JSON 分片持久化                           │
│  └── _select_account / _current_account (号池切换工具)   │
└───────────────────────────────────────────────────────┘
```

### 两层策略

```
llm_config.provider = "openai"     ← 锁死消息格式
  │
  ├── ProviderStrategy("openai")   ← 传输层
  │     BuildRequest / ParseResponse / ParseSSEEvent
  │     Endpoint / AuthHeader / SSEHeaders
  │
  └── function.Strategy("openai")  ← 工具层
        EncodeTools / DecodeToolCall
```

### 三态 NodeStrategy（WorkPlan 核心差异化）

MethodStrategy — 纯 Go 函数，零 LLM 调用，零 token
LLMStrategy    — 只调 LLM，不挂工具，轻量生成
AgentStrategy  — 完整 ReAct 循环 + 工具调用，全量能力
```

你可以在同一个 WorkPlan 里混合使用：

```go
wp := workplan.New(factory, gate, prompt)
wp.Method("validate", validateFunc).        // 0 token：本地校验输入
  LLM("rewrite", "改写为搜索关键词").         // N token：纯 LLM，不给工具
  Auto("search", "搜索文档").                // N+M token：全量 Agent
  Method("format", formatFunc).             // 0 token：本地格式化
  Auto("answer", "生成最终回答")              // N+M token：全量 Agent
```

Go 生态里**没有其他框架提供这个粒度**的节点类型——LangChainGo 的 Chain、Eino 的 Graph 节点、Galdor 的 Node 都是全量 Agent，你无法在一个工作流里混合零 token 和全量节点来控制成本。

## 支持的 Provider

| Provider | 端点 | 认证 | 工具格式 | 状态 |
|----------|------|------|----------|------|
| OpenAI | `/chat/completions` | `Authorization: Bearer` | `{type,function}` | ✅ 正式 |
| Anthropic | `/v1/messages` | `x-api-key` | `{name,input_schema}` | ✅ 正式 |
| 自定义 | 任意 | 任意 | 任意 | 实现 6 个方法即可 |

Provider 数量不是 Seele 的目标——专注把 Go 原生框架的架构和编排做到最好。需要更多 Provider？两行代码实现一个 Strategy。

### 号池多账号

号池支持 round-robin 轮转、按名称切换和每账号 RPM 限流：

```go
// 切换号池内账号（不丢失对话历史）
client.SelectAccount("deepseek-lite")

// 或让 LLM 自主切换（注册 _select_account 工具后）
agent.RegisterAccountTools()
```

子代理和主 Agent 共享同一个 `AccountPool`，通过 `_select_account` 工具临时切模型，用完切回。

## 快速开始

```bash
# 1. 创建配置文件
cp config/account-openai.yaml config/account-openai.yaml
# 编辑 config/account-openai.yaml，填入你的 API Key

# 2. 运行示例
cd example_Implement

go run ./01_hello_seele/ -c ../config/account-openai.yaml    # OpenAI 格式
go run ./01_hello_seele/ -c ../config/account-anthropic.yaml # Anthropic 格式
go run ./03_workplan/ -c ../config/account-openai.yaml       # WorkPlan 编排
go run ./07_tracer/ -c ../config/account-openai.yaml         # 全链路追踪
```

### 在你的代码中使用

```go
package main

import (
    "context"
    "flag"
    "github.com/RedHuang-0622/Seele/agent"
    "github.com/RedHuang-0622/Seele/agent/core/api"
    "github.com/RedHuang-0622/Seele/contexts/tracer"
    "github.com/RedHuang-0622/Seele/engine"
    "github.com/RedHuang-0622/Seele/types"
)

var cfgPath = flag.String("c", "config/account-openai.yaml", "config path")

func main() {
    flag.Parse()

    // 1. 加载配置
    result, _ := api.LoadFullAccountsConfig(*cfgPath)
    ls := result.LLMDefaults
    pool := result.Pool
    acct := pool.All()[0]

    llmCfg := types.LLMConfig{
        BaseURL: acct.BaseURL, APIKey: acct.APIKey,
        Model: acct.Model, MaxTokens: ls.MaxTokens,
    }

    // 2. 创建 Agent
    agt, _ := agent.New(agent.Options{LLMConfig: llmCfg})
    defer agt.Shutdown()

    // 3. 注入号池 + 锁死格式
    chatClient := agt.LLM().(*api.ChatClient)
    chatClient.WithAccountPool(pool)
    chatClient.SetProvider(ls.Provider)

    // 4. 注册工具
    agt.RegisterTool("hello", "say hello",
        map[string]any{"type": "object", "properties": map[string]any{}},
        func(ctx context.Context, args string) (string, error) {
            return `{"reply":"hello"}`, nil
        })

    // 5. 创建 Engine（可选注入 tracer）
    tr := tracer.NewSimpleTracer()
    eng := engine.New(agt,
        engine.WithTracer(tr),
        engine.WithSystemPrompt("You are helpful."))

    // 6. 对话 + 导出追踪树
    reply, _ := eng.Chat(context.Background(), "Hello!")
    println(reply)

    tree := eng.ExportTrace()
    println(tree.String()) // JSON 追踪树
}
```

## 配置格式

配置文件使用 `account-{provider}.yaml` 格式，`llm_config` 段锁死消息格式：

```yaml
llm_config:
  provider: openai                 # "openai" | "anthropic"（锁死格式）
  max_tokens: 4096
  timeout: 60
  temperature: 0.7

accounts:
  - name: main                     # 号池内多个账号（round-robin 轮转）
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-v4-flash
    priority: 1

  - name: fallback                 # 备选账号
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-v4-pro
    priority: 2
    max_tokens: 8192               # 逐账号覆盖全局设置
    disabled: true                 # 暂时禁用
```

## 示例一览

| 示例 | 说明 | 运行 |
|------|------|------|
| 01_hello_seele | 最简入门：Agent + Engine + 内联工具 | `go run . -c ../config/account-openai.yaml` |
| 02_inline_tools | SchemaOf 深度演示 | 同上 |
| 03_workplan | WorkPlan 工作流引擎（Auto/If/Loop/Fork） | 同上 |
| 04_mcp | MCP 协议集成（stdio / sse） | 同上（需安装 MCP Server） |
| 05_graph_tools | 图结构工具（子图→Tool） | 同上 |
| 06_provider_switch | 号池账号切换演示 | 同上 |
| 07_tracer | Trace Tree 可观测性追踪树 | 同上 |

所有示例均支持 `-c` 指定配置文件，OpenAI 和 Anthropic 格式通用。

## 项目结构

```
agent/                          Agent 编排层
├── agent.go                    核心：Agent 创建 + 工具注册
├── pool.go                     Account 池 + round-robin
├── core/
│   ├── api/
│   │   ├── client.go           ChatClient（HTTP 编排）
│   │   ├── strategy.go         ProviderStrategy 接口
│   │   ├── strategy_openai.go  OpenAI 协议实现
│   │   └── strategy_anthropic.go Anthropic 协议实现
│   ├── function/
│   │   ├── strategy.go         function.Strategy 接口
│   │   ├── openai.go           OpenAI 工具编码
│   │   └── anthropic.go        Anthropic 工具编码
│   └── tool/                   工具注册/调度/MCP/Hub
├── gateway/                    工具/API 网关（可见性+过滤）
engine/                          ReAct 循环引擎
├── engine.go                    Engine 核心 + Tracer 集成
├── loop.go                      chatLoop 埋点
contexts/                        LLM 会话上下文
├── tracer/                      Trace Tree 追踪（NoopTracer 默认零开销）
├── cache/                       TTL + 内容寻址缓存
├── storage/                     JSON 分片持久化
└── react/                       CompletionStrategy
workplan/                        图编排引擎（核心差异化）
├── graph.go                     有向图引擎 + Edge 条件路由
├── node.go                      节点类型 + 执行状态机
├── strategy.go                  NodeStrategy 接口 + 三态实现
├── runner.go                    各 runner 适配器
├── sugar.go                     声明式构建 API
├── plan.go                      编排引擎 + 序列化 + ToTool
├── tracer_internal.go           内置 Tracer/Span 接口（零外部依赖）
├── gate.go                      两段式审批（CLI/Network/Auto）
└── validate.go                  拓扑校验 + 环检测
config/                          配置文件
├── account-openai.yaml
├── account-anthropic.yaml
└── loader.go
docs/                            文档
test/                            测试
```

## 设计原则

1. **零循环依赖** — Engine → Agent → Contexts 单向依赖
2. **Go 标准库** — net/http、log/slog、sync、atomic，零第三方 LLM SDK
3. **Strategy > Factory** — 协议差异用策略模式封装，不复制 HTTP 编排逻辑
4. **WorkPlan 三态节点** — Method(零token) / LLM(纯文本) / Agent(全量) 混合编排，精准控费
5. **号池轻量** — Account 只做路由，不携带协议信息（从 `llm_config` 继承）
6. **可观测性可选** — Tracer 接口 + NoopTracer 默认零开销，注入即开启
7. **可读性优先** — ~65 个库文件，~12.5k 行，无泛型过度使用，一个下午读完
8. **号池原生** — round-robin + 按名切换 + RPM 限流，LLM 可自主切模型
8. **号池原生** — round-robin + 按名切换 + RPM 限流，LLM 可自主切模型

## 定位

Seele 不是 LangChainGo 的 Go 移植。不追求 Provider 数量最多、不追求社区最大。核心差异化是：

- **Go 原生** — 不是 Python 框架的翻译，从头设计，零循环依赖
- **WorkPlan 图编排** — 三态 NodeStrategy，唯一支持在同一个工作流里混合零 token / 轻量 / 全量节点的 Go 框架
- **可读** — 零第三方 LLM SDK，纯 net/http，~12.4k 行，一个人能读完的代码库

适合的场景：想用 Go 写 Agent，需要 WorkPlan 级别的编排控制，认可「读得懂的代码比功能多更重要」的团队。
