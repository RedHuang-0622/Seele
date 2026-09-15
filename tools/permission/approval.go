package permission

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

// DefaultApprovalTimeout 是未显式配置时的审批超时：请求呈现在执行选择页面上等人
// 回答，超时即视为拒绝（与拒绝同义，不产生任何提权）。
const DefaultApprovalTimeout = 2 * time.Minute

var approvalSeq uint64

// NewApprovalRequest 组装一次审批请求，把呈现所需的信息填齐：
//
//	ID        全局自增唯一，供 harness 关联「请求 ↔ 回执」（ApprovalResponse.RequestID）
//	Preview   截断后的调用预览
//	Risk      由簇属（ToolMeta.Kind）推断，未声明时按名回退
//	SessionID 从 ctx 读取（WithSessionID 注入）
//	Timeout   <=0 时取 DefaultApprovalTimeout
//
// 框架只负责「信息齐备」；UI、超时策略与提权范围仍由 harness 决定。
func NewApprovalRequest(ctx context.Context, name string, meta roottools.ToolMeta, argsJSON string, timeout time.Duration) ApprovalRequest {
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	return ApprovalRequest{
		ID:        nextApprovalID(),
		ToolName:  name,
		Arguments: argsJSON,
		Preview:   FormatPreview(name, argsJSON),
		Risk:      RiskOf(name, meta),
		Options:   DefaultApproveOptions(),
		Timeout:   timeout,
		SessionID: SessionIDFromContext(ctx),
	}
}

// nextApprovalID 生成审批请求 id：时间前缀保证跨进程大致有序，自增序号保证同进程唯一。
func nextApprovalID() string {
	return fmt.Sprintf("appr-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&approvalSeq, 1))
}

// RiskOf 由簇属推断风险等级（high/medium/low）；工具未声明 Kind 时按名回退。
func RiskOf(name string, meta roottools.ToolMeta) string {
	switch meta.Kind {
	case roottools.ToolKindControl, roottools.ToolKindAdmin:
		return "high"
	case roottools.ToolKindWrite:
		return "medium"
	case roottools.ToolKindRead:
		return "low"
	}
	return assessRisk(name)
}

// assessRisk 是无簇属时的按名回退（保持旧行为）。
func assessRisk(name string) string {
	switch name {
	case "bash", "shell_exec", "exec":
		return "high"
	case "edit", "write_file", "create_file", "delete", "rename", "rm", "mv":
		return "medium"
	default:
		return "low"
	}
}

// FormatPreview 生成不超过 80 字符的调用预览（工具名 + 参数）。
func FormatPreview(name, args string) string {
	s := name + "("
	if len(args) > 80 {
		s += args[:80] + "..."
	} else {
		s += args
	}
	return s + ")"
}
