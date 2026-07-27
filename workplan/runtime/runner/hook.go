package runner

import (
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/types"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
)

// SetNodeHook 设置节点完成回调，透传给 Scheduler。
// 见 seelex plan visualization — 每节点完成时实时回传状态给 TUI。
func (r *Runner) SetNodeHook(hook func(nr *types.NodeResult)) {
	r.sched.SetNodeHook(hook)
}

// SetBranchEventHook forwards observable branch lifecycle events to Scheduler.
func (r *Runner) SetBranchEventHook(hook func(forkexec.Event)) {
	r.sched.SetBranchEventHook(hook)
}

// SetForkPolicy configures automatic fork failure behavior.
func (r *Runner) SetForkPolicy(policy forkexec.Policy) {
	r.sched.SetForkPolicy(policy)
}

// SetMaxForkConcurrency configures automatic fork parallelism.
func (r *Runner) SetMaxForkConcurrency(maxConcurrent int) {
	r.sched.SetMaxForkConcurrency(maxConcurrent)
}

// SetBranchRuntimeResolver accepts Seelex-injected branch runtime metadata.
func (r *Runner) SetBranchRuntimeResolver(resolver func(string) forkexec.BranchRuntime) {
	r.sched.SetBranchRuntimeResolver(resolver)
}

// Ensure time import is used (Elapsed helper may be referenced elsewhere).
var _ = time.Now
