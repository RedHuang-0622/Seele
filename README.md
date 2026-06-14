# Seele — Go AI Agent 框架

Seele 是一个 Go 语言 AI Agent 框架，提供 LLM 调用、工具调度、工作流编排三层能力。核心设计主张：**Agent = LLM + 工具调度 + 工作流编排，三者解耦，各自可替换**。

## 快速开始

```bash
# 1. 编辑 LLM 配置（填入你的 API Key）
cd example_Implement
cp config/config.example.yaml config/config.yaml
vim config/config.yaml

# 2. 运行示例
go run ./01_hello_seele/     # 最简入门：Agent + 内联工具
go run ./02_inline_tools/    # SchemaOf 深度演示
go run ./03_workplan/        # WorkPlan 工作流引擎
go run ./04_mcp/             # MCP 协议集成
```

## 架构

```
agent.New(opts)                   ← 入口：注入 LLM 配置，返回 Agent
  │
  ├── llm.ChatClient              ← HTTP/SSE，OpenAI 兼容协议
  │
  ├── tool_holder.Holder          ← 工具聚合中心
  │     ├── InlineProvider        → Go 函数，零网络开销
  │     ├── HubProvider           → gRPC 微服务（microHub）
  │     └── MCPProvider           → MCP 协议（stdio / SSE）
  │
  ├── session.Holder              ← ReAct 循环：思考→调用工具→思考...
  │
  └── workplan.WorkPlan           ← DAG 工作流引擎（独立于 Agent）
        ├── sugar.go              → 链式语法糖（Auto/If/Loop/Fork...）
        ├── graph.go              → 图引擎（NodeRunner + Edge + resolver）
        └── runner.go             → 6 种节点执行器
```

**依赖方向严格向下**（零循环依赖）：`types → provider/llm/history → tool_holder → session → agent → sdk`

## 核心概念

### Agent — 编排器

```go
import "github.com/RedHuang-0622/Seele/core/agent"

llmCfg, _ := config.LoadConfig("./config.yaml")
a, err := agent.New(agent.Options{LLMConfig: llmCfg})
defer a.Shutdown()

// 多轮对话
sess := a.NewSession("你是全栈工程师", 8)
reply, _ := sess.Chat(ctx, "用 Go 写一个 HTTP health check")
reply, _ = sess.Chat(ctx, "加上 TLS 支持")  // 自动保留上下文

// 一次性对话
reply, _ = a.QuickChat(ctx, "你是翻译助手", "Hello World 用中文怎么说")
```

### 三种工具来源

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

engine.RegisterInlineTool("query_weather", "查询天气",
    provider.SchemaOf(WeatherInput{}),  // 自动生成 JSON Schema
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
mcp := engine.MCP()
mcp.Attach(ctx, provider.MCPServerConfig{
    Name: "filesystem", Transport: "stdio",
    Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
})
mcp.Attach(ctx, provider.MCPServerConfig{
    Name: "remote-tools", Transport: "sse",
    URL:  "http://mcp-server:8080/sse",
})
mcp.Detach("filesystem")          // 动态卸载
mcp.RefreshTools(ctx, "remote")   // 热更新工具列表
```

### 网络 API（gRPC）

```go
import "github.com/RedHuang-0622/Seele/sdk/api"

// 服务端 — 将 Agent 暴露为 gRPC 服务
go api.Serve(a, ":51111")

// 客户端 — 远程调用
client, _ := api.Dial("localhost:51111")
defer client.Close()
sid, _ := client.NewSession(ctx, "你是助手", 8)
reply, _ := client.Chat(ctx, sid, "远程调用测试")
```

## WorkPlan 工作流引擎

WorkPlan 将多个 LLM Agent 编织为有向图（DAG），支持条件分支、循环、并发、人工审批。

```go
factory := &EngineFactory{engine: engine}  // 适配器
wp := workplan.New(factory, nil, "你是运维助手")

// 链式构建 DAG
wp.Auto("分析日志", "分析最近1小时错误日志").
    If("判断", workplan.Contains("严重错误"), "紧急处理", "常规记录").
    Auto("紧急处理", "触发告警并通知值班", workplan.WithNext("通知")).
    Auto("常规记录", "记录到日志系统").
    Auto("通知", "发送汇总通知")

result, _ := wp.Run(ctx)
fmt.Println(result.FinalOutputString())
```

**完整节点类型：**

| 节点 | 说明 |
| ---- | ---- |
| `Auto` | Agent ReAct 循环，自动调用工具 |
| `If` / `Switch` | 条件分支路由 |
| `Loop` + `Signal` | 循环（支持 Until/MaxIter/OnExhausted + 实时回调） |
| `Fork` | 多 Agent 并发（各自独立上下文） |
| `Approve` / `Gate` | 人工审批（两段式协议：暂停→决策→继续） |
| `Emit` | 将输出保存到命名变量 |
| `Checkpoint` | 快照节点（支持回滚） |
| `Pipeline` / `Retry` | 语法糖（串行流水线 / 重试直到成功） |

**双模式设计**：初级用户用链式语法糖（`wp.Auto().If().Loop()`），高级用户可直接操作图引擎（`graph.AddNode()` + `graph.AddEdge()`）构建任意拓扑。

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
// 会话级配置
sess := session.New(llm, tools, prompt, session.SessionConfig{
    MaxLoops:             8,   // ReAct 最大循环次数
    MaxConcurrentDispatch: 5,   // 工具并发数
    MaxApprovalLoops:      10,  // 嵌套审批深度
})

// WorkPlan Fork 并发
wp := workplan.New(factory, gate, prompt, workplan.WithMaxForkConcurrency(5))
```

## 示例索引

| 示例 | 涵盖 |
| ---- | ---- |
| [01_hello_seele](example_Implement/01_hello_seele/) | Agent 创建、InlineTool 注册、QuickChat |
| [02_inline_tools](example_Implement/02_inline_tools/) | SchemaOf、enum/default/omitempty 标签、嵌套 struct |
| [03_workplan](example_Implement/03_workplan/) | Auto/If/Switch/Loop+Signal/Fork/Approve/Pipeline/Retry |
| [04_mcp](example_Implement/04_mcp/) | MCP stdio + SSE、Attach/Detach/Refresh |

## 已知限制

| 问题 | 状态 |
| ---- | ---- |
| Token 估算使用 `len/3` 启发式（中文场景不够精确） | 计划引入 tiktoken |
| `Pool` 并发不安全 — 当前仅测试使用，生产暂不建议 | 计划用 TemplatePoolByGO 替换 |
| `agent.Pool` 结构体无并发保护 — 零生产调用，标记为实验性 | 等待重构 |
| `sdk/cli` REPL 的 approval handler 与主输入共用 Scanner | 低优先级 |

## 项目状态

v0.4 — 核心稳定，API 可能变化。已完成的重大重构：

- ✅ Provider 策略模式统一三种工具源
- ✅ chatLoop 模板方法消除 Chat/ChatStream 重复
- ✅ WorkPlan 图引擎（Graph + NodeRunner + Edge）
- ✅ SchemaOf 反射生成 JSON Schema
- ✅ atomic.Pointer 优化 Holder 读路径（零锁）
- ✅ primitive.go → runner.go 引擎统一
- ✅ $sdk/api$ type alias → gRPC 网络 API
- ✅ 配置项 DefaultConfig 化
- ✅ Config/Registry 依赖注入（框架核心不读文件）

## License

MIT
