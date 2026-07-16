// Package permission 提供权限配置、检查与审批请求的通用类型。
package permission

import "time"

type Action string

const (
	ActionAllow Action = "allow"
	ActionAsk   Action = "ask"
	ActionDeny  Action = "deny"
)

type PermissionRule struct {
	ToolName string   `yaml:"tool" json:"tool"`
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	Action   Action   `yaml:"action" json:"action"`
}

type PermissionConfig struct {
	Rules []PermissionRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

type ApprovalRequest struct {
	ID        string
	ToolName  string
	Arguments string
	Preview   string
	Risk      string
	Options   []ApproveOption
	Timeout   time.Duration
	SessionID string
}

type ApprovalResponse struct {
	RequestID string
	Choice    string
	Remember  bool
	Timestamp time.Time
}

type ApproveOption struct {
	Key         string
	Label       string
	Description string
	Style       string
}

type ApprovalHandler func(ctx *ApprovalContext) (*ApprovalResponse, error)

func DefaultApproveOptions() []ApproveOption {
	return []ApproveOption{
		{Key: "allow", Label: "允许执行", Description: "执行此操作", Style: "primary"},
		{Key: "always", Label: "始终允许", Description: "记住此选择", Style: "warning"},
		{Key: "deny", Label: "拒绝", Description: "禁止执行此操作", Style: "danger"},
	}
}

type ApprovalContext struct {
	Request  ApprovalRequest
	Response chan<- *ApprovalResponse
}
