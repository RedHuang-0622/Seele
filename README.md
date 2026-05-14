# Seele 框架使用指南

## 快速索引

| 场景 | 示例文件 |
|------|---------|
| Hello World | `01_quick_start/main.go` |
| 挂载 MCP Server | `02_mcp_server/main.go` |
| 流式输出 + 历史管理 | `03_streaming_chat/main.go` |
| 多 Agent / AgentPool | `04_multi_agent/main.go` |
| 动态工具管理 | `05_dynamic_tools/main.go` |
| 封装为 HTTP 服务 | `06_web_service/main.go` |
| 生产级完整示例 | `07_production/main.go` |

---

## 1. 配置文件

### config/config.yaml — LLM 配置

```yaml
agent:
  ai_url:     "https://dashscope.aliyuncs.com/compatible-mode/v1"
  ai_name:    "qwen-plus"
  ai_api_key: "sk-xxxx"
  max_tokens: 4096
  timeout:    60
  temperature: 0.7
```

### config/registry.yaml — Hub Skill 注册

```yaml
tools:
  - name: "weather"
    method: "GetWeather"        # gRPC 路由 method，与 Skill.SetMethod() 一致
    description: "查询城市天气"
    addr: "localhost:50101"     # Skill 进程监听地址
    input_schema: |
      {
        "type": "object",
        "properties": {
          "city": { "type": "string" }
        },
        "required": ["city"]
      }
```

---

## 2. 编写 Hub Skill（microHub 能力）

每个 Skill 是一个独立的 Go 进程，实现 `Execute` 方法：

```go
type WeatherSkill struct {
    skillbase.BaseSkill
}

func (s *WeatherSkill) Execute(ctx context.Context, req *pb.ToolRequest) error {
    var input WeatherRequest
    json.Unmarshal(req.Params, &input)

    result := queryWeather(input.City)
    raw, _ := json.Marshal(result)
    return s.ReplyOK(req, raw)
}

func main() {
    skill := &WeatherSkill{}
    skill.SetMethod("GetWeather")   // 与 registry.yaml method 字段一致
    skill.SetAddr("localhost:50101")
    skill.Serve()
}
```

**启动顺序：**
```bash
go run ./skills/weather/   # 先启动所有 skill 进程
go run ./07_production/    # 再启动主程序
```

---

## 3. 注册 MCP Server

```go
// stdio 模式（启动子进程）
engine.AttachMCPServer(ctx, Seele.MCPServerConfig{
    Name:      "filesystem",
    Transport: "stdio",
    Command:   "npx",
    Args:      []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
    Env:       []string{"LOG_LEVEL=debug"},  // "KEY=VALUE" 格式
})

// SSE 模式（连接远程 server）
engine.AttachMCPServer(ctx, Seele.MCPServerConfig{
    Name:      "remote",
    Transport: "sse",
    URL:       "http://mcp-server.example.com/sse",
})

// 运行时卸载
engine.DetachMCPServer("filesystem")
```

**多 server 时工具名自动加前缀：**
- 单个 MCP Server → `read_file`（原名）
- 多个 MCP Server → `filesystem__read_file`（防冲突）

---

## 4. Agent 开发核心模式

### 普通对话
```go
agent := engine.NewAgent("你的 system prompt")
reply, err := agent.Chat(ctx, "用户输入")
```

### 流式对话
```go
_, err := agent.ChatStream(ctx, "用户输入", func(delta string) {
    fmt.Print(delta)
})
```

### 多轮对话
```go
agent.Chat(ctx, "第一轮")
agent.Chat(ctx, "第二轮")   // 自动保留上下文
agent.ClearHistory()        // 清空（保留 system 消息）
agent.SetMaxLoops(16)       // 默认 8，复杂任务可调大
```

---

## 5. 架构分层

```
engine.NewAgent(prompt)
  └── agent.Chat(ctx, input)
        ├── runtime.tools()    → 汇总所有 provider 工具
        ├── llm.complete()     → 调用 LLM
        └── runtime.dispatch()
              ├── HubProvider  → gRPC → microHub Skill 进程
              └── MCPProvider  → stdio/SSE → MCP Server
```

扩展新工具来源：实现 `ToolProvider` 接口，一行注册：
```go
engine.Runtime().Register(myCustomProvider)
```

## License

MIT