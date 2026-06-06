# 并发模型与安全保证

> Seele 中的锁、channel、goroutine 生命周期

---

## 1. 并发度全景

```
Agent (1 个进程内单例)
  │
  ├─ Hub goroutine     ← gRPC 监听 (ServeAsync)
  ├─ Health probe      ← 定时探测后端工具存活 (StartHealthProbe, 15s)
  │
  ├─ Session 1         ← 无锁 (文档规定不跨 goroutine 使用)
  │   └─ dispatchToolCalls → 并发执行 tool_calls (信号量 max 5)
  │
  ├─ Session 2         ← 独立
  │
  └─ WorkPlan
      └─ Fork → 并发 Agent (信号量 max 3, global sem)
```

**并发哲学**：Agent 负责跨 session 的全局状态（加锁），Session 不加锁（单 goroutine 使用），工具层加锁（多 provider 并发注册/查询）。

---

## 2. 各组件并发安全矩阵

| 组件 | 锁机制 | 保护范围 |
|------|--------|---------|
| `Agent.mcpMu` | `sync.Mutex` | `mcpProvider` 读写 + `Shutdown` 互斥 |
| `Agent.shutdown` | `chan struct{}` | 关闭信号广播，MCP() 检测 |
| `HubProvider.mu` | `sync.RWMutex` | `retired` 集合（Retire/Restore） |
| `HubProvider.mu2` | `sync.RWMutex` | `toolIndex` 缓存（Tools/HasTool） |
| `MCPProvider.mu` | `sync.RWMutex` | `servers` map（Attach/Detach/Tools/HasTool/Dispatch） |
| `tool_holder.mu` | `sync.RWMutex` | `providers` 列表（Register/Unregister/Tools/Dispatch） |
| `WorkPlan.mu` | `sync.RWMutex` | `vars` map（Emit 写入，Fork 并发写） |
| `NetworkApprovalGate.mu` | `sync.Mutex` | `pending` + `questions` map |
| `Signal.mu` | `sync.RWMutex` | `value` + `cbs`（set/get/OnUpdate） |
| `Pool` | 无锁 | 单 goroutine 使用约定 |
| `Holder` | 无锁 | 文档约定不跨 goroutine |

## 3. 关键锁顺序

### 3.1 MCP() 初始化

```
Agent.mcpMu.Lock()
  └─ tool_holder.Holder.mu.Lock()     ← Register() 内部
       └─ (ok, 未嵌套其他锁)
```

调用方必须先获取 Agent.mcpMu，再获取 Holder.mu。反向不存在。

### 3.2 Dispatch 路径

```
tool_holder.Holder.mu.RLock()
  └─ HubProvider.mu2.RLock()          ← HasTool() 内部
tool_holder.Holder.mu.RUnlock()

(dispatch 本身不在锁内)
HubProvider.Dispatch()
  └─ HubProvider.mu.RLock()           ← 检查 retired

MCPProvider.Dispatch()
  └─ MCPProvider.mu.RLock()           ← resolveRoute + 查找 conn
```

都是 RLock，无写锁嵌套，无死锁风险。

### 3.3 Shutdown 路径

```
Shutdown()
  → close(a.shutdown)                 ← 非阻塞广播
  → Agent.mcpMu.Lock()
    → MCPProvider.mu.RLock()          ← ServerNames()
    → MCPProvider.mu.Lock()           ← Detach()
  → Agent.mcpMu.Unlock()
```

MCPProvider 的 RLock → Lock 是安全的（同一 goroutine 内先释放 RLock 再获取 Lock，中间隔着 `ServerNames()` 返回后 RLock 已释放）。

## 4. Channel 设计

| Channel | 类型 | 容量 | 用途 |
|---------|------|------|------|
| `shutdown` | `chan struct{}` | 0 (无缓冲) | 关闭信号广播 |
| `resources` | `chan *resource[T]` | `MaxSize * IdleBufferFactor` | 连接池空闲连接 |
| `sem` (dispatch) | `chan struct{}` | 5 | dispatchToolCalls 并发限流 |
| `sem` (fork) | `chan struct{}` | 3 | primitiveFork 并发限流 |
| `globalWorkPlanSem` | `chan struct{}` | 可配 | 全局 WorkPlan 并发限流 |
| `inputCh` (CLI gate) | `chan string` | 1 | 用户审批输入 |
| `ch` (handler) | `chan *pb.ToolResponse` | 1 | gRPC 响应单发 |
| `waiter.Ch` | `chan *resource[T]` | 1 | 等待队列元素 |

## 5. Goroutine 生命周期

### 5.1 可停止的

| Goroutine | 停止方式 |
|-----------|---------|
| `healthProbe` | `healthCancel()` context → `ctx.Done()` |
| `pingIdleResources` | `closeCtx.Done()` |
| `monitorAndAdjust` | `closeCtx.Done()` |
| `cleanupLoop` (handler) | `close(shutdown)` |
| `watchRegistry` (hub) | `b.ctx.Done()` |
| `timerLoop` (hub) | `b.ctx.Done()` |
| `hub.ServeAsync` | `b.cancel()` (gRPC server GracefulStop) |

### 5.2 不可停止的

| Goroutine | 原因 |
|-----------|------|
| `CLIApprovalGate.Ask` 中的 `Scanln` goroutine | Go stdlib 限制：`fmt.Scanln` 不可取消 |
| `bufio.Scanner` / `reader.ReadString` 阻塞 | I/O 阻塞，外部无法中断 |

### 5.3 会自然退出的

| Goroutine | 退出条件 |
|-----------|---------|
| `dispatchToolCalls` 子 goroutine | 信号量获取 + Dispatch 返回 |
| `Fork` 子 goroutine | Agent.Chat() 返回或 context 取消 |
| `preInit` goroutine | 创建完 MinSize 个连接 |
| `expand` 子 goroutine | 创建完 expandSize 个连接 |

## 6. 并发模式案例

### 6.1 工具并发调度

```go
results := make([]dispatchResult, len(toolCalls))
sem := make(chan struct{}, 5)  // 最多 5 并发
var wg sync.WaitGroup

for i, tc := range toolCalls {
    wg.Add(1)
    go func(i int, tc types.ToolCall) {
        sem <- struct{}{}       // 获取信号量
        defer func() { <-sem }()
        defer wg.Done()

        result, err := h.tools.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
        results[i] = dispatchResult{tc: tc, content: resultOrError}
    }(i, tc)
}
wg.Wait()
// results[0..n] 全部写入完毕（happens-before via wg.Wait）
```

### 6.2 扩容并发控制

```go
// primitiveFork
sem := make(chan struct{}, 3)

for i, branch := range n.forkBranches {
    go func(i int, b ForkBranch) {
        select {
        case sem <- struct{}{}:   // 获取信号量
        case <-ctx.Done():        // 父 context 取消 → 放弃
            results[i] = err
            return
        }
        // ...执行...
        <-sem                     // 释放
    }(i, branch)
}
```

### 6.3 全局 WorkPlan 限流

```go
func (wp *WorkPlan) Run(ctx context.Context) (*WorkPlanResult, error) {
    if globalWorkPlanSem != nil {
        select {
        case globalWorkPlanSem <- struct{}{}:
            defer func() { <-globalWorkPlanSem }()
        case <-ctx.Done():
            return nil, ctx.Err()
        }
    }
    // ...执行...
}
```

**注意**：`SetMaxConcurrentWorkPlans` 加锁后（B8 修复），修改 `globalWorkPlanSem` 是并发安全的。但 `Run()` 中读取 `globalWorkPlanSem != nil` 不加锁——这是安全的，因为 `nil` channel 的读写语义明确：nil channel 永远阻塞，`!= nil` 的检查只是避免在信号量上阻塞。即使有人并发调 `SetMaxConcurrentWorkPlans(0)`，最多是某次 `Run()` 不阻塞（使用 nil sem），而已经在排队的有序退出。不会 crash。

## 7. Data Race 风险点（全部已修复）

| 位置 | 修复前 | 修复后 |
|------|--------|--------|
| `MCP()` | 无锁读写 `mcpProvider` | `mcpMu.Lock()` |
| `Shutdown()` | 无锁访问 `mcpProvider`，health probe 不可停止 | `mcpMu` + `healthCancel` |
| `SetMaxConcurrentWorkPlans` | 无锁写 `globalWorkPlanSem` | `globalWorkPlanSemMu` |
| `buildToolCalls` | 零值 ToolCall 注入 | append + index check |
