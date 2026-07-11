package engine

// SessionConfig 配置引擎的 ReAct 循环参数。
type SessionConfig struct {
	// MaxLoops 单次 Chat 调用中最多允许的 tool_call 循环次数。默认 10。
	MaxLoops int

	// MaxToolResultChars 工具调用结果的最大字符数。默认 4000。
	MaxToolResultChars int
}

// DefaultSessionConfig 返回推荐的默认配置。
func DefaultSessionConfig() SessionConfig {
	return SessionConfig{
		MaxLoops:           25,
		MaxToolResultChars: 4000,
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c SessionConfig) Effective() SessionConfig {
	d := DefaultSessionConfig()
	if c.MaxLoops <= 0 {
		c.MaxLoops = d.MaxLoops
	}
	if c.MaxToolResultChars <= 0 {
		c.MaxToolResultChars = d.MaxToolResultChars
	}
	return c
}
