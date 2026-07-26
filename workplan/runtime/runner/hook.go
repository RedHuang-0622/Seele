package runner

import (
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

// SetNodeHook 设置节点完成回调，透传给 Scheduler。
// 见 seelex plan visualization — 每节点完成时实时回传状态给 TUI。
func (r *Runner) SetNodeHook(hook func(nr *types.NodeResult)) {
	r.sched.SetNodeHook(hook)
}

// Ensure time import is used (Elapsed helper may be referenced elsewhere).
var _ = time.Now
