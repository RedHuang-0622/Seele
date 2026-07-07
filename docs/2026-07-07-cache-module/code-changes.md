# 编码变更报告

## 摘要
实现缓存模块 + context 架构优化。方案 A：全内聚 context 包 + CachedStrategy 装饰器。

## 文件清单

### 新增文件（6个）

| 文件 | 说明 | 行数 |
|------|------|------|
| `context/cache.go` | CacheProvider 接口 + FileCache 实现（内容寻址 SHA256 + TTL + sync.Map 内存索引 + atomic 统计） | ~410 |
| `context/cache_config.go` | CacheConfig + DefaultCacheConfig + Effective | ~40 |
| `context/session_config.go` | SessionConfig 从 config.go 拆分独立 | ~50 |
| `context/context_config.go` | ContextConfig 从 history.go 拆分独立 | ~60 |
| `context/cache_tool.go` | RegisterCacheTools 装配件模式工具注册（4 个 \_cache\_\* 工具） | ~170 |
| `workplan/cache_strategy.go` | CachedStrategy：NodeStrategy 缓存装饰器（函数注入解环） | ~90 |

### 修改文件（3个）

| 文件 | 变更 | 行数 |
|------|------|------|
| `context/holder.go` | +CacheProvider 字段 + NewWithCache + 7 个缓存公开方法 | +80 |
| `context/chat.go` | chatLoop 缓存检查（输入 SHA256 → 缓存查询 → 命中跳过 LLM） | +15 |
| `context/config.go` | 精简为包文档入口 | -60 |

### 新增测试文件（1个）

| 文件 | 说明 | 测试数 |
|------|------|--------|
| `context/cache_test.go` | FileCache CRUD / TTL / 去重 / 统计 / 并发 / 持久化 / 边界 | 15 tests |

## 架构变更

### 旧架构
```
context/
├── config.go     # SessionConfig + ContextConfig (混合)
├── holder.go     # Holder (无缓存)
├── chat.go       # chatLoop (无缓存)
├── storage.go    # LocalStorage (JSON only)
└── history.go    # ContextConfig + 压缩逻辑
```

### 新架构
```
context/
├── cache.go          # [NEW] CacheProvider + FileCache (内容寻址 SHA256 + TTL)
├── cache_config.go   # [NEW] CacheConfig
├── cache_tool.go     # [NEW] RegisterCacheTools (装配件模式工具注册)
├── session_config.go # [NEW] SessionConfig (从 config.go 拆分)
├── context_config.go # [NEW] ContextConfig (从 history.go 拆分)
├── config.go         # [MOD] 精简为包文档入口
├── holder.go         # [MOD] +CacheProvider 字段 + 7 个公开方法
├── chat.go           # [MOD] +缓存检查 (SHA256 输入 → 跳过 LLM 调用)
├── storage.go        # [UNCHANGED] 保留原始 LocalStorage
└── history.go        # [MOD] 移除 ContextConfig (已迁至 context_config.go)

workplan/
├── cache_strategy.go # [NEW] CachedStrategy (NodeStrategy 装饰器, 函数注入)
```

## 关键设计决策

| 决策 | 选型 | 理由 |
|------|------|------|
| 缓存键策略 | SHA256(userInput) | 简单确定，相同输入命中相同缓存 |
| 存储结构 | baseDir/{sha256(content)}/content + baseDir/{sha256(content)}/meta.json | 内容去重：相同内容共享同一文件 |
| 内存索引 | sync.Map (key → *cacheIndexEntry) | O(1) 查询，并发安全 |
| 统计 | atomic.Int64 (hits/misses + totalSize) | 无锁读路径 |
| TTL 过期 | 惰性删除 (Get 时检查) | 无后台 GC goroutine |
| 循环依赖解环 | CachedStrategy 通过函数注入 cacheGetter/cacheSetter | workplan 包不 import seelectx |
| 工具注册 | RegisterCacheTools 接收 ToolRegistrar + SchemaGenerator 函数参数 | context 包不 import agent/tool 或 provider |

## 测试结果

- context/ 包: ✅ 15 tests, race clean, `go vet` clean
- workplan/ 包: ✅ All existing tests pass, race clean, `go vet` clean
- 覆盖率: ~85% core paths covered (CRUD + TTL + stats + persistence + concurrent)
