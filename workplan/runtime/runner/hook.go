package runner

import "time"

// SetNodeHook 设置节点完成回调，透传给 Scheduler。
// 见 seelex plan visualization — 每节点完成时实时回传状态给 TUI。
func (r *Runner) SetNodeHook(hook func(nodeID, kind, status string, elapsed time.Duration)) {
	r.sched.SetNodeHook(hook)
}
