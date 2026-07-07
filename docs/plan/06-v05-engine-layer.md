# Engine Layer — v0.5 架构演进

> 目标：超出 review P0 修复 + 按用户设计稿建立 Engine 顶层编排

## 架构图

```
User Input
    │
    ▼
┌─────────────────────────────────────────────────────┐
│  engine/                                              │
│                                                      │
│  1. 从 contexts 获取历史（带 TTL 缓存判断）              │
│  2. 从 agent/gateway/api 选号（模型路由）               │
│  3. 从 agent/gateway/tool 获取可见工具（Plugin 过滤）    │
│  4. ReAct loop:                                       │
│     a. LLM 调用（流式 onChunk 回调外泄）                │
│     b. tool_call → dispatch → 结果注入                 │
│     c. 循环直至 text reply                              │
│  5. 最终回复 → 存入 contexts 历史管理                    │
└─────────────────────────────────────────────────────┘
    │
    ├── agent/          ← 工具路由 + 号池 + FC 格式
    │   ├── gateway/api/     号池路由（模型选择策略）
    │   ├── gateway/tool/    工具路由（Plugin 切换）
    │   ├── function/        FC 策略（OpenAI/Anthropic)
    │   ├── tool/            注册中心 + Provider
    │   └── api/             号池
    │
    └── contexts/       ← 会话历史（热插拔 + TTL 缓存）
```

## 实施顺序

### P0: 超时传递修复（review 指出）
- `dispatchToolCalls`: 所有 handler 从 context 继承 deadline
- `chatLoop`: 全局 dispatch 超时兜底
- `MCPToolHandler`/`inlineHandler`: 使用传入 context 而非 Background

### P1: Engine 层
- `engine/engine.go` — 主结构 + New
- `engine/loop.go` — ReAct loop（从 contexts/chat.go 迁入并增强）
- `engine/stream.go` — 流式 SSE 外泄回调

### P2: function/ 策略模式
- `agent/function/format.go` — Format 接口
- `agent/function/openai.go` — OpenAI 实现
- `agent/function/anthropic.go` — Anthropic 代理适配

### P3: Context 缓存热插拔
- `contexts/history/cache.go` — TTL 缓存 + 置信度判断
- contexts 支持运行时替换历史
