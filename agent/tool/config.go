package tool

import "time"

// HolderConfig 工具持有者的可调参数。
// 零值字段使用 DefaultHolderConfig() 中的默认值。
type HolderConfig struct {
	// DispatchRetries 瞬时错误（ErrToolUnavailable）的最大重试次数。默认 3。
	DispatchRetries int

	// DispatchRetryDelay 重试间隔。默认 2s。
	DispatchRetryDelay time.Duration
}

// DefaultHolderConfig 返回推荐的 Holder 配置。
func DefaultHolderConfig() HolderConfig {
	return HolderConfig{
		DispatchRetries:    3,
		DispatchRetryDelay: 2 * time.Second,
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c HolderConfig) Effective() HolderConfig {
	d := DefaultHolderConfig()
	if c.DispatchRetries <= 0 {
		c.DispatchRetries = d.DispatchRetries
	}
	if c.DispatchRetryDelay <= 0 {
		c.DispatchRetryDelay = d.DispatchRetryDelay
	}
	return c
}
