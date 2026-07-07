package seelectx

// SessionConfig 会话级别的可调参数。
// 零值字段使用 DefaultSessionConfig() 中的默认值。
type SessionConfig struct {
	// MaxLoops 单次 Chat 调用中最多允许的 tool_call 循环次数。默认 4。
	MaxLoops int

	// MaxConcurrentDispatch 单次 tool_calls 批量的最大并发 goroutine 数。默认 5。
	MaxConcurrentDispatch int

	// MaxApprovalLoops 嵌套审批的最大循环层数。默认 10。
	MaxApprovalLoops int

	// ContextCfg 上下文管理配置。零值时使用 DefaultContextConfig()。
	ContextCfg ContextConfig

	// CacheCfg 缓存配置。零值时使用 DefaultCacheConfig()。
	CacheCfg CacheConfig
}

// DefaultSessionConfig 返回推荐的会话配置。
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		MaxLoops:               4,
		MaxConcurrentDispatch: 5,
		MaxApprovalLoops:      10,
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c SessionConfig) Effective() SessionConfig {
	d := DefaultSessionConfig()
	if c.MaxLoops <= 0 {
		c.MaxLoops = d.MaxLoops
	}
	if c.MaxConcurrentDispatch <= 0 {
		c.MaxConcurrentDispatch = d.MaxConcurrentDispatch
	}
	if c.MaxApprovalLoops <= 0 {
		c.MaxApprovalLoops = d.MaxApprovalLoops
	}
	c.ContextCfg = c.ContextCfg.Effective()
	c.CacheCfg = c.CacheCfg.Effective()
	return c
}
