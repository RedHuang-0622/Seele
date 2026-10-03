package jobs

import "context"

// Sink is the execution-side channel an Executor writes through. Every method
// must be safe to call from any goroutine and must never block on a reader.
type Sink interface {
	// Note appends text to the job's bounded output file. Bytes past the size
	// cap are dropped while still reporting "written" to the caller, so an
	// infrastructure budget is never disguised as a command failure (I-6). It
	// is a no-op when the job's output file is product-owned (Spec.OutputPath).
	Note(text string)
	// SignalBytes reports that new bytes are available without appending text.
	// It exists for executors that write the output file themselves.
	SignalBytes()
	// Exit records the exit code projected in the terminal record. It must be
	// called before Complete.
	//
	// It is an addition to the documented three-method shape: the documented
	// Complete(state, summary) cannot carry the exit code that Record.ExitCode
	// promises, and synthesizing "124/137 for everything" would erase exactly
	// the distinction I-2 requires.
	Exit(code int)
	// Complete performs the single terminal transition (I-1). Exactly one of the
	// four paths - normal exit, hard cap, killed, executor panic - must reach
	// it. Calling it twice is a no-op.
	Complete(state State, summary string)
}

// Executor runs one kind of job. Implementations are product-supplied: the
// framework ships no executor of its own.
//
// Start is called once per job, on its own goroutine, and must return when the
// work is finished or the context is cancelled. It reports the outcome through
// the Sink; returning an error is equivalent to a failed terminal transition
// with exit code 1 unless the Sink already completed the job.
type Executor interface {
	// Kind is the job kind this executor serves. It must be unique per manager.
	Kind() Kind
	// Start executes the job until it finishes or ctx is cancelled.
	Start(ctx context.Context, spec Spec, sink Sink) error
}

// ExecutorFunc adapts a function to Executor.
type ExecutorFunc struct {
	JobKind Kind
	Run     func(ctx context.Context, spec Spec, sink Sink) error
}

// Kind implements Executor.
func (f ExecutorFunc) Kind() Kind { return f.JobKind }

// Start implements Executor.
func (f ExecutorFunc) Start(ctx context.Context, spec Spec, sink Sink) error {
	if f.Run == nil {
		return nil
	}
	return f.Run(ctx, spec, sink)
}
