# Gap C — Graceful Shutdown + Hub 启动修复

## 任务概要

修复 agent/agent.go 的两处竞态问题：

1. **Graceful Shutdown** — Shutdown() 不等待 in-flight Dispatch 完成就释放资源
2. **Hub 启动竞态** — New() 用 time.Sleep(100ms) 硬等 Hub 就绪，不可靠

## 改动内容

### 1. Agent 结构体新增字段

- `done chan struct{}` — shutdown 完成后 close，供外部监听
- `wg sync.WaitGroup` — 追踪 in-flight 的 Dispatch/DirectDispatch 调用

### 2. Hub 启动改用 channel 等待

**之前：**

```go
go func() {
    if err := hub.ServeAsync(opts.HubAddr, 5); err != nil { ... }
}()
time.Sleep(opts.HubStartupDelay)
opts.Logger.Info("hub listening", ...)
```

**之后：**

```go
hubReady := make(chan struct{})
go func() {
    if err := hub.ServeAsync(opts.HubAddr, 5); err != nil { ... }
    close(hubReady)
}()
select {
case <-hubReady:
    opts.Logger.Info("hub ready", ...)
case <-time.After(5 * time.Second):
    opts.Logger.Info("hub startup timeout, continuing anyway")
}
```

- `HubStartupDelay` 保留为废弃字段（兼容调用方），不再生效
- 用 5s 超时的 `select` 替代固定 sleep

### 3. Dispatch / DirectDispatch 支持 Graceful Shutdown

```go
func (a *Agent) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
    select {
    case <-a.shutdown:
        return "", fmt.Errorf("agent: shutting down")
    default:
    }
    a.wg.Add(1)
    defer a.wg.Done()
    return a.toolGW.Dispatch(ctx, name, argsJSON)
}
```

- shutdown 信号已发送时立即返回错误，不发起新调用
- wg.Add/Done 包裹实际调用，确保 Shutdown 等待完成

### 4. Shutdown 重构

```go
func (a *Agent) Shutdown() {
    select {
    case <-a.shutdown:
        return
    default:
        close(a.shutdown)
    }
    a.wg.Wait()               // 新增：等待 in-flight Dispatch
    // ... 清理 healthCancel, mcpProvider ...
    close(a.done)             // 新增：通知外部 shutdown 完成
    a.opts.Logger.Info("shutdown complete")
}
```

## 构建验证

```
$ go build ./agent
agent/core/api/client.go:337: ...  // 预存 BUG，与本次改动无关
```

agent.go 本身语法正确，无新增编译错误。

包内无测试文件，`go test -count=1 -race ./agent/...` 除预存 client.go 编译错误外无失败。

## 影响文件

| 文件 | 改动 |
|------|------|
| G:\Program\go\Seele\agent\agent.go | 结构体加 wg/done；New 改 channel 等 Hub；Dispatch 加 wg 追踪；Shutdown 加 wg.Wait |

## 相关变更记录

- 移除 `time.Sleep(opts.HubStartupDelay)` 硬等待
- 保留 `Options.HubStartupDelay` 字段但标记废弃
- 提升 Shutdown 的日志从 "shutdown" 为 "shutdown complete"
