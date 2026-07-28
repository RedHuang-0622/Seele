# 实现方案

## 设计目标

1. CacheProvider 接口 — 抽象缓存操作（Get/Set/Delete/List/Stats），面向接口编程
2. FileCache 实现 — 本地文件存储，内容可寻址（SHA256 去重），TTL 过期，命中统计
3. context 包架构优化 — config.go 拆分为 SessionConfig + ContextConfig + CacheConfig 独立文件
4. chatLoop 缓存感知 — 输入哈希 → 缓存查询，命中跳过 LLM 调用（零 token 消耗）
5. CachedStrategy 装饰器 — 包装 NodeStrategy，图编排流程透明缓存
6. 缓存查看工具装配 — 按 Plugin 装配件模式注册 \_cache\_\* 工具

## 设计模式选择

| 模式 | Go 实现 | 应用位置 | 理由 |
|------|--------|---------|------|
| Strategy | `CacheProvider` 接口 | `context/cache.go` | 缓存实现可替换（FileCache / Redis / Mock） |
| Adapter | `CachedStrategy` 包装 `NodeStrategy` | `workplan/cache_strategy.go` | 将缓存注入已有的 NodeStrategy 体系，零侵入 |
| Decorator | `CachedStrategy.Execute` 代理原策略 | `workplan/cache_strategy.go` | 透明添加缓存行为，不改变策略内部逻辑 |
| Factory Method | `NewFileCache(cfg)` | `context/cache.go` | 按配置创建 FileCache 实例 |
| Template Method | `chatLoop` 缓存检查模板 | `context/chat.go` | 统一在 chatLoop 中集成缓存 |
| 装配件模式 | `CacheToolProvider` 实现 `ToolProvider` | `context/cache_tool.go` | 按 Plugin 装配件模式注册缓存查看工具 |
| Functional Options | Options 模式配置缓存 | `context/cache.go` | Go 惯用配置模式 |

## 方案 A: 全内聚 + 策略装饰器（推荐）

### 核心思路
- CacheProvider + FileCache 全在 `context/` 包，零外部依赖
- CachedStrategy 在 `workplan/` 包扩展（NodeStrategy 定义在 workplan）
- 缓存查看工具注册由调用方（`agent/` 或应用层）负责
- chatLoop 缓存检查统一在 `context/` 的 chatLoop 层面

### 关键接口

**context/cache.go — CacheProvider 接口**
```go
type CacheProvider interface {
    Get(key string) (value string, ok bool)
    GetEntry(key string) (*CacheEntry, bool)
    Set(key, value string) *CacheEntry
    SetWithTTL(key, value string, ttl time.Duration) *CacheEntry
    Delete(key string) bool
    ClearByPrefix(prefix string) int
    ClearAll() int
    Keys() []string
    List() []CacheEntry
    Stats() CacheStats
}
```

**context/cache.go — FileCache 实现**
```go
// FileCache 基于文件系统的缓存实现。
// 存储格式：基目录 / sha256(内容) / key → 内容文件 + 元数据 JSON
// 内存索引：key → {元数据, 文件路径}，O(1) 查询
type FileCache struct {
    cfg     CacheConfig
    baseDir string
    index   sync.Map      // key → *cacheIndexEntry
    hits    atomic.Int64
    misses  atomic.Int64
    mu      sync.RWMutex  // 保护元数据写入
}
```

**context/cache.go — CachedEntry 与 CacheStats**
```go
type CacheEntry struct {
    Key         string    `json:"key"`
    CreatedAt   time.Time `json:"created_at"`
    ExpiresAt   time.Time `json:"expires_at,omitempty"`
    HitCount    int64     `json:"hit_count"`
    SizeBytes   int64     `json:"size_bytes"`
    ContentHash string    `json:"content_hash,omitempty"`
}

type CacheStats struct {
    Entries   int     `json:"entries"`
    TotalSize int64   `json:"total_size_bytes"`
    HitCount  int64   `json:"hit_count"`
    MissCount int64   `json:"miss_count"`
    HitRate   float64 `json:"hit_rate"`
}
```

**workplan/cache_strategy.go — CachedStrategy**
```go
// CachedStrategy 包装内层 NodeStrategy，执行前查缓存，命中直接返回。
// 通过 workplan.NodeStrategy 接口与图引擎无缝集成。
type CachedStrategy struct {
    inner  workplan.NodeStrategy
    cache  seelectx.CacheProvider  // 注意：workplan 包不能直接 import seelectx
    prefix string
    ttl    time.Duration
}

// 关键设计决策：通过函数参数或接口注入避免循环依赖
// workplan 包需要能接收 seelectx.CacheProvider，但不 import seelectx
// 方法：在 workplan 包定义缓存接口或通过闭包注入
```

**循环依赖解环方案**：
由于 `workplan` 包不能 import `seelectx`（`seelectx` → `workplan` 是现有依赖方向反），CachedStrategy 可采用两种方式：

1. **接口定义在 workplan**：workplan 包定义自己的缓存接口，调用方提供适配
2. **函数注入**：CachedStrategy 的 Execute 逻辑接收一个 `cacheFn func(key string) (string, bool)` 函数

**推荐方式 2 + 辅助函数**：
```go
// workplan/cache_strategy.go
// CachedStrategy 不直接依赖 seelectx，通过 cacheGetter/cacheSetter 函数注入
type CachedStrategy struct {
    inner       NodeStrategy
    cacheGetter func(key string) (string, bool)
    cacheSetter func(key, value string, ttl time.Duration)
    keyPrefix   string
    ttl         time.Duration
}

// NewCachedStrategy 创建缓存包装策略
func NewCachedStrategy(inner NodeStrategy, opts ...CachedStrategyOption) *CachedStrategy {
    s := &CachedStrategy{inner: inner, ttl: 5 * time.Minute}
    for _, o := range opts {
        o(s)
    }
    return s
}
```

**workplan/cache_strategy.go — 缓存键构建与执行**
```go
func (s *CachedStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) {
    key := s.buildKey(input, ec)
    
    if s.cacheGetter != nil {
        if cached, ok := s.cacheGetter(key); ok {
            return cached, nil  // 缓存命中，零 LLM 调用
        }
    }
    
    result, err := s.inner.Execute(ctx, input, ec)
    if err != nil {
        return "", err
    }
    
    if s.cacheSetter != nil {
        s.cacheSetter(key, result, s.ttl)
    }
    return result, nil
}

func (s *CachedStrategy) buildKey(input string, ec *ExecutionContext) string {
    h := sha256.New()
    h.Write([]byte(s.keyPrefix))
    h.Write([]byte(input))
    if ec != nil {
        h.Write([]byte(ec.PrevOutput))
    }
    return fmt.Sprintf("%s:%x", s.keyPrefix, h.Sum(nil))
}
```

**context/holder.go — 缓存集成**
```go
type Holder struct {
    llm       types.ChatCompleter
    tools     ToolDispatcher
    cache     CacheProvider  // 新增：缓存提供者
    ...
}

func NewWithCache(llm types.ChatCompleter, tools ToolDispatcher, systemPrompt string, cfg SessionConfig, cache CacheProvider) *Holder {
    h := New(llm, tools, systemPrompt, cfg)
    h.cache = cache
    return h
}

// 缓存查看方法
func (h *Holder) CacheStats() CacheStats {
    if h.cache == nil { return CacheStats{} }
    return h.cache.Stats()
}

func (h *Holder) CacheList() []CacheEntry {
    if h.cache == nil { return nil }
    return h.cache.List()
}

func (h *Holder) CacheClear(prefix string) int {
    if h.cache == nil { return 0 }
    return h.cache.ClearByPrefix(prefix)
}
```

**context/chat.go — chatLoop 缓存感知**
```go
func (h *Holder) chatLoop(ctx context.Context, userInput string, strategy completionStrategy) (string, error) {
    if userInput != "" {
        h.history = append(h.history, types.Message{Role: "user", Content: &userInput})
    }
    
    // 缓存检查：如果缓存支持，检查是否已有相同输入的完整回复
    cacheKey := h.buildCacheKey(userInput)
    if h.cache != nil {
        if cached, ok := h.cache.Get(cacheKey); ok {
            // 缓存命中：直接注入历史并返回，零 LLM 调用
            h.history = append(h.history, types.Message{Role: "assistant", Content: &cached})
            return cached, nil
        }
    }
    ...
}
```

**context/config.go → 拆分为三文件**
```
context/session_config.go     — SessionConfig (MaxLoops, MaxConcurrentDispatch, MaxApprovalLoops)
context/context_config.go     — ContextConfig (MaxTokens, CompressThreshold, MaxToolResultChars)
context/cache_config.go       — CacheConfig (Enabled, BaseDir, DefaultTTL, MaxEntries)
```

### 变更范围
| 文件 | 类型 | 改动量 |
|------|------|--------|
| context/cache.go | 新增 | ~250 行 |
| context/cache_config.go | 新增（从 config.go 拆分） | ~40 行 |
| context/session_config.go | 新增（从 config.go 拆分） | ~50 行 |
| context/context_config.go | 新增（从 config.go 拆分） | ~60 行 |
| context/holder.go | 修改 | ~30 行（+缓存字段+方法） |
| context/chat.go | 修改 | ~20 行（+缓存检查） |
| context/config.go | 修改 | ~5 行（精简为 re-export） |
| context/cache_tool.go | 新增 | ~100 行 |
| workplan/cache_strategy.go | 新增 | ~80 行 |
| workplan/strategy.go | 不修改 | — |
| **合计** | **≈ 635 行** | |

## 方案 B: 接口注入 + 函数式配置

### 核心思路
- CacheProvider 接口定义为**最小缓存原语**：Get(key)/Set(key,val,ttl) 仅两个核心方法
- 更丰富的功能（List/Keys/Stats）通过函数式 Option 插拔
- 缓存查看工具直接注册到 `agent/tool.Holder` 作为 ToolProvider
- chatLoop 缓存检查仅在 `CacheProvider.Enabled()` 为 true 时触发

### 关键差异
```go
// 最小接口
type CacheProvider interface {
    Get(key string) (value string, ok bool)
    Set(key, value string, ttl time.Duration)
    
    // 以下可选，通过 CacheWithLister/CacheWithStats 接口扩展
}

// Lister 接口扩展
type CacheLister interface {
    List() []CacheEntry
    Keys() []string
    ClearByPrefix(prefix string) int
}

// Stater 接口扩展
type CacheStater interface {
    Stats() CacheStats
}
```

### 变更范围
比方案 A 约少 60 行（精简接口），但调用方需要类型断言检查扩展接口。

### 优缺点
- **优点**：接口最小化，实现方负担轻
- **缺点**：调用方需类型断言（`lister, ok := cache.(CacheLister)`），不优雅
- **缺点**：无法统一保证所有实现都提供 List/Stats

## 方案 C: 独立缓存包 + 回调注入

### 核心思路
- 缓存不放在 `context/` 包，改为独立 `pkg/cache/` 顶级包
- `context/` 包通过**回调函数**而非直接依赖使用缓存
- chatLoop 的缓存检查通过 `CacheCheckFn func(input string) (cached string, hit bool)` 回调注入
- CachedStrategy 的缓存查询也通过回调注入

### 关键差异
```go
// context/holder.go
type Holder struct {
    ...
    checkCache func(input string) (string, bool)  // 新增：缓存检查回调
    storeCache func(input, output string)          // 新增：缓存存储回调
}
```

### 变更范围
| 文件 | 类型 |
|------|------|
| pkg/cache/cache.go | 新增 |
| pkg/cache/file.go | 新增 |
| pkg/cache/config.go | 新增 |
| context/holder.go | 修改（回调注入） |
| workplan/cache_strategy.go | 新增（回调注入） |
| **合计** | **~同方案 A** |

### 优缺点
- **优点**：context 包完全不感知缓存存在，极致解耦
- **缺点**：回调函数签名限制表达能力；回调组合不方便
- **缺点**：独立包增加 import 路径深度

## 定性对比

| 维度 | 方案 A (全内聚+装饰器) ⭐ | 方案 B (最小接口) | 方案 C (独立包+回调) |
|------|------------------------|------------------|-------------------|
| **耦合度** | 低 — context 包零外部依赖；workplan 通过函数注入解环 | 低 — 同方案 A | **极低** — context 完全不知缓存 |
| **内聚性** | **高** — 缓存与 context 强关联；缓存配置与文化配置同包 | 中 — List/Stats 需类型断言 | 中 — 缓存与 context 分两包 |
| **可测试性** | **高** — CacheProvider 接口易 mock；CachedStrategy 可单独测 | 中 — 扩展接口需多组 mock | 高 — 回调函数易 mock |
| **实现成本** | **低** — ≈635 行，当前架构自然扩展 | 中 — 接口精简但类型断言增加 | **高** — 多包管理，回调签名需对齐 |
| **改动面** | **小** — 新增文件为主，修改 | 小 | 中 — 多一个顶级包 |
| **可回滚性** | **高** — 每步独立提交，缓存移除不改 context 核心逻辑 | 高 | 中 — 回调移除需改 holder.go |
| **团队适配** | **高** — 遵循现有包结构，自然理解 | 中 — Go 类型断言需团队共识 | 低 — 回调模式非 Go 惯用 |
| **装配件集成** | **好** — CacheToolProvider 实现 ToolProvider | 好 | 好 |

## 推荐：方案 A（全内聚 + 策略装饰器）

### 理由
1. **零新增外部依赖**：`context/cache.go` 只用标准库（crypto/sha256, os, sync/atomic, time）
2. **自然解环**：CachedStrategy 在 workplan 包通过函数注入，不修改 workplan 已有代码
3. **与现有架构一致**：cache 作为 context 的自然扩展，像 storage.go 一样在 context 包
4. **渐进可用**：FileCache 本身即可用，chatLoop 集成是附加的（无缓存时行为不变）
5. **深度级要求满足**：接口契约（CacheProvider）、依赖图（已分析）、设计模式（Strategy+Decorator）

### 最大风险
| 风险 | 概率 | 缓解 |
|------|------|------|
| 方案 A 的 CachedStrategy 在 workplan 包，如何引用 CacheProvider | **中 — 已解** | 函数注入：cacheGetter/cacheSetter 闭包 |
| 哈希碰撞（SHA256 → 极低） | 极低 | 不缓解 |
| 缓存文件残留 | 低 | TTL + ClearAll + MaxEntries 三重保障 |
| chatLoop 缓存对多轮对话支持 | 中 | 缓存键需含历史摘要（初始实现只缓存单轮） |

## 循环依赖检查

| 依赖路径 | 状态 | 说明 |
|---------|------|------|
| context/cache.go → 标准库 | ✅ 安全 | 只依赖 crypto/sha256, os, sync, time |
| context/holder.go → context/cache.go | ✅ 安全 | 同包引用，CacheProvider 接口 |
| workplan/cache_strategy.go → context | ⚠️ 无环 | 通过函数注入而非 import |
| context/cache_tool.go → agent/tool | ⚠️ 无环 | 通过接口参数注入，不直接 import；或放在应用层 |
| context → workplan | ✅ 安全 | 已确认：context 不 import workplan |

## 核心接口定义

### context/cache.go
```go
package seelectx

import (
    "crypto/sha256"
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "sync"
    "sync/atomic"
    "time"
)

// ── Config ────────────────────────────────────

type CacheConfig struct {
    Enabled      bool          // 默认 true
    BaseDir      string        // 缓存根目录
    DefaultTTL   time.Duration // 默认 5min
    MaxEntries   int           // 默认 1000
    MaxEntrySize int64         // 默认 1MB
}

func DefaultCacheConfig() CacheConfig { ... }

// ── Entry ─────────────────────────────────────

type CacheEntry struct {
    Key         string    `json:"key"`
    CreatedAt   time.Time `json:"created_at"`
    ExpiresAt   time.Time `json:"expires_at,omitempty"`
    HitCount    int64     `json:"hit_count"`
    SizeBytes   int64     `json:"size_bytes"`
    ContentHash string    `json:"content_hash,omitempty"`
}

// ── Stats ─────────────────────────────────────

type CacheStats struct {
    Entries   int     `json:"entries"`
    TotalSize int64   `json:"total_size_bytes"`
    HitCount  int64   `json:"hit_count"`
    MissCount int64   `json:"miss_count"`
    HitRate   float64 `json:"hit_rate"`
}

// ── Provider ──────────────────────────────────

type CacheProvider interface {
    Get(key string) (value string, ok bool)
    GetEntry(key string) (*CacheEntry, bool)
    Set(key, value string) *CacheEntry
    SetWithTTL(key, value string, ttl time.Duration) *CacheEntry
    Delete(key string) bool
    ClearByPrefix(prefix string) int
    ClearAll() int
    Keys() []string
    List() []CacheEntry
    Stats() CacheStats
}

// ── FileCache ────────────────────────────────

type FileCache struct {
    cfg     CacheConfig
    baseDir string
    index   sync.Map          // key → *cacheIndexEntry
    indexMu sync.RWMutex      // 保护索引批量写入
    hits    atomic.Int64
    misses  atomic.Int64
    closer  sync.Once
}

type cacheIndexEntry struct {
    meta     CacheEntry
    filePath string // 内容文件路径
}

func NewFileCache(cfg CacheConfig) (*FileCache, error) { ... }
func (c *FileCache) Get(key string) (string, bool) { ... }
func (c *FileCache) SetWithTTL(key, value string, ttl time.Duration) *CacheEntry { ... }
func (c *FileCache) Keys() []string { ... }
func (c *FileCache) List() []CacheEntry { ... }
func (c *FileCache) Stats() CacheStats { ... }
```

### workplan/cache_strategy.go
```go
package workplan

import (
    "context"
    "crypto/sha256"
    "fmt"
    "time"
)

// CachedStrategyOption 配置 CachedStrategy
type CachedStrategyOption func(*CachedStrategy)

// WithCacheGetter 设置缓存读取函数
func WithCacheGetter(fn func(key string) (string, bool)) CachedStrategyOption { ... }

// WithCacheSetter 设置缓存写入函数
func WithCacheSetter(fn func(key, value string, ttl time.Duration)) CachedStrategyOption { ... }

// WithCacheTTL 设置缓存 TTL
func WithCacheTTL(ttl time.Duration) CachedStrategyOption { ... }

// WithCacheKeyPrefix 设置缓存键前缀
func WithCacheKeyPrefix(prefix string) CachedStrategyOption { ... }

// CachedStrategy 包装内层策略，提供透明缓存能力
type CachedStrategy struct {
    inner       NodeStrategy
    cacheGetter func(key string) (string, bool)
    cacheSetter func(key, value string, ttl time.Duration)
    keyPrefix   string
    ttl         time.Duration
}

func NewCachedStrategy(inner NodeStrategy, opts ...CachedStrategyOption) *CachedStrategy { ... }

func (s *CachedStrategy) Execute(ctx context.Context, input string, ec *ExecutionContext) (string, error) { ... }
```

### context/cache_tool.go（应用层注册辅助）
```go
// 注意：此文件仅提供辅助函数，不自动注册。
// 调用方显式调用 RegisterCacheTools(toolHolder, cache) 注册。

// RegisterCacheTools 将缓存查看/管理方法注册为工具。
// tools 是 tool_holder.Holder（或实现了 Register 方法的对象）。
// 使用 SchemaOf 自动生成工具参数 Schema。
func RegisterCacheTools(registerFn func(name, desc string, schema map[string]interface{}, handler func(ctx, argsJSON string) (string, error)), cache CacheProvider) {
    // 注册 _cache_stats  — 查看缓存统计
    // 注册 _cache_list   — 列出缓存条目
    // 注册 _cache_get    — 获取指定缓存值
    // 注册 _cache_clear  — 按前缀清理缓存
}
```

## 实现步骤

| # | 步骤 | 文件 | 设计模式 | 说明 |
|---|------|------|---------|------|
| 1 | CacheConfig + CacheEntry + CacheStats | `context/cache_config.go`, `context/cache.go` | DTO | 数据定义 |
| 2 | CacheProvider 接口 | `context/cache.go` | Strategy | 接口定义 |
| 3 | FileCache 实现 | `context/cache.go` | Factory | 文件存储实现 |
| 4 | config.go 拆分三文件 | `context/session_config.go`, `context/context_config.go`, `context/cache_config.go` | 重构 | 原 config.go 精简 |
| 5 | Holder 集成缓存 | `context/holder.go` | DI | +CacheProvider 字段 +CacheStats()/List()/Clear() |
| 6 | chatLoop 缓存感知 | `context/chat.go` | Template | +缓存检查路径 |
| 7 | CachedStrategy 适配器 | `workplan/cache_strategy.go` | Decorator+Strategy | NodeStrategy 缓存包装 |
| 8 | cache 工具注册函数 | `context/cache_tool.go` | 装配件 | RegisterCacheTools 辅助 |
| 9 | 单元测试 | `context/cache_test.go` | — | 覆盖 80%+ |
| 10 | 集成示例 | `example_Implement/` | — | 缓存命中演示 |

## 测试策略

| 层级 | 目标 | 方法 |
|------|------|------|
| 单元测试 | FileCache Get/Set/Delete/Stats | 临时目录 + 内容验证 |
| 单元测试 | CacheProvider 接口一致性 | 用 mock 测试调用方 |
| 单元测试 | CachedStrategy 命中/未命中 | mock NodeStrategy + 函数注入 |
| 单元测试 | chatLoop 缓存路径 | mock CacheProvider |
| 单元测试 | SHA256 去重 | 相同内容不同key → 同一文件 |
| 竞态测试 | FileCache 并发读写 | go test -race -count=3 |
| TTL 测试 | 过期行为 | 短 TTL + time.Sleep |

## 回滚方案

| 步骤 | 回滚方式 | 影响 |
|------|---------|------|
| 新增文件 | 不删除，不影响编译 | 无 |
| config.go 拆分 | 保留新文件 + config.go re-export | 无 |
| holder.go 修改 | 缓存字段零值时不启用（nil check） | 功能降级 |
| chat.go 修改 | cache==nil 时无缓存行为 | 功能降级 |
| CachedStrategy | 不使用即无影响 | 无 |
| cache 工具 | 不调用 RegisterCacheTools | 无 |

**关键策略**: 所有缓存功能 zero-cost on no-op。无缓存时路径完全不变，无性能退化。
