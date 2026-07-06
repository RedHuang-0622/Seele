# Seele v0.5 Architecture Redesign

> 代号：**Two Gateways** — 双网关解耦 + 纯抽象原语 + 会话上下文独立

## 1. 设计目标

- **完全解耦**：Agent（编排）与 Session（上下文）彻底分离，各自独立演化
- **双网关**：API 号池网关（连接管理）与 Tool 网关（工具路由）职责分明
- **纯抽象原语**：图编排只有 Node/Edge/Graph 接口，无具体实现
- **树形依赖**：避免循环依赖，每层只依赖下层
- **开发者体验**：通过 JSON/YAML 灵活配置，框架层提供默认实现

## 2. 整体架构

```
┌──────────────────────────────────────────────────────────────────┐
│                          agent/ (编排层)                          │
│                                                                  │
│  ┌──────────┐    ┌──────────┐    ┌───────────────────────────┐  │
│  │ api/     │    │ tool/    │    │ gateway/                  │  │
│  │ 号池抽象  │    │ 工具抽象  │    │  ├── api/   (号池路由)     │  │
│  │          │    │          │    │  └── tool/  (工具路由)     │  │
│  └─────┬────┘    └────┬─────┘    └──────────┬────────────────┘  │
│        │              │                     │                    │
└────────┼──────────────┼─────────────────────┼────────────────────┘
         │              │                     │
         │     ┌────────┴────────┐            │
         │     │  LLM Client     │            │
         └─────┤  (ReAct Loop)   ├────────────┘
               │  桥接双网关      │
               └────────┬────────┘
                        │
                        ▼
┌──────────────────────────────────────────────────────────────────┐
│                    context/ (会话层)                               │
│                                                                  │
│  Holder | Chat(ReAct) | Dispatch | History | Storage | Config   │
│                                                                  │
│  职责：管理一次对话全生命周期                                       │
│  - 对话历史管理 + 上下文压缩 + Token 限制                          │
│  - 工具调用调度（通过 Tool Gateway）                              │
│  - 本地存储（会话数据持久化）                                      │
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│                    graph/ (图编排原语层)                           │
│                                                                  │
│  Node (接口) | Edge (Direct/Conditional/Parallel) | Graph | Runner │
│                                                                  │
│  职责：提供图编排的抽象原语，无具体实现。                            │
│  用例层（上层框架或用户代码）实现 Node 并注册到 Graph               │
└──────────────────────────────────────────────────────────────────┘

┌──────────────────────────────────────────────────────────────────┐
│                    provider/ (外部实现层)                         │
│                                                                  │
│  HubProvider | MCPProvider | InlineProvider                       │
│                                                                  │
│  职责：实现 agent/tool.ToolProvider，对接外部系统                  │
└──────────────────────────────────────────────────────────────────┘
```

## 3. 双网关详解

### 3.1 API Gateway — 号池路由

```
请求 → API Gateway → Select(Account) → LLM Client
```

职责：
- 从 AccountPool 中选择可用账号
- 负载均衡（round-robin / priority-based / weighted）
- 账号健康检查与故障转移
- 速率限制

```go
// agent/gateway/api/gateway.go
package apigw

import "github.com/RedHuang-0622/Seele/agent/api"

// Gateway API 号池网关接口
type Gateway interface {
    // Select 返回下一个可用账号
    Select(ctx context.Context) (*api.Account, error)

    // Health 返回所有账号的健康状态
    Health(ctx context.Context) map[string]error

    // Register 注册或更新账号
    Register(account *api.Account)
}
```

### 3.2 Tool Gateway — 工具路由

```
Tool Schema → Tool Gateway(Plugin过滤) → LLM可见工具
Tool Call  → Tool Gateway(Dispatch)    → 执行结果
```

职责：
- 根据当前 Plugin 过滤可见工具
- 路由工具调用到正确的 Provider
- 管理 Plugin 激活/停用

```go
// agent/gateway/tool/gateway.go
package toolgw

import "github.com/RedHuang-0622/Seele/types"

// Gateway 工具网关接口
type Gateway interface {
    // VisibleTools 返回当前插件规则下 LLM 可见的工具列表
    VisibleTools(ctx context.Context) []types.Tool

    // Dispatch 执行工具调用
    Dispatch(ctx context.Context, name, argsJSON string) (string, error)

    // ActivatePlugin 激活指定插件（""=all-tools）
    ActivatePlugin(name string) error

    // ActivePlugin 返回当前激活的插件名
    ActivePlugin() string

    // DeactivatePlugin 回到 all-tools 模式
    DeactivatePlugin()
}
```

### 3.3 LLM Client — 双网关桥接

LLM Client 位于 API Gateway 和 Tool Gateway 之间，是 ReAct loop 的核心：

```
User Input
    │
    ▼
┌──────────────────────────────────────┐
│  API Gateway                         │
│  Select(Account) → {baseURL, apiKey} │
└────────────┬─────────────────────────┘
             │
             ▼
┌──────────────────────────────────────┐
│  LLM Client (ReAct Loop)             │
│                                      │
│  loop {                              │
│      messages + tools → LLM          │
│      if tool_calls:                  │
│          Tool Gateway.Dispatch()     │
│          append result → messages    │
│      else:                           │
│          return text                 │
│  }                                   │
│                                      │
│  tools: ← Tool Gateway.VisibleTools()│
└──────────────────────────────────────┘
```

```go
// agent/api/client.go
package api

type LLMClient struct {
    apiGW  apigw.Gateway    // API 网关（选号）
    toolGW toolgw.Gateway   // 工具网关（路由）
    client *http.Client
}

// Chat 执行一次完整 ReAct 循环
func (c *LLMClient) Chat(ctx context.Context, messages []types.Message) (string, error) {
    account, _ := c.apiGW.Select(ctx)
    for loop := 0; loop < maxLoops; loop++ {
        tools := c.toolGW.VisibleTools(ctx)
        resp, _ := c.callLLM(ctx, account, messages, tools)
        if len(resp.ToolCalls) == 0 {
            return resp.Content, nil
        }
        for _, tc := range resp.ToolCalls {
            result, _ := c.toolGW.Dispatch(ctx, tc.Function.Name, tc.Function.Arguments)
            messages = append(messages, toolResultMsg(tc, result))
        }
    }
}
```

## 4. 模块详细设计

### 4.1 `agent/api/` — LLM 号池抽象

从 `provider/account/` 提升到 `agent/api/`，保留现有设计。

```
agent/api/
├── pool.go      ← Account, AccountPool（从 provider/account 迁入）
├── client.go    ← LLMClient（从 llm/ 迁入，新增双网关桥接）
└── config.go    ← YAML 配置加载（从 provider/account/config 迁入）
```

### 4.2 `agent/tool/` — 工具抽象

合并 `core/tool/interface.go` + `core/tool_holder/` 为一个包。

```
agent/tool/
├── interface.go  ← ToolProvider, ToolHandler, ToolEntry（现状）
├── holder.go     ← 注册中心 + Dispatch（从 tool_holder 迁入）
├── plugin.go     ← Plugin, PluginManager（从 tool_holder 迁入）
└── config.go     ← Holder 配置（从 tool_holder/config 迁入）
```

### 4.3 `agent/gateway/` — 双网关

```
agent/gateway/
├── api/
│   ├── gateway.go    ← API Gateway 接口
│   └── default.go    ← 默认实现（round-robin 负载均衡）
└── tool/
    ├── gateway.go    ← Tool Gateway 接口
    └── default.go    ← 默认实现（Plugin-based 过滤）
```

### 4.4 `context/` — 会话层

合并 `core/session/` + `history/` 为一个包。

⚠️ **包名冲突说明**：`context` 与 Go 标准库冲突。
建议使用 `./context/` 路径 + 别名导入：

```go
import (
    "context"                    // stdlib
    seelectx "github.com/RedHuang-0622/Seele/context"  // 框架上下文
)
```

或在迁移时改为 `sctx/` / `session/` 避免冲突。

```
context/
├── holder.go      ← Holder（从 core/session 迁入）
├── chat.go        ← ReAct loop（从 core/session/chat 迁入）
├── dispatch.go    ← 工具调用调度（从 core/session/dispatch 迁入）
├── history.go     ← 上下文历史管理（从 history/ 迁入）
├── compress.go    ← 上下文压缩（从 history/ 迁入）
├── limit.go       ← Token 限制（从 history/ 迁入）
├── storage.go     ← 本地存储接口 + 默认文件存储实现
└── config.go      ← Session 配置（从 core/session/config 迁入）
```

### 4.5 `graph/` — 图编排原语

从 `workplan/` 简化为纯抽象原语。

参考 LangGraph 的 Edge 类型：
- **Direct**: A → B（顺序执行）
- **Conditional**: A → B|C（条件路由）
- **Parallel**: A → B,C（并发分支）

```
graph/
├── node.go       ← Node 接口
├── edge.go       ← Edge 类型定义
├── graph.go      ← Graph 结构 + Compile
└── runner.go     ← Runnable 执行引擎
```

### 4.6 `provider/` — 外部实现（保留）

保留现有文件，import 路径从 `core/tool` 改为 `agent/tool`。

```
provider/
├── tool_provider.go   ← type alias → agent/tool
├── hub_provider.go
├── mcp_provider.go
├── inline_provider.go
└── schema.go
```

## 5. 依赖树（保证无循环）

```
types (纯数据，无依赖)
│
├── agent/api         ← types
│   └── agent/gateway/api  ← agent/api
│       └── agent
├── agent/tool        ← types
│   └── agent/gateway/tool ← agent/tool
│       └── agent
├── agent/gateway/api ← agent/api
│   └── agent
├── agent/gateway/tool ← agent/tool
│   └── agent
├── context           ← agent/api.LLMClient + agent/gateway/tool.Gateway
│   └── agent
├── graph             ← 无依赖（纯原语）
│   └── 用例层
└── provider          ← agent/tool
    └── agent
```

验证：从任意节点出发沿箭头方向走，不会回到自身。

## 6. 文件路径变更映射

| 当前路径 | 目标路径 | 操作 |
|----------|----------|------|
| `core/agent/` | `agent/` | 提升为顶级 |
| `core/session/` | `context/` | 改名提升 |
| `core/tool/` | `agent/tool/` | 合并迁入 |
| `core/tool_holder/` | `agent/tool/` | 合并迁入 |
| `llm/` | `agent/api/` | 合并迁入 |
| `provider/account/` | `agent/api/` | 合并迁入 |
| `history/` | `context/` | 合并迁入 |
| `workplan/` | `graph/` | 重写为纯原语 |
| `provider/` | `provider/` | 保留，改 import |
| `types/` | `types/` | 保留 |

## 7. 向后兼容

- `agent/` 包保持 `New()` 接口签名不变
- `context.Holder` 保持 `Chat()` / `ChatStream()` 接口签名不变
- `provider/tool_provider.go` 通过 type alias 保持向后兼容
- 旧 import 路径在迁移期内通过别名或 shim 文件兼容
