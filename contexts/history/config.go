// Package history 提供 LLM 上下文预算管理。
//
// 核心能力：
//   - Token 估算（EstimateTokens / EstimateHistoryTokens）
//   - 硬截断（TrimHistory）
//   - LLM 压缩（CompressHistory）
//   - 工具结果截断（TruncateToolResult）
//
// 依赖：types.Message（LLM 消息类型），仅此而已。
package history

// ── Config ─────────────────────────────────────────────────────────

// Config 控制上下文管理的各项阈值。
// 零值字段使用 DefaultConfig() 中的默认值。
type Config struct {
	// MaxTokens 硬上限：单次 LLM 请求的最大上下文 token 数。
	MaxTokens int

	// CompressThreshold 压缩触发阈值：history token 数超过此值时触发压缩。
	// 通常设为 MaxTokens 的 75%。
	CompressThreshold int

	// MaxToolResultChars 单个 tool 结果追加到 history 前的最大字符数。
	// 超过此长度则截断，末尾追加 "[truncated]" 标记让 LLM 感知到裁剪。
	MaxToolResultChars int
}

// DefaultConfig 返回推荐的上下文配置。
// 默认值针对主流 LLM（8K 上下文窗口）设计：
//
//	MaxTokens:           8192
//	CompressThreshold:   6144 (75%)
//	MaxToolResultChars:  4000
func DefaultConfig() Config {
	return Config{
		MaxTokens:           8192,
		CompressThreshold:   6144,
		MaxToolResultChars:  4000,
	}
}

// Effective 返回实际生效的配置，零值字段用默认值填充。
func (c Config) Effective() Config {
	d := DefaultConfig()
	if c.MaxTokens <= 0 {
		c.MaxTokens = d.MaxTokens
	}
	if c.CompressThreshold <= 0 {
		c.CompressThreshold = d.CompressThreshold
	}
	if c.MaxToolResultChars <= 0 {
		c.MaxToolResultChars = d.MaxToolResultChars
	}
	return c
}
