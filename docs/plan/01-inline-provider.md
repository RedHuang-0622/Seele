# 工具层策略模式重构 + InlineProvider

> 目标：将三种 provider（Hub、MCP、Inline）统一为策略模式，通过 map 中转 Dispatch，所有工具暴露一致的 ToolEntry 结构体。

---

## 0. 策略模式评估

### 当前问题

```go
// 当前：Dispatch 靠 O(n) 遍历 provider + 每个 provider 各自实现 HasTool/Dispatch
func (h *Holder) Dispatch(ctx, name, argsJSON) (string, error) {
    for _, p := range h.providers {       // O(n)
        if p.HasTool(name) {              // 每个 provider 不同实现
            return p.Dispatch(ctx, ...)   // 每个 provider 不同实现
        }
    }
}
```

三种 provider 暴露给 tool_holder 的"工具调用方式"各不相同：
- HubProvider：gRPC method → hub.Dispatch()
- MCPProvider：client.CallTool() → JSON-RPC
- InlineProvider：直接调 Go 函数

### 改造后

```go
// 改造后：所有工具统一为 ToolEntry{Definition + Handler}
// Dispatch 变成 O(1) map lookup，handler 是多态的策略接口
func (h *Holder) Dispatch(ctx, name, argsJSON) (string, error) {
    entry := h.toolMap[name]             // O(1)
    return entry.Handler.Execute(ctx, argsJSON)  // 策略模式
}
```

### 可行性结论

| 考察点 | 结论 |
|--------|------|
| 接口统一 | ✅ 三种 provider 都返回 `[]ToolEntry`，对 holder 透明 |
| 动态工具 | ✅ `Tools()` 每次重建 map，和旧行为一致 |
| `_` 前缀 | ✅ 统一在 holder 层过滤，不分散在各 provider |
| 协议差异封装 | ✅ 演进到 ToolHandler 接口里，每个 handler 自己管理自己的协议细节 |
| provider 依赖变化 | ✅ provider 包从此只依赖 types，不再依赖 microHub 内部协议（Handler 在 provider 内部构造） |
| 重试 | ✅ 仍在 holder.Dispatch 里，对 handler 透明 |
| 性能 | ✅ map 分配成本（~30 entries）远小于一次 LLM HTTP 调用 |
| 兼容性 | ⚠️ ToolProvider 接口破性变更，HubProvider / MCPProvider 需要适配，但都是内部改动 |

**结论：可行，且优于现行设计。** 实施时应放在 InlineProvider 和 Graph 重构之前，因为两个后续改动都依赖新的 ToolProvider 接口。

---

## 1. 新 ToolProvider 接口

### 1.1 核心变更

```
旧接口（4 方法）：                  新接口（1 方法）：
  ProviderName() string              ProviderName() string
  Tools() []types.Tool               Tools() []ToolEntry   ← 返回带 handler 的条目
  Dispatch(ctx, name, argsJSON)      （删除，下沉到 Handler）
  HasTool(name) bool                  （删除，由 holder.map 替代）
```

### 1.2 新类型定义（provider/tool_provider.go）

```go
package provider

import (
    "context"
    "errors"
    "github.com/RedHuang-0622/Seele/types"
)

// ── 策略接口 ──────────────────────────────────────────────────────

// ToolHandler 是工具执行的策略接口。
// 三种实现：
//   HubToolHandler    — gRPC 调用远程 Skill 进程
//   MCPToolHandler    — stdio/SSE 调用 MCP Server
//   InlineToolHandler — 直接调用 Go 函数
type ToolHandler interface {
    Execute(ctx context.Context, argsJSON string) (string, error)
}

// ── 统一暴露结构体 ──────────────────────────────────────────────

// ToolEntry 是所有 provider 向 tool_holder 暴露的统一结构。
// 不管工具来源是 gRPC、MCP 还是 Go 函数，tool_holder 看到的都是
// 一样的 {Definition + Handler} 组合。
type ToolEntry struct {
    Definition types.Tool  // LLM 可见的定义（name / description / schema）
    Handler    ToolHandler // 执行策略
}

// ── Provider 接口 ────────────────────────────────────────────────

// ToolProvider 是所有工具来源的抽象接口。
// 每次调用 Tools() 实时查询，支持热更新（MCP 动态增减、Hub 在线/离线变化）。
type ToolProvider interface {
    ProviderName() string
    Tools() []ToolEntry
}

// ── 哨兵错误 ────────────────────────────────────────────────────

var ErrToolUnavailable = errors.New("tool temporarily unavailable")
```

---

## 2. 三种 Handler 实现

### 2.1 HubToolHandler（gRPC）

```go
// provider/hub_handler.go —— 新建

type HubToolHandler struct {
    hub     *hubbase.BaseHub
    method  string        // gRPC 路由 method，对应 registry.yaml 的 method 字段
    timeout time.Duration
}

func (h *HubToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
    ctx, cancel := context.WithTimeout(ctx, h.timeout)
    defer cancel()
    
    result, err := h.hub.Dispatch(ctx, h.method, argsJSON)
    if err != nil {
        // gRPC 连接错误 → 标记为瞬时不可用，触发 holder 重试
        if isGRPCTransient(err) {
            return "", fmt.Errorf("%w: %v", ErrToolUnavailable, err)
        }
        return "", err
    }
    return result, nil
}
```

### 2.2 MCPToolHandler（stdio/SSE）

```go
// provider/mcp_handler.go —— 新建

type MCPToolHandler struct {
    server   *MCPServer     // MCP 连接
    toolName string         // MCP server 侧的工具名（未加前缀的原始名）
}

func (h *MCPToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
    var args map[string]interface{}
    if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
        return "", fmt.Errorf("MCP tool %q: invalid args: %w", h.toolName, err)
    }
    
    result, err := h.server.client.CallTool(ctx, h.toolName, args)
    if err != nil {
        if isMCPTransient(err) {
            return "", fmt.Errorf("%w: %v", ErrToolUnavailable, err)
        }
        return "", err
    }
    
    return marshalMCPResult(result), nil
}
```

### 2.3 InlineToolHandler（Go 函数）

```go
// provider/inline_handler.go —— 新建

type InlineToolHandler struct {
    Fn func(ctx context.Context, argsJSON string) (string, error)
}

func (h *InlineToolHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
    return h.Fn(ctx, argsJSON)
}
```

---

## 3. tool_holder 重构为 Map 中转

### 3.1 新的 Holder（core/tool_holder/holder.go）

```go
package tool_holder

type Holder struct {
    mu                sync.RWMutex
    providers         []provider.ToolProvider
    toolMap           map[string]provider.ToolEntry   // name → entry（含 _ 前缀工具）
    DispatchRetries   int
    DispatchRetryDelay time.Duration
}

func New() *Holder {
    return &Holder{
        providers:          make([]provider.ToolProvider, 0),
        toolMap:            make(map[string]provider.ToolEntry),
        DispatchRetries:    3,
        DispatchRetryDelay: 2 * time.Second,
    }
}
```

### 3.2 Register / Unregister

```go
func (h *Holder) Register(p provider.ToolProvider) {
    h.mu.Lock()
    defer h.mu.Unlock()
    h.providers = append(h.providers, p)
    h.mergeLocked(p)
}

func (h *Holder) Unregister(name string) {
    h.mu.Lock()
    defer h.mu.Unlock()
    filtered := make([]provider.ToolProvider, 0, len(h.providers))
    for _, p := range h.providers {
        if p.ProviderName() != name {
            filtered = append(filtered, p)
        }
    }
    h.providers = filtered
    h.rebuildLocked() // 删除后重建 map
}
```

### 3.3 Tools() — 重建 map + 过滤 `_` 前缀

```go
func (h *Holder) Tools() []types.Tool {
    h.mu.Lock()
    defer h.mu.Unlock()
    
    h.rebuildLocked()
    
    var result []types.Tool
    for name, entry := range h.toolMap {
        if strings.HasPrefix(name, "_") {
            continue // 框架内部工具，LLM 不可见
        }
        result = append(result, entry.Definition)
    }
    return result
}
```

### 3.4 Dispatch() — O(1) map lookup + 统一重试

```go
func (h *Holder) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
    h.mu.RLock()
    entry, ok := h.toolMap[name]
    h.mu.RUnlock()
    
    if !ok {
        return "", fmt.Errorf("tool %q not found", name)
    }
    
    var lastErr error
    for attempt := 0; attempt < h.DispatchRetries; attempt++ {
        result, err := entry.Handler.Execute(ctx, argsJSON)
        if err == nil {
            return result, nil
        }
        if !errors.Is(err, provider.ErrToolUnavailable) {
            return "", err // 业务错误，不重试
        }
        lastErr = err
        time.Sleep(h.DispatchRetryDelay)
    }
    return "", fmt.Errorf("tool %q: %w", name, lastErr)
}
```

### 3.5 内部辅助

```go
func (h *Holder) rebuildLocked() {
    h.toolMap = make(map[string]provider.ToolEntry, len(h.toolMap))
    for _, p := range h.providers {
        h.mergeLocked(p)
    }
}

func (h *Holder) mergeLocked(p provider.ToolProvider) {
    for _, entry := range p.Tools() {
        name := entry.Definition.Function.Name
        if _, exists := h.toolMap[name]; !exists {
            h.toolMap[name] = entry // 同名工具先注册的优先
        }
    }
}
```

---

## 4. HubProvider 适配

### 4.1 变更点

```diff
- func (p *HubProvider) Tools() []types.Tool
+ func (p *HubProvider) Tools() []provider.ToolEntry

- func (p *HubProvider) Dispatch(ctx, name, argsJSON) (string, error)
+ （删除，逻辑迁入 HubToolHandler）

- func (p *HubProvider) HasTool(name string) bool
+ （删除，由 holder.toolMap 替代）
```

### 4.2 新实现

```go
func (p *HubProvider) Tools() []provider.ToolEntry {
    all := registry.GetOnlineTools()
    var result []provider.ToolEntry
    for _, t := range all {
        if registry.IsOffline(t.Addr) {
            continue
        }
        if _, retired := p.retired[t.Name]; retired {
            continue
        }
        result = append(result, provider.ToolEntry{
            Definition: types.Tool{
                Type: "function",
                Function: types.ToolFunction{
                    Name:        t.Name,
                    Description: t.Description,
                    Parameters:  t.InputSchema,
                },
            },
            Handler: &provider.HubToolHandler{
                Hub:     p.hub,
                Method:  t.Method,
                Timeout: p.timeout,
            },
        })
    }
    return result
}
```

**注意**：`Tools()` 返回的列表**包含 `_` 前缀工具**。`_decide` 等内部工具作为 ToolEntry 注册进 map（Dispatch 可路由），但 `tool_holder.Tools()` 在返回给 LLM 时统一过滤。逻辑从 HubProvider 移到 holder，集中管理。

---

## 5. MCPProvider 适配

### 5.1 变更点

```diff
- func (p *MCPProvider) Tools() []types.Tool
+ func (p *MCPProvider) Tools() []provider.ToolEntry

- func (p *MCPProvider) Dispatch(ctx, name, argsJSON) (string, error)  
+ （删除，逻辑迁入 MCPToolHandler）

- func (p *MCPProvider) HasTool(name string) bool
+ （删除）
```

### 5.2 多 server 前缀逻辑保持不变

```go
func (p *MCPProvider) Tools() []provider.ToolEntry {
    p.mu.RLock()
    defer p.mu.RUnlock()
    
    var result []provider.ToolEntry
    for name, srv := range p.servers {
        for _, t := range srv.tools {
            entryName := t.Name
            if len(p.servers) > 1 {
                entryName = name + "__" + t.Name // 多 server 时加前缀
            }
            result = append(result, provider.ToolEntry{
                Definition: types.Tool{
                    Type: "function",
                    Function: types.ToolFunction{
                        Name:        entryName,
                        Description: t.Description,
                        Parameters:  t.InputSchema,
                    },
                },
                Handler: &provider.MCPToolHandler{
                    Server:   srv,
                    ToolName: t.Name, // 原始工具名，不加前缀
                },
            })
        }
    }
    return result
}
```

---

## 6. InlineProvider 实现

### 6.1 新增文件：provider/inline_provider.go

```go
package provider

type InlineProvider struct {
    mu    sync.RWMutex
    tools map[string]ToolEntry
}

func NewInlineProvider() *InlineProvider {
    return &InlineProvider{tools: make(map[string]ToolEntry)}
}

func (p *InlineProvider) ProviderName() string { return "inline" }

func (p *InlineProvider) Tools() []ToolEntry {
    p.mu.RLock()
    defer p.mu.RUnlock()
    result := make([]ToolEntry, 0, len(p.tools))
    for _, entry := range p.tools {
        result = append(result, entry)
    }
    return result
}

func (p *InlineProvider) Register(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
    p.mu.Lock()
    defer p.mu.Unlock()
    p.tools[name] = ToolEntry{
        Definition: types.Tool{
            Type: "function",
            Function: types.ToolFunction{
                Name:        name,
                Description: desc,
                Parameters:  inputSchema,
            },
        },
        Handler: &InlineToolHandler{Fn: handler},
    }
}

func (p *InlineProvider) Unregister(name string) {
    p.mu.Lock()
    defer p.mu.Unlock()
    delete(p.tools, name)
}
```

---

## 7. Agent 层集成

```go
// core/agent/agent.go

type Agent struct {
    // ... 原有字段 ...
    inlineProvider *provider.InlineProvider
}

func (a *Agent) RegisterInlineTool(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
    if a.inlineProvider == nil {
        a.inlineProvider = provider.NewInlineProvider()
        a.tools.Register(a.inlineProvider)
    }
    a.inlineProvider.Register(name, desc, inputSchema, handler)
}

// SDK 层透传
func (e *Engine) RegisterInlineTool(name, desc string, inputSchema map[string]interface{}, handler func(ctx context.Context, argsJSON string) (string, error)) {
    e.Agent.RegisterInlineTool(name, desc, inputSchema, handler)
}
```

---

## 8. 使用示例

```go
engine, _ := seeleapi.New(seeleapi.Options{
    RegistryPath:  "config/registry.yaml",
    LLMConfigPath: "config/config.yaml",
})
defer engine.Shutdown()

engine.RegisterInlineTool("write_to_project",
    "将代码写入项目文件",
    map[string]interface{}{
        "type": "object",
        "properties": map[string]interface{}{
            "path":    map[string]string{"type": "string"},
            "content": map[string]string{"type": "string"},
        },
        "required": []string{"path", "content"},
    },
    func(ctx context.Context, argsJSON string) (string, error) {
        var args struct{ Path, Content string }
        json.Unmarshal([]byte(argsJSON), &args)
        // 路径安全检查...
        os.WriteFile(filepath.Join("/project", args.Path), []byte(args.Content), 0644)
        return `{"ok":true}`, nil
    },
)

// 内联工具和 Hub/MCP 工具在 LLM 眼里完全一样
agent := engine.NewSession("你是全栈开发助手", 32)
reply, _ := agent.Chat(ctx, "在 src/api/ 下创建一个 ping 路由")
```

---

## 9. 三种 Provider 的 Dispatch 路径对比

```
Dispatch("weather", args):

  旧路径：holder → 遍历 providers
         → HubProvider.HasTool("weather")? → true
         → HubProvider.Dispatch("weather", args)
              → hub.Dispatch(method, args) → gRPC → Skill 进程
         → 返回结果

  新路径：holder → toolMap["weather"]
         → entry.Handler.Execute(ctx, args)
              → HubToolHandler.Execute()
                   → hub.Dispatch(method, args) → gRPC → Skill 进程
         → 返回结果

  Dispatch("read_file", args):
          holder → toolMap["filesystem__read_file"]
                 → MCPToolHandler.Execute()
                      → client.CallTool("read_file", args) → stdio/SSE
                 → 返回结果

  Dispatch("write_to_project", args):
          holder → toolMap["write_to_project"]
                 → InlineToolHandler.Execute()
                      → handler(args)  (本地 Go 函数，零网络)
                 → 返回结果
```

**三种路径，一个入口，策略模式。** tool_holder 不关心 handler 内部是 gRPC 还是 HTTP 还是函数调用。

---

## 10. 文件变更清单

| 文件 | 操作 | 说明 |
|------|------|------|
| `provider/tool_provider.go` | **重写** | 新接口：ToolProvider(1 方法) + ToolHandler + ToolEntry + ErrToolUnavailable |
| `provider/hub_handler.go` | **新建** | HubToolHandler 实现 |
| `provider/mcp_handler.go` | **新建** | MCPToolHandler 实现 |
| `provider/inline_handler.go` | **新建** | InlineToolHandler 实现 |
| `provider/inline_provider.go` | **新建** | InlineProvider 实现 |
| `provider/Hub_provider.go` | 修改 | Tools() 返回 `[]ToolEntry`，删除 Dispatch/HasTool |
| `provider/mcp_provider.go` | 修改 | Tools() 返回 `[]ToolEntry`，删除 Dispatch/HasTool |
| `core/tool_holder/holder.go` | **重写** | map 中转 + 统一 `_` 前缀过滤 + Dispatch 策略模式 |
| `core/tool_holder/tools.go` | 修改 | 逻辑迁入 holder.go |
| `core/tool_holder/provider.go` | 不变 | Register/Unregister 逻辑已在 holder.go |
| `core/agent/agent.go` | 修改 | 加 `inlineProvider` + `RegisterInlineTool` |
| `sdk/api/seele_api.go` | 修改 | 透传 `RegisterInlineTool` |
| `example_Implement/inline_using/main.go` | 新建 | 示例：内联工具 + Hub 工具混合使用 |

---

## 11. 不做的

- **不替代 HubProvider**：跨机器 gRPC 能力对某些场景仍有价值
- **不做 tool 热更新 for InlineProvider**：内联工具是编译时注册的 Go 函数，需要热更新用 MCP
- **不做 HubProvider Retire/Restore 的重构**：保留在 HubProvider 内部，通过 `Tools()` 返回的新旧变化自然反映到 map
- **不引入依赖注入框架**：map 本身就是注册表，不需要额外的 DI 容器
