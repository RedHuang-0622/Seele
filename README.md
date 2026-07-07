# Seele — Go AI Agent 框架

[![Go Build](https://img.shields.io/badge/go%20build-passing-brightgreen)](#)
[![Go Vet](https://img.shields.io/badge/go%20vet-passing-brightgreen)](#)
[![Go Test](https://img.shields.io/badge/go%20test-passing-brightgreen)](#)
[![Go Test -race](https://img.shields.io/badge/go%20test%20--race-passing-brightgreen)](#)
[![Go Version](https://img.shields.io/badge/go-1.25.5-blue)](./go.mod)
[![License](https://img.shields.io/badge/license-MIT-green)](./LICENSE)

Seele 是一个 Go 语言 AI Agent 框架，以 **Engine > Agent > Contexts** 的树形依赖组织三层能力：顶层 ReAct 循环、中间工具路由层、底层会话历史管理。每一层职责单一、可替换、零循环依赖。

## 快速开始

```bash
# 1. 编辑 LLM 配置（填入你的 API Key）
cd example_Implement
cp config/config.example.yaml config/config.yaml
vim config/config.yaml

# 2. 运行示例
go run ./01_hello_seele/     # 最简入门：Agent + Engine + 内联工具
go run ./02_inline_tools/    # SchemaOf 深度演示
go run ./03_workplan/        # WorkPlan 工作流引擎
go run ./04_mcp/             # MCP 协议集成
go run ./05_graph_tools/     # 图结构工具编排

# 3. 在你的代码中使用
```

```go
import (
    "github.com/RedHuang-0622/Seele/agent"
    "github.com/RedHuang-0622/Seele/agent/core/tool"   // SchemaOf
    "github.com/RedHuang-0622/Seele/config"
    "github.com/RedHuang-0622/Seele/engine"
)

// 加载配置
llmCfg, _ := config.LoadConfig("./config.yaml")

// 1. 创建 Agent（工具路由层）
agt, _ := agent.New(agent.Options{LLMConfig: llmCfg})
defer agt.Shutdown()

// 2. 注册内联工具
type GreetInput struct {
    Name string `json:"name" desc:"用户名"`
}
agt.RegisterInlineTool("hello", "say hello",
    tool.SchemaOf(GreetInput{}),
    func(ctx context.Context, argsJSON string) (string, error) {
        return `{"reply":"hello, seele"}`, nil
    })

// 3. 创建 Engine（ReAct 循环层）
eng := engine.New(agt, engine.WithSystemPrompt("You are helpful"))

// 4. 对话
reply, _ := eng.Chat(ctx, "Hello!")
fmt.Println(reply)

// 流式响应
eng.ChatStream(ctx, "Tell me a story", func(chunk string) {
    fmt.Print(chunk)
})
```

## 架构

```
                         ┌──────────────────────┐
                         │       Engine          │  ← ReAct 循环
                         │  Chat / ChatStream    │    历史 + 缓存 + 压缩
                         │  cache.Provider       │
                         └────────┬─────────────┘
                                  │  holds
                         ┌────────▼─────────────┐
                         │       Agent           │  ← 工具路由层
                         │  core / gateway       │    装配 + 路由 + 过滤
                         └────────┬─────────────┘
                                  │  manages
                         ┌────────▼─────────────┐
                         │     Contexts          │  ← 会话管理层
                         │  history / cache      │    TTL + 压缩
                         │  holder / dispatch    │
                         └──────────────────────┘
```

**依赖方向严格向下**（树形结构，零循环依赖）：`engine → agent → contexts`

## Engine — 顶层编排器

`engine` 包是框架的入口，封装完整的 ReAct 循环：

```
build history → 压缩 → get tools → call LLM → tool_calls → dispatch → repeat
```

```go
eng := engine.New(agt,
    engine.WithSystemPrompt("你是一位全栈工程师"),
    engine.WithSessionConfig(engine.SessionConfig{
        MaxLoops: 10,
        MaxToolResultChars: 4000,
    }),
)

// 同步
reply, _ := eng.Chat(ctx, "用 Go 写一个 health check")

// 流式
eng.ChatStream(ctx, "解释这段代码", func(chunk string) {
    fmt.Print(chunk)
})

// 缓存支持（JSON 文件，带 TTL + 置信度）
import "github.com/RedHuang-0622/Seele/contexts/cache"

fc, _ := cache.NewFileCache(cache.Config{
    BaseDir:    "./.seele_cache",
    DefaultTTL: 5 * time.Minute,
})
eng2 := engine.New(agt, engine.WithCache(fc))
```

**Engine 职责：**
- 维护对话历史（自动压缩，`EstimateHistoryTokens` + `CompressHistory`）
- 调用 LLM 并处理 stream 与同步两种模式
- 将 LLM 返回的 tool_calls 路由到 Agent.Dispatch
- 可选 JSON 文件缓存（TTL + 命中置信度统计）

## Agent — 工具路由层

`agent` 包是框架的中枢，负责组装 LLM 客户端、工具注册中心、API 与工具双层网关。

### 内部结构

```
agent/
  ├── agent.go               ← 主入口：New(opts) → Shutdown()
  ├── pool.go                ← 废弃，v0.5 已迁移至 gateway
  │
  ├── core/                  ← 实现层
  │   ├── api/               ← LLM 客户端 + 账号池
  │   ├── tool/
  │   │   ├── interfaces/    ← ToolProvider / ToolEntry 接口定义
  │   │   ├── holder/        ← 工具注册中心 + 插件管理
  │   │   ├── hub/           ← gRPC microHub Provider
  │   │   └── mcp/           ← MCP 协议 Provider（stdio / SSE）
  │   ├── function/          ← FC 策略模式
  │   │   ├── strategy.go    ← Strategy 接口：EncodeTools / DecodeToolCall
  │   │   ├── openai.go      ← OpenAI 格式（init 注册 "openai"）
  │   │   └── anthropic.go   ← Anthropic tool_use 格式
  │   └── schema.go          ← SchemaOf 反射工具
  │
  └── gateway/               ← 路由层
      ├── api/               ← API 账号网关：负载均衡 + 健康检查
      └── tool/              ← 工具网关：插件白/黑名单 + 可见性控制
```

### 三种工具源

| 来源 | 协议 | 适用场景 | 开销 |
| ---- | ---- | -------- | ---- |
| **InlineProvider** | Go 函数调用 | 计算、本地操作、Mock | 零网络 |
| **HubProvider** | gRPC (microHub) | 重量级能力（数据库、沙箱） | 网络 |
| **MCPProvider** | MCP (stdio/SSE) | 第三方工具生态 | 网络 |

#### 内联工具 — struct 标签驱动

```go
type WeatherInput struct {
    City string `json:"city" desc:"城市名称"`
    Date string `json:"date,omitempty" desc:"日期" default:"today"`
}

agt.RegisterInlineTool("query_weather", "查询天气",
    tool.SchemaOf(WeatherInput{}),  // 自动生成 JSON Schema
    func(ctx context.Context, argsJSON string) (string, error) {
        var input WeatherInput
        json.Unmarshal([]byte(argsJSON), &input)
        return `{"city":"北京","temperature":22.5}`, nil
    },
)
```

支持标签：`json`（属性名/omitempty）、`desc`（LLM 可见的描述）、`enum`（枚举约束）、`default`（默认值）。嵌套 struct、`[]T`、指针类型自动递归展开。

#### MCP 工具

```go
mcp := agt.MCP()
mcp.Attach(ctx, mcp.ServerConfig{
    Name: "filesystem", Transport: "stdio",
    Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
})
mcp.Attach(ctx, mcp.ServerConfig{
    Name: "remote-tools", Transport: "sse",
    URL:  "http://mcp-server:8080/sse",
})
mcp.Detach("filesystem")          // 动态卸载
mcp.RefreshTools(ctx, "remote")   // 热更新工具列表
```

### 双层网关

**API 网关**（`agent/gateway/api/`）—— 账号负载均衡：
- 从 `AccountPool` 中选择可用账号
- 支持 round-robin 多账号切换
- 健康检查与自动剔除

**工具网关**（`agent/gateway/tool/`）—— 插件白/黑名单：
- 控制 LLM 可见的工具范围
- `VisibleTools(ctx)` 返回过滤后的工具列表
- 插件机制切换工具可见性

### FC 策略模式

`agent/core/function/` 提供可插拔的 Function Calling 格式编解码：

| 策略 | 注册名 | 格式 |
| ---- | ------ | ---- |
| `OpenAIStrategy` | `openai` | JSON tool_definition / tool_call |
| `AnthropicStrategy` | `anthropic` | `tool_use` content block |

```go
import "github.com/RedHuang-0622/Seele/agent/core/function"

function.Names()        // ["openai", "anthropic"]
strat := function.Get("openai")
encoded := strat.EncodeTools(tools)
decoded := strat.DecodeToolCall(raw)
```

## 插件系统

插件（Plugin）是命名工具装配件，通过 `include` / `exclude` + glob 匹配控制工具可见性。

```go
// 定义插件
agt.Tools().Plugin().Define(holder.NewPlugin(
    "code-assist",
    "代码辅助工具集",
    []string{"read_*", "write_*", "search_*"},  // include 模式
    nil,
))

// 激活插件 — 只有匹配的工具对 LLM 可见
agt.Tools().ActivatePlugin("code-assist")

// 停用插件 — 回到 all-tools 模式
agt.Tools().DeactivatePlugin()
```

**匹配规则：**
- `include` 为空 + `exclude` 为空 → 匹配所有工具
- `exclude` 优先于 `include`：先剔除排除列表，再匹包含列表
- 支持 glob：`read_*` 匹配以 `read_` 开头的所有工具名

## Contexts — 会话管理层

`contexts` 包管理对话历史，提供 Holder（会话容器）、缓存、压缩、工具调度分派。

```go
import seelectx "github.com/RedHuang-0622/Seele/contexts"

// 创建会话
h := seelectx.New(llmClient, toolDispatcher, "system prompt", cfg)

// 读历史
h.History()
h.SessionID()

// 缓存
h.SetCache(cacheProvider)
h.CacheStats()
h.CacheGet("key")
h.CacheClearAll()

// 上下文压缩（超出 6144 token 时自动触发）
seelectx.EstimateHistoryTokens(history)
seelectx.CompressHistory(ctx, llm, history, 8192)
```

### JSON 文件缓存

`contexts/cache/` 提供本地文件系统缓存，带 TTL 和命中率统计：

```go
fc, _ := cache.NewFileCache(cache.Config{
    BaseDir:      "./.seele_cache",
    DefaultTTL:   5 * time.Minute,
    MaxEntrySize: 1024 * 100, // 100KB
    MaxEntries:   1000,
})

fc.Set("key", "value")
value, ok := fc.Get("key")
stats := fc.Stats()  // {Entries, HitCount, MissCount, HitRate}
```

## 配置

```yaml
# config.yaml
llm:
  ai_url:     "https://api.deepseek.com"
  ai_name:    "deepseek-v4-flash"
  ai_api_key: "sk-xxxx"
  timeout:    300
```

```go
// 从文件加载
llmCfg, _ := config.LoadConfig("./config.yaml")

// 直接构造
agent.New(agent.Options{
    LLMConfig: types.LLMConfig{
        BaseURL: "https://api.deepseek.com",
        Model:   "deepseek-v4-flash",
        APIKey:  "sk-xxxx",
        Timeout: 300,
    },
})
```

## 示例索引

| 示例 | 涵盖 |
| ---- | ---- |
| [01_hello_seele](example_Implement/01_hello_seele/) | Agent 创建、InlineTool 注册、Engine.Chat |
| [02_inline_tools](example_Implement/02_inline_tools/) | SchemaOf、enum/default/omitempty 标签、嵌套 struct |
| [03_workplan](example_Implement/03_workplan/) | Auto/If/Switch/Loop+Signal/Fork/Approve/Pipeline/Retry |
| [04_mcp](example_Implement/04_mcp/) | MCP stdio + SSE、Attach/Detach/Refresh |
| [05_graph_tools](example_Implement/05_graph_tools/) | 图结构工具编排 |

## 项目状态

v0.5 — 架构重构完成，核心 API 稳定。主要变更：

- **三层树形架构**：Engine → Agent → Contexts，依赖方向严格向下
- **Engine 作为顶层入口**：Chat/ChatStream 统一 ReAct 循环，自带历史管理与缓存
- **Agent 双层网关**：API 网关（负载均衡）+ 工具网关（插件白/黑名单）
- **插件系统**：`Plugin` 命名工具装配件，include/exclude + glob 匹配
- **FC 策略模式**：`function.Strategy` 接口 + OpenAI/Anthropic 格式注册
- **JSON 文件缓存**：`cache.FileCache`，TTL + 置信度统计 + 内容去重
- **上下文压缩**：`EstimateHistoryTokens` + `CompressHistory` 自动裁剪窗口
- **WorkPlan** 工作流引擎继续可用（独立子包 `workplan/`）

## License

MIT
