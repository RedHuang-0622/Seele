// Package permission 提供权限配置、检查与审批请求的通用类型。
package permission

import "time"

// Mode 控制权限门控的整体行为。
// full_access：所有工具静默放行，不弹审批。
// manual：命中 allow 规则放行，命中 deny 规则拒绝，命中 ask 或无匹配则弹审批。
type Mode string

const (
	ModeFullAccess Mode = "full_access"
	ModeManual     Mode = "manual"
)

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

// PermissionConfig 是权限门控的配置入口。
// Mode 决定无规则命中时的默认行为；Rules 提供细粒度覆盖。
type PermissionConfig struct {
	Mode  Mode             `yaml:"mode,omitempty" json:"mode,omitempty"`
	Rules []PermissionRule `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// EffectiveMode 返回生效的 Mode，空值默认为 full_access（与现有行为兼容）。
func (cfg PermissionConfig) EffectiveMode() Mode {
	if cfg.Mode == "" {
		return ModeFullAccess
	}
	return cfg.Mode
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
