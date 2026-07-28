# 前置审查报告

## 需求摘要
为 Seele agent 框架创建本地任意文件缓存模块（FileCache + CacheProvider），提供缓存查看方法并按照装配件模式（PluginManager）装配到 tools（ToolProvider）和图编排（NodeStrategy → CachedStrategy）中，最终在 context/chatLoop 层面利用缓存命中减少 LLM token 消耗。

## 影响文件清单

### 新增文件
| 文件路径 | 修改类型 | 说明 |
|---------|---------|------|
| `context/cache.go` | **新增** | CacheProvider 接口 + FileCache 实现（文件缓存、TTL、去重、统计） |
| `context/cache_tool.go` | **新增** | CacheToolProvider 实现 ToolProvider 接口，注册 \_cache\_\* 查看/管理工具 |
| `context/cache_strategy.go` | **新增** | CachedStrategy 适配器，包装任意 NodeStrategy 实现缓存命中 |

### 修改文件
| 文件路径 | 修改类型 | 说明 |
|---------|---------|------|
| `context/config.go` | **修改** | 添加 CacheConfig 字段，拆分 SessionConfig 与 ContextConfig 为独立文件 |
| `context/interface.go` | **修改** | 添加 CacheProvider 类型引用（无侵入） |
| `context/holder.go` | **修改** | 添加缓存字段 + 缓存相关访问方法 |
| `context/chat.go` | **修改** | chatLoop 集成缓存查表（输入哈希命中 → 跳过 LLM 调用） |
| `context/storage.go` | **修改** | 扩展为通用键值存储（非 JSON 感知），定位为 LocalStorage 升级版 |
| `context/session_config.go` | **拆分** | 从 config.go 拆出 SessionConfig 到独立文件 |
| `context/context_config.go` | **拆分** | 从 config.go 拆出 ContextConfig 到独立文件 |

### 无修改文件
| 文件路径 | 原因 |
|---------|------|
| `workplan/strategy.go` | NodeStrategy 接口不变，CachedStrategy 从外部实现该接口 |
| `workplan/runner.go` | 无需修改，strategyRunner 自动适配任何 NodeStrategy |
| `agent/tool/plugin.go` | ToolProvider 接口不变，CacheToolProvider 从外部实现 |
| `agent/tool/holder.go` | 无需修改，Register() 支持任意 ToolProvider |
| `agent/agent.go` | 可扩展（可选），提供快捷方法挂载缓存 |
| `types/model.go` | 无需修改 |
| `provider/inline_provider.go` | 参考模式，CacheToolProvider 采用相同的 ToolProvider 实现方式 |

## 架构设计

```
┌─────────────────────────────────────────────────────────────────┐
│                        context/ 包                              │
│                                                                │
│  ┌──────────┐   ┌──────────────┐   ┌──────────────────┐       │
│  │ cache.go  │   │ cache_tool.go│   │cache_strategy.go │       │
│  │          │   │              │   │                  │       │
│  │CacheProv │◄──│CacheToolProv │   │CachedStrategy    │──┐    │
│  │  ider    │   │ (ToolProv)   │   │(NodeStrategy)    │  │    │
│  │  ▲       │   │              │   │  ▲               │  │    │
│  │  │       │   └──────┬───────┘   └──┼───────────────┘  │    │
│  │FileCache │          │              │                  │    │
│  └────┬─────┘          │              │                  │    │
│       │                │              │                  │    │
│  ┌────┴─────────────────┴──────────────┘                  │    │
│  │ Holder (会话主入口)                                      │    │
│  │   ├─ cache CacheProvider  ← 引用 CacheProvider         │    │
│  │   ├─ chatLoop() → cache check before LLM call          │    │
│  │   └─ CacheStats() / CacheList() / CacheClear()         │    │
│  └────────────────────────────────────────────────────────┘    │
│                                                                │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────┐            │
│  │ config.go   │  │holder.go     │  │chat.go    │ ← cache hit │
│  │ +CacheCfg   │  │ +cache field │  │ +cache    │   skip LLM  │
│  └─────────────┘  └──────────────┘  │   check   │            │
│                                     └───────────┘            │
└────────────────────────────────────────────────────────────────┘
        │                        │
        │ registers as           │ wraps as
        ▼                        ▼
┌──────────────────┐   ┌──────────────────────┐
│ agent/tool/      │   │ workplan/            │
│  Holder          │   │  NodeStrategy        │
│  ├─ Register( )  │   │  ├─ MethodStrategy   │
│  └─ PluginManager│   │  ├─ LLMStrategy      │
│     (装配件模式)   │   │  ├─ AgentStrategy   │
│                   │   │  └─ CachedStrategy  │ ← 新
└──────────────────┘   └──────────────────────┘
```

## 依赖分析

### 上下游依赖
```
context/cache.go  ──── 标准库: crypto/sha256, os, time, sync/atomic
context/cache_tool.go   → agent/tool (ToolProvider interface)
                         → provider (SchemaOf, for tool schema generation)
context/cache_strategy.go → workplan (NodeStrategy interface)
                          → context (CacheProvider)
```

### 关键路径
```
chatLoop()
  ├─ hash(input) → cache.Get(key)
  │   ├─ HIT  → return cached result (zero LLM tokens)
  │   └─ MISS → LLM call → cache.Set(key, result)
  │
  └─ dispatchToolCalls()
       └─ (future: cache tool results for idempotent tools)
```

## 循环依赖检查

| 依赖路径 | 是否有环 | 说明 |
|---------|---------|------|
| context ↔ agent/tool | ✅ **有环风险** | context 包不能 import agent/tool（agent 已 import context）。解法：cache_tool.go 中通过**接口注入**而非导入具体类型。CacheToolProvider 不在 context/ 包内实现，或在 context/ 中只定义接口，在 agent/tool/ 中提供实现。 |
| context ↔ workplan | ✅ **有环风险** | 同上。解法：cache_strategy.go 在 workplan 包内部实现（作为 workplan 的扩展），或放在独立 package。 |

### 解环方案
在 context/ 包内：
- `cache.go`: 只定义 `CacheProvider` 接口 + `FileCache` 实现，零外部依赖
- `cache_tool.go`: **不直接实现 ToolProvider**，改为定义 `CacheToolAssembler` 类型，提供一个 `ToToolProvider() tool.ToolProvider` 方法（接收 `tool.ToolProvider` 类型作为参数，不在 context/ 中 import tool 包）
- `cache_strategy.go`: **不在 context/ 实现**，改为在 `workplan/` 包内扩展（因为 workplan.NodeStrategy 在 workplan 包），或在单独 package `context/cache_strategy.go` 只定义函数签名

**最终解环方案**：
1. `context/cache.go` — 纯接口 + 文件存储，零外部依赖 ✅
2. `context/cache_tool.go` — 定义 `CacheToolProvider` struct，独立包；或用函数式注入
3. `context/cache_strategy.go` — 单独包或者直接在调用方侧构建 CachedStrategy

→ **推荐**：CachedStrategy 作为独立结构放在 workplan 包（扩展文件），CacheToolProvider 放在独立的 `context/cache_tool_provider.go` 中通过 `tool.ToolProvider` 接口注入而不直接 import。

## 风险预估

| 风险 | 概率 | 严重程度 | 缓解措施 |
|------|------|---------|---------|
| 循环依赖（context ↔ tool/ ↔ workplan/） | 中 | 高 | 接口注入，避免直接 import 具体类型 |
| 缓存误命中导致 LLM 使用过期数据 | 中 | 中 | TTL 默认值 + 缓存键含输入哈希 + 可选 ETag/version |
| 文件缓存磁盘占用膨胀 | 低 | 低 | MaxEntries + MaxEntrySize + ClearByPrefix |
| sha256 哈希计算开销 | 低 | 低 | 缓存键短时只计算一次，大输入用长度+前缀hash |
| 并发写入竞争 | 低 | 中 | sync.RWMutex + 原子操作（atomic.Int64）+ 文件原子写入 |

## 建议方案

### 实现路径（按顺序）

1. **context/cache.go** — CacheProvider 接口 + FileCache 实现
   - 哈希内容寻址（SHA256 of content → filename）
   - 内存索引（key → CacheEntry）用于快速查找
   - TTL 过期检查
   - 原子化统计（hits/misses via atomic.Int64）

2. **context/config.go 拆分优化** — 拆分为 `session_config.go` + `context_config.go` + 新增 CacheConfig

3. **context/holder.go 扩展** — 添加 CacheProvider 字段、缓存访问方法

4. **context/chat.go 集成** — chatLoop 中缓存感知（输入哈希 → 缓存查询）

5. **workplan/cache_strategy.go** — CachedStrategy 包装器（在 workplan 包扩展，避免循环依赖）

6. **cache_tool_provider.go（独立包或 app 层）** — 把缓存查看方法注册为 LLM 工具
   - `_cache_stats` → 返回缓存统计（命中率、条目数、总大小）
   - `_cache_list` → 列出所有缓存项（含元数据）
   - `_cache_get` → 获取指定缓存项内容
   - `_cache_clear` → 按前缀清理缓存
   - 这些工具通过 PluginManager 的 Include/Exclude 控制可见性

7. **agent/agent.go 快捷入口** — `Agent.Cache()` 返回 FileCache 引用，`Agent.RegisterCacheTools()` 一键注册缓存工具
