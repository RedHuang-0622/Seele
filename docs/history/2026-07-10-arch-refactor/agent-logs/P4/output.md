# Phase 4: Engine chatLoop 解耦

## 目标

将 engine 包的 chatLoop 从 Engine 结构体中解耦为独立的 Loop 接口 + ReActLoop 实现。

## 变更文件

### 1. `G:\Program\go\Seele\engine\loop.go` — 重写

- **Loop 接口**: 定义 `Run(ctx, userInput, onChunk) -> (string, error)`, `History() -> []Message`, `ClearHistory()`
- **ReActLoop 结构体**: 持有 `agent`, `llm`, `history`, `cfg`, `sessionID`, `cache`, `store`, `modelName`, `tracer`
- **ReActLoopOption**: `WithMaxLoops`, `WithSessionID`, `WithModelName`
- **NewReActLoop**: 构造函数，功能选项模式
- **Run**: 将原 `chatLoop` 方法体移入，`e.*` 替换为 `rl.*`，末尾添加 `defer rl.saveToCache()`
- **callLLM / restoreFromCache / saveToCache / truncateResult**: 方法体原样迁移，接收者改为 `*ReActLoop`
- **History / ClearHistory**: ReActLoop 实现

### 2. `G:\Program\go\Seele\engine\engine.go` — 修改

- **Engine 结构体**: 添加 `loop Loop` 字段；保留 `cfg/history/sessionID/cache/store/modelName` 配置字段
- **WithLoop**: 新增 Option，注入自定义 Loop
- **New**: 创建默认 ReActLoop，将 Engine 的配置字段直接赋值给 ReActLoop；初始化历史（system prompt）
- **Chat / ChatStream**: 委托 `e.loop.Run()` + `e.tracer.Export(ctx)`
- **History / ClearHistory**: 委托给 `e.loop.*`

## 设计要点

| 关注点 | 方案 |
|---|---|
| **命名冲突** | `WithCache`/`WithStore`/`WithTracer` 既是 Engine Option 又是 ReActLoopOption（同名不同签名），Go 禁止同一包内同名函数。解法：Engine.New 使用直接字段赋值（同包可见），不通过 ReActLoopOption 传递；仅保留不冲突的 ReActLoopOption（WithMaxLoops/WithSessionID/WithModelName） |
| **saveToCache** | 原 Chat/ChatStream 中 call 后调用；重构后在 Run 中通过 `defer rl.saveToCache()` 保证无论成功/错误均保存 |
| **历史传递** | WithSystemPrompt 在 New 中通过 opt(e) 设置 e.history → 创建 ReActLoop 后 `rl.history = append(rl.history, e.history...)` |
| **tracer 共享** | Engine.Option 设置 `e.tracer` → New 中 `rl.tracer = e.tracer`（同一实例）→ Chat 后 `e.lastTrace = e.tracer.Export(ctx)` 获取完整的 Trace Tree |

## 测试结果

```bash
go vet ./engine/...       # PASS
go test -race -count=3 ./engine/...  # PASS (1.387s)
go test -cover ./engine/...         # PASS (65.1%)
```

存量测试全部通过，未修改测试代码：

- TestEngine_Chat_Basic
- TestEngine_ChatStream_Basic
- TestEngine_Chat_WithToolCalls
- TestEngine_Chat_EmptyInput
- TestEngine_ClearHistory
- TestEngine_Tracer_SimpleText
- TestEngine_Tracer_WithToolCalls
- TestEngine_Tracer_NoopIsDefault
