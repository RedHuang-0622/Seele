package holder

import "time"

// HolderConfig 工具持有者的可调参数。
type HolderConfig struct {
	DispatchRetries    int
	DispatchRetryDelay time.Duration
	ToolCallTimeout    time.Duration // 单次工具调用超时，0=不限制
}

// DefaultHolderConfig 返回推荐的 Holder 配置。
func DefaultHolderConfig() HolderConfig {
	return HolderConfig{
		DispatchRetries:    3,
		DispatchRetryDelay: 2 * time.Second,
		ToolCallTimeout:    30 * time.Second,
	}
}

func (c HolderConfig) Effective() HolderConfig {
	d := DefaultHolderConfig()
	if c.DispatchRetries <= 0 {
		c.DispatchRetries = d.DispatchRetries
	}
	if c.DispatchRetryDelay <= 0 {
		c.DispatchRetryDelay = d.DispatchRetryDelay
	}
	if c.ToolCallTimeout <= 0 {
		c.ToolCallTimeout = d.ToolCallTimeout
	}
	return c
}
