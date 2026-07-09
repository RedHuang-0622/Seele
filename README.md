# Seele — Go AI Agent 框架

[![Go Build](https://img.shields.io/badge/go%20build-passing-brightgreen)](#)
[![Go Vet](https://img.shields.io/badge/go%20vet-passing-brightgreen)](#)
[![Go Test](https://img.shields.io/badge/go%20test-passing-brightgreen)](#)
[![Go Version](https://img.shields.io/badge/go-1.25.5-blue)](./go.mod)
[![License](https://img.shields.io/badge/license-MIT-green)](./LICENSE)

Seele 是一个 Go 语言 AI Agent 框架，以 **Engine → Agent → Contexts** 三层架构组织能力，配合 **两层策略模式** 适配不同 LLM Provider 的协议差异。

## 架构

```
┌───────────────────────────────────────────────────────┐
│  Engine (ReAct 循环)                                   │
│  ├── Chat / ChatStream                               │
│  ├── loop: LLM → tool_calls → dispatch → repeat      │
│  └── history: session 级对话历史管理                    │
├───────────────────────────────────────────────────────┤
│  Agent (工具路由层)                                    │
│  ├── tool.Holder: 工具注册中心                          │
│  ├── tool.Gateway: 可见性过滤 + 调度                     │
│  ├── MCP / Hub: 外部工具协议接入                        │
│  └── InlineTool: Go 函数工具                            │
├───────────────────────────────────────────────────────┤
│  Contexts (LLM 会话层)                                 │
│  ├── ChatClient: HTTP 生命周期 + ProviderStrategy       │
│  │     ├── ProviderStrategy("openai")  ← 传输层格式    │
│  │     └── function.Strategy("openai") ← 工具编码格式   │
│  ├── cache: TTL + 内容寻址缓存                          │
│  └── storage: JSON 分片持久化                           │
├───────────────────────────────────────────────────────┤
│  WorkPlan (图编排)                                     │
│  │  Auto / If / Switch / Loop / Fork / Approve         │
│  │  有向图引擎 + Edge 条件路由                          │
│  └── ToTool: 子图包装为工具                             │
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

Provider 决定同一 session 内的所有消息格式，session 内不可切换。
账号池中的多个账号共享同一个 provider 格式，只做路由。

### 支持的 Provider

| Provider | 端点 | 认证 | 工具格式 | 状态 |
|----------|------|------|----------|------|
| OpenAI | `/chat/completions` | `Authorization: Bearer` | `{type,function}` | ✅ 正式 |
| Anthropic | `/v1/messages` | `x-api-key` | `{name,input_schema}` | ✅ 正式 |
| 自定义 | 任意 | 任意 | 任意 | 实现 6 个方法即可 |

## 快速开始

```bash
# 1. 创建配置文件
cp config/account-openai.yaml config/account-openai.yaml
# 编辑 config/account-openai.yaml，填入你的 API Key

# 2. 运行示例
cd example_Implement

go run ./01_hello_seele/ -c ../config/account-openai.yaml   # OpenAI 格式
go run ./01_hello_seele/ -c ../config/account-anthropic.yaml # Anthropic 格式

go run ./06_provider_switch/ -c ../config/account-openai.yaml # 号池切换
```

### 在你的代码中使用

```go
package main

import (
    "flag"
    "github.com/RedHuang-0622/Seele/agent"
    "github.com/RedHuang-0622/Seele/agent/core/api"
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

    // 5. 对话（ReAct 循环自动进行）
    eng := engine.New(agt, engine.WithSystemPrompt("You are helpful."))
    reply, _ := eng.Chat(context.Background(), "Hello!")
    println(reply)
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
contexts/                        LLM 会话上下文
├── cache/                      缓存（TTL + 内容寻址）
├── storage/                    持久化（JSON 分片）
└── react/                      CompletionStrategy
workplan/                       图编排引擎
config/                         配置文件
├── account-openai.yaml         OpenAI 格式
├── account-anthropic.yaml      Anthropic 格式
└── loader.go                   配置加载器
docs/                           文档
test/                           测试
```

## 设计原则

1. **零循环依赖** — Engine → Agent → Contexts 单向依赖
2. **Go 标准库** — net/http、log/slog、sync、atomic，无第三方 LLM SDK
3. **Strategy > Factory** — 协议差异用策略模式封装，不复制 HTTP 编排逻辑
4. **Session 级格式锁定** — `llm_config.provider` 一经设定不可变，保证历史消息格式一致
5. **号池轻量** — Account 只做路由，不携带协议信息（从 `llm_config` 继承）
