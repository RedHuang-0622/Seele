package seelectx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ── Cache 工具注册 ──────────────────────────────────────────────────
//
// 本文件提供将缓存查看/管理方法注册为 LLM 可调用工具的函数。
//
// 装配件模式集成：
//   这些工具以 _cache_ 前缀命名，是内部工具（不默认对 LLM 可见）。
//   可通过 PluginManager 的 Include/Exclude 控制可见性：
//
//		// 在 cache 插件中暴露缓存工具
//		pm := tool.NewPluginManager()
//		pm.Define(tool.NewPlugin("cache-view",
//		    "缓存查看工具集",
//		    []string{"_cache_*"},   // Include 匹配所有缓存工具
//		    nil,
//		))
//
// 使用示例：
//
//	import (
//	    "github.com/RedHuang-0622/Seele/seelectx"
//	    "github.com/RedHuang-0622/Seele/provider"
//	)
//
//	cache, _ := seelectx.NewFileCache(seelectx.DefaultCacheConfig())
//	seelectx.RegisterCacheTools(func(name, desc string, schema map[string]any, fn func(ctx, args string) (string, error)) {
//	    engine.RegisterInlineTool(name, desc, schema, fn)
//	}, cache, provider.SchemaOf)

// ToolRegistrar 是工具注册回调的函数签名。
// 调用方提供此函数，将缓存工具注册到自己的工具系统中。
//
// 参数：
//   - name: 工具名称
//   - desc: 工具描述
//   - schema: JSON Schema（由 SchemaOf 或手动构建）
//   - handler: 工具执行函数
type ToolRegistrar func(name, desc string, schema map[string]any, handler func(ctx context.Context, argsJSON string) (string, error))

// SchemaGenerator 是 JSON Schema 生成器的函数签名。
// 对应 provider.SchemaOf。
type SchemaGenerator func(v any) map[string]any

// RegisterCacheTools 将缓存查看/管理方法注册为 LLM 可调用的工具。
//
// 参数：
//   - register: 工具注册回调（通常包装 engine.RegisterInlineTool）
//   - cache: 缓存提供者实例
//   - schemaOf: JSON Schema 生成函数（传 provider.SchemaOf）
//
// 注册的工具：
//   - _cache_stats  — 查看缓存统计信息（命中率、条目数、总大小）
//   - _cache_list   — 列出缓存条目（支持可选前缀过滤）
//   - _cache_get    — 获取指定缓存键的内容
//   - _cache_clear  — 按前缀清理缓存
//
// 所有工具以 _cache_ 前缀命名，LLM 默认不可见，
// 需要通过 PluginManager 的 Include 规则显式暴露。
func RegisterCacheTools(register ToolRegistrar, cache CacheProvider, schemaOf SchemaGenerator) {
	if register == nil || cache == nil {
		return
	}

	// 工具 1: _cache_stats — 缓存统计
	type CacheStatsInput struct{}
	register(
		"_cache_stats",
		"查看缓存统计信息，包括条目数、总大小、命中次数、未命中次数和命中率。",
		schemaOf(CacheStatsInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			stats := cache.Stats()
			b, _ := json.MarshalIndent(stats, "", "  ")
			return string(b), nil
		},
	)

	// 工具 2: _cache_list — 列出缓存条目
	type CacheListInput struct {
		Prefix string `json:"prefix,omitempty" desc:"可选前缀过滤，只返回键名以此前缀开头的条目"`
	}
	register(
		"_cache_list",
		"列出所有缓存条目的键名和元数据。支持可选前缀过滤。",
		schemaOf(CacheListInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input CacheListInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				input.Prefix = ""
			}
			entries := cache.List()
			if input.Prefix != "" {
				filtered := make([]CacheEntry, 0)
				for _, e := range entries {
					if strings.HasPrefix(e.Key, input.Prefix) {
						filtered = append(filtered, e)
					}
				}
				entries = filtered
			}
			if len(entries) == 0 {
				return `{"entries":[],"message":"no cache entries found"}`, nil
			}
			result := map[string]any{
				"entries": entries,
				"total":   len(entries),
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			return string(b), nil
		},
	)

	// 工具 3: _cache_get — 获取缓存值
	type CacheGetInput struct {
		Key string `json:"key" desc:"缓存键名"`
	}
	register(
		"_cache_get",
		"获取指定缓存键的内容。返回该键对应的缓存值以及元数据。",
		schemaOf(CacheGetInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input CacheGetInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return `{"error":"invalid input: key is required"}`, nil
			}
			if input.Key == "" {
				return `{"error":"key is required"}`, nil
			}
			value, ok := cache.Get(input.Key)
			if !ok {
				return fmt.Sprintf(`{"key":%q,"found":false,"message":"cache miss"}`, input.Key), nil
			}
			entry, _ := cache.GetEntry(input.Key)
			result := map[string]any{
				"key":    input.Key,
				"found":  true,
				"value":  value,
				"length": len(value),
			}
			if entry != nil {
				result["created_at"] = entry.CreatedAt
				if !entry.ExpiresAt.IsZero() {
					result["expires_at"] = entry.ExpiresAt
				}
				result["hit_count"] = entry.HitCount
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			return string(b), nil
		},
	)

	// 工具 4: _cache_clear — 按前缀清理缓存
	type CacheClearInput struct {
		Prefix string `json:"prefix" desc:"要清理的缓存键前缀，空字符串表示清空所有缓存"`
	}
	register(
		"_cache_clear",
		"按前缀清理缓存。prefix 为空串时清空所有缓存。返回清理的条目数。",
		schemaOf(CacheClearInput{}),
		func(ctx context.Context, argsJSON string) (string, error) {
			var input CacheClearInput
			if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
				return `{"error":"invalid input"}`, nil
			}
			var deleted int
			if input.Prefix == "" {
				deleted = cache.ClearAll()
			} else {
				deleted = cache.ClearByPrefix(input.Prefix)
			}
			result := map[string]any{
				"deleted":    deleted,
				"prefix":     input.Prefix,
				"message":    fmt.Sprintf("cleared %d cache entries", deleted),
			}
			b, _ := json.MarshalIndent(result, "", "  ")
			return string(b), nil
		},
	)
}
