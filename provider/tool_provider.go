package provider

import (
	"context"

	types "github.com/sukasukasuka123/Seele/types"
)

// ToolProvider 是工具来源的统一抽象。
//
// 设计原则：
//   - 实现方只负责"提供工具"和"执行工具"，不感知 LLM、历史、会话
//   - Runtime 通过此接口聚合多个来源，Agent 完全不接触该接口
//
// 已有实现：
//   - HubProvider   —— 封装现有 microHub + registry 逻辑
//   - MCPProvider   —— 通过 MCP 协议连接外部工具服务器
type ToolProvider interface {
	// ProviderName 返回唯一标识符，用于日志和 Retire/Restore 路由
	ProviderName() string

	// Tools 返回当前可用工具列表（格式：OpenAI function calling schema）
	// 每次 LLM 调用前都会调用此方法，实现应保证热更新
	Tools() []types.Tool

	// Dispatch 执行指定工具，返回结果 JSON 字符串
	// name 为工具名（与 Tools() 中 Function.Name 一致）
	// argsJSON 为 LLM 生成的参数 JSON 字符串，已通过 json.Valid 校验
	Dispatch(ctx context.Context, name, argsJSON string) (string, error)

	// HasTool 判断此 provider 是否包含指定工具名
	// Runtime 用此方法做路由，避免遍历 Tools() 切片
	HasTool(name string) bool
}
