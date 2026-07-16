// Package permission 提供权限配置、检查与审批请求的通用类型。
//
// 架构：
//
//	PermissionConfig → PermissionChecker → check(工具名, 参数) → allow / ask / deny
//	                                          ↓ "ask"
//	                                   ApprovalRequest → ApprovalHandler → ApprovalResponse
//
// 这是一个横切关注点（基础设施层），不依赖任何 UI 框架。
package permission

import "time"

// ─── 权限动作 ──────────────────────────────────────────────────────

// Action 是权限检查的结果动作。
type Action string

const (
	ActionAllow Action = "allow" // 允许执行
	ActionAsk   Action = "ask"   // 需要用户审批
	ActionDeny  Action = "deny"  // 拒绝执行
)

// ─── 配置层 ────────────────────────────────────────────────────────

// PermissionRule 定义一条权限规则。
// 规则按顺序评估，最后匹配的规则胜出。
type PermissionRule struct {
	// ToolName 是工具名的 glob 模式，例如 "bash", "edit", "read_file", "git_*"
	ToolName string `yaml:"tool" json:"tool"`

	// Patterns 是参数字符串的 glob 模式列表（可选）。
	// 空列表表示匹配该工具的所有调用。
	// 示例：["git *", "npm install *"]
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`

	// Action 指定匹配时的处理动作。
	Action Action `yaml:"action" json:"action"`
}

// PermissionConfig 是权限配置的顶层结构。
type PermissionConfig struct {
	Rules []PermissionRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// ─── 运行时审批 ────────────────────────────────────────────────────

// ApprovalRequest 是审批请求（Permission Gate 发往 UI）。
type ApprovalRequest struct {
	ID        string          // 唯一请求 ID
	ToolName  string          // 工具名
	Arguments string          // JSON 参数字符串
	Preview   string          // 人类可读的预览（如 "rm -rf /tmp/cache"）
	Risk      string          // "low" | "medium" | "high"
	Options   []ApproveOption // 用户选项
	Timeout   time.Duration   // 超时（0 = 无超时）
	SessionID string          // 所属会话
}

// ApprovalResponse 是审批响应（UI 返回 Permission Gate）。
type ApprovalResponse struct {
	RequestID string    // 对应的请求 ID
	Choice    string    // 用户选择的 key
	Remember  bool      // 是否记住此选择
	Timestamp time.Time // 响应时间
}

// ApproveOption 是审批选项。
type ApproveOption struct {
	Key         string // 选项标识
	Label       string // 显示标签
	Description string // 描述
	Style       string // "primary" | "secondary" | "danger" | "warning"
}

// ApprovalHandler 是 UI 层注入的审批回调。
// Permission Gate 在需要用户确认时调用此函数。
type ApprovalHandler func(ctx *ApprovalContext) (*ApprovalResponse, error)

// ─── 标准审批选项 ────────────────────────────────────────────────

// DefaultApproveOptions 返回默认的审批选项。
func DefaultApproveOptions() []ApproveOption {
	return []ApproveOption{
		{Key: "allow", Label: "允许执行", Description: "执行此操作", Style: "primary"},
		{Key: "always", Label: "始终允许", Description: "记住此选择，后续自动执行", Style: "warning"},
		{Key: "deny", Label: "拒绝", Description: "禁止执行此操作", Style: "danger"},
	}
}

// ─── 审批上下文（桥接阻塞等待） ──────────────────────────────────

// ApprovalContext 包装审批请求和响应通道，供 UI 层实现审批回调。
type ApprovalContext struct {
	Request  ApprovalRequest
	Response chan<- *ApprovalResponse
}
