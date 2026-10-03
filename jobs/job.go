// Package jobs defines Seele's product-neutral asynchronous-job contract.
//
// A "job" is one long-running unit of work that is *dispatched* and then
// *observed / fetched / killed / retired* through a handle. The framework owns
// the contract, the bookkeeping table, the state machine and the generic
// management tool (jobs_manage); it never interprets what a job actually does.
// Concrete work is supplied by an Executor that the host product registers.
//
// Boundary (see docs/arch/1x-jobs-contracts.md):
//
//   - this package must not import any product package (e.g. seelex);
//   - dispatch-side tools (bash_bg / read_batch / fork_subagents) live in the
//     product and call Manager.Dispatch; they are not part of the framework.
//
// The contract mirrors the invariants already proven by Seelex's async job
// face (seelebridge/tools/job_contract.go): a handle is unique and never
// persisted, output is bounded and consumed through an offset cursor,
// cross-scope access is refused, and finalization happens exactly once.
package jobs

import (
	"encoding/json"
	"errors"
	"time"
)

// Kind is an open string that classifies a job. The framework does not
// interpret concrete values beyond the two it ships; a product registers its
// own kinds (e.g. "worker", "seat") together with an Executor for each.
type Kind string

const (
	// KindProcess is an external process: the process tree is terminable and
	// output is written to a bounded log file.
	KindProcess Kind = "process"
	// KindInline is an in-process fan-out (e.g. batch reads): there is no
	// process, cancellation runs through the context.
	KindInline Kind = "inline"
)

// State is the lifecycle state of one job. Terminal states are exactly
// StateDone / StateFailed / StateKilled; StateRunning is the only live one.
type State string

const (
	StateRunning State = "running"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateKilled  State = "killed"
)

// Terminal reports whether the state is one of the three final states.
func (s State) Terminal() bool {
	switch s {
	case StateDone, StateFailed, StateKilled:
		return true
	default:
		return false
	}
}

// Handle identifies one dispatched job for its whole in-memory lifetime.
// Handles are never persisted (invariant I-4): a persisted handle would leave a
// permanently "running" ghost row after a restart.
type Handle string

// Scope is a job's isolation and reclamation scope. It is deliberately two
// parallel fields - never a concatenated string - so that no escaping or
// collision rules are invented for characters that both session ids and role
// names may contain.
//
//   - Session: session key; same origin as a product's per-session job table.
//     Isolation + reclamation granularity (a destroyed session kills its jobs).
//   - Subject: subject/group dimension within a session (e.g. "emp_<role>" for
//     a teammate); empty means "the main agent".
//
// Both fields take part in authorization: fetch/kill/done refuse a caller whose
// resolved scope does not match the job's scope.
type Scope struct {
	Session string `json:"session,omitempty"`
	Subject string `json:"subject,omitempty"`
}

// IsZero reports whether the scope carries no identity at all.
func (s Scope) IsZero() bool { return s.Session == "" && s.Subject == "" }

// Contains reports whether a job scope `inner` falls inside the query scope
// `s` (used by Snapshot / Reclaim). An empty field in the query is a wildcard;
// an empty field on the job side only matches a wildcard query on that field.
func (s Scope) Contains(inner Scope) bool {
	if s.Session != "" && inner.Session != s.Session {
		return false
	}
	if s.Subject != "" && inner.Subject != s.Subject {
		return false
	}
	return true
}

// Spec is the full input of one dispatch.
type Spec struct {
	// Kind selects the registered Executor that runs this job.
	Kind Kind
	// Scope is the isolation / reclamation / grouping scope (see Scope).
	Scope Scope
	// Node is an attribution tag: the orchestration node or team stage that
	// dispatched this job. It is recorded and projected, never authorized.
	Node string
	// Batch is an attribution tag: the chat request that dispatched this job.
	// It has the same role as a product batch id; never authorized.
	Batch string
	// Description is the job's row title (one line). Required: a background job
	// outlives the turn that dispatched it, so this sentence is the only honest
	// source for the job's row.
	Description string
	// Payload is interpreted by the Executor only.
	Payload json.RawMessage
	// Index is the dispatch-order sort key within one batch (not a completion
	// order).
	Index int
	// Dedup, when non-empty, collapses concurrent dispatches that share the same
	// scope + dedup key while the existing job is still running. It has no
	// effect once the existing job reaches a terminal state.
	Dedup string
	// OutputPath, when non-empty, names an output file the *product* owns: the
	// manager then never creates or holds a write handle for it (it only reads
	// it through an offset cursor), so the containing directory stays removable
	// while the job is registered, and the product decides how the bytes are
	// bounded. The manager still deletes a path it owns on retirement, but a
	// product-owned path is the product's to remove.
	//
	// Empty means the manager creates and owns <outputDir>/<handle>.log.
	OutputPath string
	// Handle is the job's identity, **assigned by the manager** when the job is
	// registered. It is filled in before the executor's Start is called, so an
	// executor that keeps a per-job side table (a process tree, a cancel hook,
	// the raw command, an index) keys it by this value. A caller-supplied value
	// is overwritten: the handle is proof of registration, not an input.
	Handle Handle
}

// Record is the read-only projection of one job. It is what Observe/Snapshot
// return and what a product renders.
type Record struct {
	Handle      Handle `json:"handle"`
	Seq         int    `json:"seq"`
	Kind        Kind   `json:"kind"`
	State       State  `json:"state"`
	ExitCode    int    `json:"exit_code"`
	Scope       Scope  `json:"scope"`
	Node        string `json:"node,omitempty"`
	Batch       string `json:"batch,omitempty"`
	Description string `json:"description"`
	// OutputRef is the job's output file path (manager-owned, or the
	// product-supplied Spec.OutputPath). A product renders its receipt's log
	// path from here instead of guessing the naming rule.
	OutputRef string    `json:"output_ref,omitempty"`
	Bytes     int64     `json:"bytes"`
	Lines     int       `json:"lines"`
	Cursor    int64     `json:"cursor"`
	Truncated bool      `json:"truncated"`
	Degraded  bool      `json:"degraded"`
	Summary   string    `json:"summary,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	// Repeated is a transient receipt flag: true when this job's handle was
	// returned by a dispatch that the dedup rule collapsed. It is never
	// persisted and only describes the dispatch that produced the handle.
	Repeated bool `json:"repeated,omitempty"`
}

// Running reports whether the record is still live.
func (r Record) Running() bool { return r.State == StateRunning }

// FetchBudget bounds one fetch request: how long the caller is willing to wait
// for a still-running job and how many bytes it wants back in this call.
type FetchBudget struct {
	// WaitMS is this request's wait budget: 0 = the manager default, negative =
	// return immediately with whatever is available, above the hard ceiling is
	// clamped. It belongs to this request, so it is carried here rather than
	// opening a second management entry point.
	WaitMS int
	// MaxBytes caps the bytes returned by this call. 0 = the manager default.
	MaxBytes int
}

// Sentinel errors. Callers separate them with errors.Is so a panel can tell
// "wrong scope" from "unknown handle" from "still running".
var (
	ErrUnknownHandle = errors.New("jobs: unknown handle")
	ErrCrossScope    = errors.New("jobs: handle belongs to another scope")
	ErrStillRunning  = errors.New("jobs: job is still running")
	ErrUnknownKind   = errors.New("jobs: no executor registered for kind")
	ErrInFlightLimit = errors.New("jobs: in-flight limit reached")
	ErrRetired       = errors.New("jobs: job already retired")
	ErrManagerClosed = errors.New("jobs: manager is closed")
	ErrEmptySession  = errors.New("jobs: scope.session is required")
	ErrEmptyDesc     = errors.New("jobs: spec.description is required")
	ErrExecutorPanic = errors.New("jobs: executor panicked")
)

// CloseTimeoutDefault bounds how long Close waits for live jobs to observe
// cancellation before giving up on them.
const CloseTimeoutDefault = 5 * time.Second
