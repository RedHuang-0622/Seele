# 号池策略 & 双策略路由

> 决策记录：llm_config.provider 锁死消息格式，Account 只做路由。
> 更新：2026-07-08

---

## 架构总览

```
┌─────────────────────────────────────────────────────────────────┐
│                       accounts.yaml                             │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  llm_config:                                             │   │
│  │    provider: openai        ← 锁死消息格式（不可变）        │   │
│  │    max_tokens: 4096                                       │   │
│  │    timeout: 60                                            │   │
│  │    temperature: 0.7                                       │   │
│  ├──────────────────────────────────────────────────────────┤   │
│  │  accounts:                         ← 同格式下的号池       │   │
│  │    - name: main                                           │   │
│  │      base_url: https://api.deepseek.com                   │   │
│  │      api_key: sk-xxx                                      │   │
│  │      model: deepseek-v4-flash                             │   │
│  │      priority: 1                                          │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
                               │
           ┌───────────────────┴───────────────────┐
           │                                       │
           ▼                                       ▼
  ProviderStrategy("openai")             function.Strategy("openai")
  ┌──────────────────────┐              ┌──────────────────────┐
  │ BuildRequest()       │              │ EncodeTools()        │
  │ ParseResponse()      │              │ DecodeToolCall()     │
  │ ParseSSEEvent()      │              │                      │
  │ Endpoint()           │              │ OpenAI: 原样传递      │
  │ AuthHeader()         │              │ Anthropic: 格式转换   │
  │ SSEHeaders()         │              └──────────────────────┘
  └──────────────────────┘
```

## 设计原则

### 1. llm_config.provider 是唯一格式来源

`provider` 决定同一 session 内的所有消息格式：

| 字段 | 决定权 | 说明 |
|------|--------|------|
| 消息体结构 | `llm_config.provider` | OpenAI 的 `role/content/tool_calls` vs Anthropic 的 `role/content[]` |
| 请求端点 | `llm_config.provider` | `/chat/completions` vs `/v1/messages` |
| 认证头 | `llm_config.provider` | `Authorization: Bearer` vs `x-api-key` |
| 工具编码 | `llm_config.provider` | `function:{name,arguments}` vs `tool_use:{name,input}` |
| SSE 帧格式 | `llm_config.provider` | `data: {...}` vs `event: content_block_delta` |

**Session 内不可切换**——一旦 ChatClient 的 `provider` 设好，`effectiveStrategy()` 读取该值后不变。换 provider = 加载不同的 `account-{provider}.yaml`。

### 2. Account 只做路由

每个 Account 只剩下：

```yaml
accounts:
  - name: deepseek-main
    base_url: https://api.deepseek.com    # 请求发到哪
    api_key: sk-xxx                        # 用谁的 key
    model: deepseek-v4-flash               # 用什么模型
    priority: 1                            # 优先级（越小越优先）
```

`provider` 字段已从 account 级移除，由 loader 在加载时自动从 `llm_config.provider` 继承。Account 池支持：

- **round-robin**：同优先级账号轮转
- **priority 排序**：高优先级账号优先使用
- **disabled**：临时禁用账号
- **per-account override**：`max_tokens` / `temperature` 可逐账号覆盖全局值

### 3. 双策略同路由

```
Account.provider = "openai"（从 llm_config 继承）
    │
    ├── ProviderStrategy("openai")      ───    HTTP 传输层
    │       .BuildRequest()                     构建请求体
    │       .ParseResponse()                    解析响应体
    │       .ParseSSEEvent()                    解析流式帧
    │       .Endpoint()                         /chat/completions
    │       .AuthHeader()                       Authorization: Bearer
    │       .SSEHeaders()                       Accept: text/event-stream
    │
    └── function.Strategy("openai")    ───    工具编码层
            .EncodeTools()                      OpenAI: tools → {type,function}
            .DecodeToolCall()                   OpenAI: {id,function} → ToolCall
```

两层策略共享同一个 provider name，确保传输格式和工具格式一致。

## 配置示例

### OpenAI 格式（`config/account-openai.yaml`）

```yaml
llm_config:
  provider: openai
  max_tokens: 4096
  timeout: 60
  temperature: 0.7

accounts:
  - name: deepseek-main
    base_url: https://api.deepseek.com
    api_key: sk-xxx
    model: deepseek-v4-flash
    priority: 1
```

### Anthropic 格式（`config/account-anthropic.yaml`）

```yaml
llm_config:
  provider: anthropic
  max_tokens: 4096
  timeout: 60
  temperature: 0.7

accounts:
  - name: deepseek-main
    base_url: https://api.deepseek.com/anthropic
    api_key: sk-xxx
    model: deepseek-v4-flash
    priority: 1
```

## 代码调用链

```
main()
  ├── api.LoadFullAccountsConfig("account-openai.yaml")
  │     └── LoadFullAccountsConfigBytes()
  │           ├── 解析 llm_config → LLMConfigEntry
  │           ├── 解析 accounts → []Account（provider 从 llm_config 继承）
  │           └── 返回 LoadResult{LLMDefaults, Pool}
  │
  ├── agent.New(Options{LLMConfig: ...})
  │     └── 创建 Agent → 内含 ChatClient
  │
  ├── chatClient.WithAccountPool(pool)
  ├── chatClient.SetProvider(llmDefaults.Provider)    ← 锁死格式
  │
  └── eng.Chat(ctx, "你好")
        └── chatLoop → callLLM
              └── chatClient.Complete(history, tools)
                    ├── effectiveAccount()      → 从 pool 取账号
                    ├── effectiveStrategy()     → 读 c.provider 选策略
                    ├── requestOpts()           → 合并账号级覆盖
                    ├── effectiveModel()        → 取账号/全局 model
                    ├── strategy.BuildRequest(...)  → 构建请求体
                    ├── HTTP POST → API
                    └── strategy.ParseResponse(...) → 解析响应
```

## 与旧格式的差异

| 维度 | 旧格式 | 新格式 |
|------|--------|--------|
| 配置文件 | `config.yaml` + `accounts.yaml` 两个文件 | 单个 `account-{provider}.yaml` |
| provider 位置 | 每个 account 写自己的 provider | `llm_config.provider` 统一定义 |
| 消息格式来源 | 混用（account.Provider + fallback） | `llm_config.provider` 唯一来源 |
| session 内切换 | `SetProviderFilter()` 随意切 | 不允许——换 provider = 换文件 |
| 工具注册 | 直接传 `types.Tool`（假设 OpenAI 格式） | 通过 `function.Strategy` 做格式转换 |
