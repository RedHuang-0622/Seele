package jobs

// Namer names every job a Manager registers.
//
// Naming is a *product* decision, not a framework default: handles live in the
// product's vocabulary (one product keeps teammate jobs as "bteam-3" and
// background commands as "bt-1"), and only the product knows which namespaces
// must stay apart. The framework therefore no longer invents a handle shape of
// its own — a Manager is built with exactly one Namer, and New refuses a
// manager built without one.
//
// Contract of Name:
//
//   - Called exactly once per registration with the manager's own monotonic
//     seq (the same number Record.Seq reports). Dispatch/Declare call it *while
//     the manager's lock is held*, so it must be cheap, must not call back into
//     the Manager (deadlock), and must be safe for concurrent dispatches.
//   - It returns the job's handle; the manager validates it and refuses an
//     empty (ErrInvalidHandle) or already-registered (ErrDuplicateHandle)
//     handle instead of silently aliasing two jobs onto one identity — a
//     duplicated handle is exactly the "one identity, two jobs" defect that an
//     opaque framework-chosen name makes possible.
//   - The dedup path does not call it: a collapsed dispatch reuses the running
//     job's handle.
//   - Handles are never persisted (invariant I-4).
type Namer interface {
	// Name returns the handle for the job described by spec. seq is the
	// manager's registration counter, which a namer normally folds into the
	// name to keep handles unique.
	Name(spec Spec, seq int) Handle
}
