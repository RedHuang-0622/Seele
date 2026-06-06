# Provider 模型与工具系统

> ToolProvider 接口 · HubProvider · MCPProvider · 路由与重试

---

## 1. 设计目标

Seele 的工具系统遵循**适配器模式**：框架定义统一接口，不同工具来源各自实现。

```
tool_holder.Holder (聚合层)
  │
  ├─ HubProvider   → gRPC → microHub Skill 进程
  ├─ MCPProvider   → stdio/SSE → MCP Server
  └─ (未来) HTTPProvider → REST API → 外部服务
```

**核心原则**：编排层不感知协议细节。添加新工具来源不需要改 `core/session` 或 `core/agent`。

## 2. ToolProvider 接口

```go
type ToolProvider interface {
    ProviderName() string
    Tools() []types.Tool
    Dispatch(ctx context.Context, name, argsJSON string) (string, error)
    HasTool(name string) bool
}
```

四个方法的最小契约：
- `ProviderName()` — 标识来源，用于 Unregister
- `Tools()` — 获取工具列表（每次调用实时查询，支持热更新）
- `Dispatch()` — 执行工具调用
- `HasTool()` — 快速路由查找（避免遍历完整列表）

## 3. tool_holder.Holder（聚合层）

```go
type Holder struct {
    mu                sync.RWMutex
    providers          []provider.ToolProvider  // 按注册顺序排列
    DispatchRetries    int                      // 默认 3
    DispatchRetryDelay time.Duration            // 默认 2s
}
```

### 3.1 路由规则

```
Dispatch(name, argsJSON)
  ↓
  按注册顺序遍历 providers
    ├─ p.HasTool(name) → true? → p.Dispatch()
    │   ├─ 成功 → return result
    │   └─ ErrToolUnavailable → 重试 (最多 DispatchRetries 次)
    └─ 所有 provider 都不含此工具 → error
```

### 3.2 瞬时重试

```go
for attempt := 0; attempt < h.DispatchRetries; attempt++ {
    for _, p := range providers {
        if p.HasTool(name) {
            result, err := p.Dispatch(ctx, name, argsJSON)
            if err == nil { return result, nil }
            if !errors.Is(err, provider.ErrToolUnavailable) {
                return "", err  // 业务错误，不重试
            }
            lastErr = err
            time.Sleep(h.DispatchRetryDelay)
            break
        }
    }
}
```

**区分瞬时错误和业务错误**：`ErrToolUnavailable` 哨兵错误 → 重试；其他错误 → 直接返回。

### 3.3 Tool 聚合

```go
func (h *Holder) Tools() []types.Tool {
    h.mu.RLock()
    providers := h.providers  // 快照
    h.mu.RUnlock()

    var result []types.Tool
    for _, p := range providers {
        result = append(result, p.Tools()...)
    }
    return result
}
```

每次调用都实时查询（不缓存），支持 MCP Server 动态增减工具。

## 4. HubProvider（gRPC）

### 4.1 架构

```
Seele → HubProvider → BaseHub.Dispatch() → gRPC → Skill 进程
                                     ↓
                              hubRouter.Execute()
                                ├─ registry.GetOnlineTools()
                                └─ 返回 [{Addr, Request}]
```

### 4.2 工具列表来源

```go
func (p *HubProvider) Tools() []types.Tool {
    all := registry.GetOnlineTools()  // 从 registry.yaml 加载
    for _, t := range all {
        if registry.IsOffline(t.Addr) { continue }   // 跳过离线工具
        if retired[t.Name] { continue }               // 跳过已退役工具
        if strings.HasPrefix(t.Name, "_") { continue } // 隐藏内部工具
        result = append(result, types.Tool{...})
    }
}
```

### 4.3 `_` 前缀工具的双重设计

```
Tools()    → 过滤 _ 前缀 → LLM 看不见（安全）
HasTool()  → 不过滤      → tool_holder 可以路由（功能）
Dispatch() → 直接查 registry → 不受 toolIndex 限制（保底）
```

### 4.4 Retire / Restore

允许在运行时禁用/恢复特定工具（不修改 registry 文件）：

```go
engine.Hub().Retire("dangerous_tool")   // 立即不可用
engine.Hub().Restore("dangerous_tool")  // 恢复
```

## 5. MCPProvider（MCP 协议）

### 5.1 传输方式

| 模式 | 实现 | 适用场景 |
|------|------|---------|
| stdio | 启动子进程，通过 stdin/stdout 通信 | 本地工具（如 filesystem） |
| sse | HTTP SSE 连接远程服务器 | 远程/容器化工具 |

### 5.2 多 server 工具名前缀

```
单个 MCP Server → 工具名保持原样: "read_file"
多个 MCP Server → 自动加前缀: "filesystem__read_file"
```

**为什么用 `__`（双下划线）**：单下划线在工具名中很常见（如 `web_search`），双下划线极少用于工具命名，减少误匹配。

### 5.3 热更新

```go
// 运行时动态增减 MCP Server
engine.MCP().Attach(ctx, MCPServerConfig{
    Name: "filesystem", Transport: "stdio",
    Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
})
engine.MCP().Detach("filesystem")

// 刷新某个 server 的工具列表（server 端工具变化时）
engine.MCP().RefreshTools(ctx, "filesystem")
```

### 5.4 生命周期

```
Agent.New()
  └─ tools.Register(HubProvider)

Agent.MCP()  ← 首次调用才创建 MCPProvider（延迟初始化）
  ├─ mcpMu.Lock()
  ├─ 检查 shutdown channel
  ├─ NewMCPProvider() → Register()
  └─ return

Agent.Shutdown()
  ├─ close(shutdown)  ← MCP() 检测到后不再创建
  ├─ healthCancel()
  └─ mcpMu.Lock()
       └─ 遍历 ServerNames() → Detach(name) 逐个断开
```

### 5.5 stdio 子进程泄漏（B1 已修复）

```go
c, err = mcpclient.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
// ...子进程已启动...
if _, err := c.Initialize(ctx, initReq); err != nil {
    c.Close()  // ← B1 修复：清理子进程
    return fmt.Errorf("MCPProvider.Attach: initialize %q: %w", cfg.Name, err)
}
```

## 6. 扩展指南

### 添加新的 Provider

```go
type HTTPProvider struct { ... }

func (p *HTTPProvider) ProviderName() string { return "http" }
func (p *HTTPProvider) Tools() []types.Tool { ... }
func (p *HTTPProvider) Dispatch(ctx, name, argsJSON) (string, error) { ... }
func (p *HTTPProvider) HasTool(name string) bool { ... }

// 注册
tools.Register(&HTTPProvider{...})
```

三行集成，编排层零改动。

### Provider 的返回规范

- 业务成功 → 返回 JSON 字符串（可以是 `"hello"`、`{"temp":25}`、`[1,2,3]`）
- 业务失败 → 返回 `error`（非 `ErrToolUnavailable`），被注入 history 为 `{"error":"..."}`
- 瞬时不可用 → 返回 `fmt.Errorf("%w: ...", ErrToolUnavailable)` → tool_holder 自动重试
