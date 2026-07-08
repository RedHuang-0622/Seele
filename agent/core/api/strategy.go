// Package api 提供 LLM API 客户端抽象。
//
// ProviderStrategy 处理 LLM API 传输层协议差异。
// ChatClient 负责 HTTP 编排（超时、重试、连接池），策略只处理格式转换。
package api

import (
	"sync"

	"github.com/RedHuang-0622/Seele/types"
)

// SSEEventType 流式事件类型（传输层协议无关）。
type SSEEventType int

const (
	SSEEventText     SSEEventType = iota // 文本 delta
	SSEEventToolCall                     // 工具调用帧
	SSEEventReasoning                    // 推理内容
	SSEEventDone                         // 流结束
	SSEEventError                        // 错误
)

// SSEEvent 是传输层协议解析后的结构化事件。
// ChatClient 的 SSE read loop 将原始帧通过 ParseSSEEvent 转换为 SSEEvent 切片，
// 再根据事件类型做通用处理（文本 → onChunk，tool_call → 累积等）。
type SSEEvent struct {
	Type          SSEEventType
	Content       string
	ToolCallIndex int
	Meta          map[string]any
}

// ProviderStrategy 处理 LLM API 传输层协议差异。
//
// ChatClient 负责 HTTP 生命周期管理（超时、重试、连接池），
// 策略只处理以下六个维度的协议差异：
//
//  1. BuildRequest  — 请求体序列化（不同 provider 的 JSON 结构不同）
//  2. ParseResponse — 响应体反序列化为 types.Message
//  3. ParseSSEEvent — SSE data 帧解析为结构化事件
//  4. Endpoint      — API 路径（/chat/completions vs /v1/messages）
//  5. AuthHeader    — 认证方式（Bearer vs x-api-key）
//  6. SSEHeaders    — 流式请求的额外头部
type ProviderStrategy interface {
	// Name 返回策略名称，与 Account.ProviderType 对应。
	// 例如 "openai"、"anthropic"、"gemini"。
	Name() string

	// Endpoint 返回 API 相对路径，如 "/chat/completions"。
	Endpoint() string

	// BuildRequest 构建请求体字节序列。
	// stream 为 true 时应在请求体中设置流式标记。
	BuildRequest(model string, messages []types.Message, tools []types.Tool, stream bool) ([]byte, error)

	// ParseResponse 解析同步响应的 JSON body，返回 types.Message。
	ParseResponse(body []byte) (types.Message, error)

	// SSEHeaders 返回流式请求的额外 HTTP 头部。
	// 至少应包含 Accept: text/event-stream。
	SSEHeaders() map[string]string

	// ParseSSEEvent 解析单个 SSE 帧。
	// eventType 为事件类型（如 "data"），payload 为事件体字符串。
	// 返回结构化事件切片（一帧可能包含多个事件，如并发 tool_calls），
	// 无法解析的帧返回空切片。
	ParseSSEEvent(eventType string, payload string) ([]SSEEvent, error)

	// AuthHeader 返回认证头部键值对。
	// OpenAI → ("Authorization", "Bearer sk-xxx")
	// Anthropic → ("x-api-key", "sk-ant-xxx")
	AuthHeader(apiKey string) (string, string)
}

// ---------------------------------------------------------------------------
// 全局策略注册表
// ---------------------------------------------------------------------------

var (
	providerStrategies   = map[string]ProviderStrategy{}
	providerStrategiesMu sync.RWMutex
)

// RegisterProviderStrategy 注册一个 ProviderStrategy。
// name 必须唯一，重复注册会 panic。
func RegisterProviderStrategy(s ProviderStrategy) {
	providerStrategiesMu.Lock()
	defer providerStrategiesMu.Unlock()
	name := s.Name()
	if _, dup := providerStrategies[name]; dup {
		panic("api: ProviderStrategy already registered: " + name)
	}
	providerStrategies[name] = s
}

// GetProviderStrategy 按名称获取已注册的 ProviderStrategy。
// 未找到时返回 nil。
func GetProviderStrategy(name string) ProviderStrategy {
	providerStrategiesMu.RLock()
	defer providerStrategiesMu.RUnlock()
	return providerStrategies[name]
}

// ProviderStrategyNames 返回所有已注册的策略名称。
func ProviderStrategyNames() []string {
	providerStrategiesMu.RLock()
	defer providerStrategiesMu.RUnlock()
	names := make([]string, 0, len(providerStrategies))
	for n := range providerStrategies {
		names = append(names, n)
	}
	return names
}
