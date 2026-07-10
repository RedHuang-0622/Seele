package workplan

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// =============================================================================
// CachedStrategy —— NodeStrategy 的缓存装饰器
// =============================================================================
//
// CachedStrategy 包装内层 NodeStrategy，在执行前查缓存，命中直接返回。
// 通过函数注入（cacheGetter / cacheSetter）而非接口类型，避免循环依赖：
//
//	context 包 ←┐  (无依赖)
//	           │
//	workplan ──┘  (通过闭包注入 CacheProvider 能力)
//
// 典型用法：
//
//	cache := seelectx.NewFileCache(seelectx.DefaultCacheConfig())
//	wp.Strategy("cached-llm", workplan.NewCachedStrategy(
//	    workplan.NewLLMStrategy(factory, "prompt"),
//	    workplan.WithCacheGetter(func(key string) (string, bool) {
//	        return cache.Get(key)
//	    }),
//	    workplan.WithCacheSetter(func(key, value string, ttl time.Duration) {
//	        cache.SetWithTTL(key, value, ttl)
//	    }),
//	    workplan.WithCacheTTL(10*time.Minute),
//	    workplan.WithCacheKeyPrefix("llm"),
//	))

// CachedStrategy 包装一个内层 NodeStrategy，提供透明缓存能力。
//
// 工作流程：
//  1. Execute 被调用时，根据 input + ec.PrevOutput 构建缓存键（SHA256）
//  2. 通过 cacheGetter 查询缓存
//  3. 命中 → 直接返回缓存结果（零 LLM / 零函数调用）
//  4. 未命中 → 执行内层策略 → 通过 cacheSetter 缓存结果 → 返回
type CachedStrategy struct {
	inner       NodeStrategy
	cacheGetter func(key string) (value string, ok bool)
	cacheSetter func(key, value string, ttl time.Duration)
	keyPrefix   string
	ttl         time.Duration
}

// CachedStrategyOption 配置 CachedStrategy 的函数选项。
type CachedStrategyOption func(*CachedStrategy)

// WithCacheGetter 设置缓存读取函数。
// 函数签名：接收缓存键，返回 (缓存值, 是否命中)。
func WithCacheGetter(fn func(key string) (string, bool)) CachedStrategyOption {
	return func(s *CachedStrategy) { s.cacheGetter = fn }
}

// WithCacheSetter 设置缓存写入函数。
// 函数签名：接收缓存键、缓存值、TTL。
func WithCacheSetter(fn func(key, value string, ttl time.Duration)) CachedStrategyOption {
	return func(s *CachedStrategy) { s.cacheSetter = fn }
}

// WithCacheTTL 设置缓存项的 TTL。默认 5 分钟。
func WithCacheTTL(ttl time.Duration) CachedStrategyOption {
	return func(s *CachedStrategy) { s.ttl = ttl }
}

// WithCacheKeyPrefix 设置缓存键的前缀，用于区分不同策略的缓存。
func WithCacheKeyPrefix(prefix string) CachedStrategyOption {
	return func(s *CachedStrategy) { s.keyPrefix = prefix }
}

// NewCachedStrategy 创建缓存包装策略。
//
// inner 是被包装的内层策略（LLMStrategy / AgentStrategy / MethodStrategy 或自定义）。
// opts 是可选的函数选项配置。
//
// 至少需要设置 cacheGetter（只读缓存）或 cacheSetter（只写缓存）。
func NewCachedStrategy(inner NodeStrategy, opts ...CachedStrategyOption) *CachedStrategy {
	s := &CachedStrategy{
		inner:     inner,
		keyPrefix: "cached",
		ttl:       5 * time.Minute,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Execute 执行包装后的策略：查缓存 → 命中则返回 → 否则执行内层策略并缓存。
func (s *CachedStrategy) Execute(ctx context.Context, ec *ExecutionContext) (string, error) {
	key := s.buildKey(ec)

	// 缓存命中
	if s.cacheGetter != nil {
		if cached, ok := s.cacheGetter(key); ok {
			return cached, nil
		}
	}

	// 未命中：执行内层策略
	result, err := s.inner.Execute(ctx, ec)
	if err != nil {
		return "", err
	}

	// 缓存结果
	if s.cacheSetter != nil {
		s.cacheSetter(key, result, s.ttl)
	}

	return result, nil
}

// buildKey 构建缓存键：SHA256(keyPrefix + prevOutput)。
func (s *CachedStrategy) buildKey(ec *ExecutionContext) string {
	h := sha256.New()
	h.Write([]byte(s.keyPrefix))
	h.Write([]byte{0}) // 分隔符
	if ec != nil {
		h.Write([]byte(ec.PrevOutput))
	}
	return fmt.Sprintf("%s:%x", s.keyPrefix, h.Sum(nil))
}
