// Package permission 提供权限配置、检查与审批请求的通用类型。
//
// 除扁平的 allow/ask/deny 规则外，配置还可以表达 Linux 式权限模型：
// 主体（Subject）× 路由组（PermissionGroup）× 位（r/w/x）+ sudo。所有新增
// 字段都是可选的，零值等价于旧的纯规则行为。
package permission

import (
	"context"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

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

// 位常量与 tools.BitRead/BitWrite/BitExecute 同源：位模型在 tools 包定义
// （ToolMeta.Bits 使用它），这里重导出为权限层可直接使用的数值，避免漂移。
const (
	BitRead    uint8 = roottools.BitRead
	BitWrite   uint8 = roottools.BitWrite
	BitExecute uint8 = roottools.BitExecute
)

// Subject 是不透明的主体标识（用户、组、进程或子代理）。空串表示匿名 /
// 进程级主体，与旧 Check 的行为完全一致。
type Subject string

const (
	// SubjectAnonymous 是未解析到具体主体时的标识，等价于旧 Check 语义。
	SubjectAnonymous Subject = ""
	// SubjectRoot 是控制类工具默认可路由的主体。
	SubjectRoot Subject = "root"
)

// SudoMode 描述主体在某路由组上被拒后允许的提权方式。
type SudoMode string

const (
	SudoNone     SudoMode = "none"
	SudoPassword SudoMode = "password"
	SudoNopasswd SudoMode = "nopasswd"
)

// GrantBit 是某主体在某路由组上被授予的位与资源范围。
// Resources 为空表示不作资源限制。
type GrantBit struct {
	Bits      uint8
	Resources []string
}

// SubjectGrant 汇总一个主体的所有授权。
type SubjectGrant struct {
	Bits map[string]GrantBit // group name -> grant
	Sudo SudoMode
}

// PermissionGroup 把一个路由组名映射到工具名 glob，并声明该组所需位、
// 默认动作与资源。名字路由取最后一个命中组（LMRW，与 Rules 一致）。
type PermissionGroup struct {
	Name     string
	Match    []string // 工具名 glob
	Mode     uint8    // 组所需位（r=4 w=2 x=1）
	Default  Action   // 组默认动作；空 = 不覆盖（保持 ask）
	Resource string   // "project" | "desktop" | ""
}

type PermissionRule struct {
	ToolName string   `yaml:"tool" json:"tool"`
	Patterns []string `yaml:"patterns,omitempty" json:"patterns,omitempty"`
	Action   Action   `yaml:"action" json:"action"`

	// Bits 可选：非 nil 时该规则仅对拥有这些位的主体生效；nil 表示旧语义
	// （规则对所有主体一视同仁）。位相对被路由到的组的授权判定。
	Bits *uint8 `yaml:"bits,omitempty" json:"bits,omitempty"`
}

// PermissionConfig 是权限门控的配置入口。
//
// 求值顺序：先按工具名把调用路由到 PermissionGroup（名字 glob，多组命中取
// 最后一个），再判定主体是否持有该组所需位，最后套用 Rules（Rules 永远最后、
// 最细，覆盖组默认）。
//
// 所有新增字段都可选：Groups/Subjects 为空时退化为纯 Rules 的旧行为。
type PermissionConfig struct {
	Mode  Mode             `yaml:"mode,omitempty" json:"mode,omitempty"`
	Rules []PermissionRule `yaml:"rules,omitempty" json:"rules,omitempty"`

	// Groups 是路由组：只写 Groups 不写 Rules 即可表达分组默认动作。
	Groups []PermissionGroup `yaml:"groups,omitempty" json:"groups,omitempty"`

	// Subjects 是主体授权表。非空时启用 Linux 式位检查与可见性判定；
	// 为空时跳过位检查（纯规则行为）。
	Subjects map[Subject]SubjectGrant `yaml:"subjects,omitempty" json:"subjects,omitempty"`

	// MissingBit 是主体缺少组所需位时采用的动作；零值（空）等价于 ask。
	MissingBit Action `yaml:"missing_bit,omitempty" json:"missing_bit,omitempty"`
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

// 提权范围。空串等价于 ScopeOnce，保持旧行为。
const (
	ScopeOnce    = "once"    // 仅本次调用
	ScopeTool    = "tool"    // 当前工具（不限参数）
	ScopeSession = "session" // 同一 subject+group 内的后续调用
	ScopeArgs    = "args"    // 当前工具 + 具体参数
)

type ApprovalResponse struct {
	RequestID string
	Choice    string
	Remember  bool
	Timestamp time.Time

	// Scope 可选：本次放行的提权范围。空 = once（旧行为）。
	Scope string
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
	// Context 是本次调用携带的 context：框架只透传、不做解释，harness 可据此读取
	// 会话 id / 追踪器 / 截止时间（配合 WithSessionID、EngineFromContext 使用）。
	Context context.Context

	Request  ApprovalRequest
	Response chan<- *ApprovalResponse
}

// ElevationEvent 记录一次提权，供审计使用。
type ElevationEvent struct {
	Subject Subject
	Group   string
	Tool    string
	Reason  string
	Scope   string
	Granted bool
	At      time.Time
}

// ElevationAuditor 接收每一次提权事件。框架不解释事件，由调用方落盘或上报。
type ElevationAuditor func(ElevationEvent)

// SubjectResolver 从 context 解析当前主体。未安装时主体为 SubjectAnonymous。
type SubjectResolver func(ctx context.Context) Subject

// BitEnforcer 是位与沙箱 / 路径的挂载点：框架只调用它，本身不包含任何路径
// 或命令解析逻辑。ok=false 表示框架不干预，行为与未安装时一致。
type BitEnforcer interface {
	Enforce(ctx context.Context, subject Subject, meta roottools.ToolMeta, toolName, argsJSON string) (Action, bool)
}
