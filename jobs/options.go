package jobs

import (
	"context"
	"time"
)

// Limits bounds the manager's resource usage. A zero Limits is completed by
// DefaultLimits before use, so a product only declares what it wants to change.
type Limits struct {
	// InFlight is the maximum number of simultaneously running jobs. It is the
	// framework-level backstop; a product-level cap (e.g. a team size limit)
	// must take effect before it, otherwise the framework guard is what the
	// user actually hits.
	InFlight int
	// Records is the soft ceiling of the in-memory table. When exceeded, the
	// oldest terminal jobs are retired first; live jobs are never evicted.
	Records int
	// HardCap is the wall-clock ceiling of one job. Zero = no cap. Reaching it
	// cancels the job and synthesizes exit code 124.
	HardCap time.Duration
	// MaxOutputBytes caps the output file of one job. Once reached, further
	// bytes are dropped but still reported as written (I-6).
	MaxOutputBytes int64
	// FetchChunkBytes is the default number of bytes returned by one Fetch.
	FetchChunkBytes int
	// DefaultWait is the wait budget of a Fetch whose FetchBudget.WaitMS is 0.
	DefaultWait time.Duration
	// MaxWait clamps FetchBudget.WaitMS.
	MaxWait time.Duration
}

// DefaultLimits returns the framework defaults.
func DefaultLimits() Limits {
	return Limits{
		InFlight:        32,
		Records:         256,
		HardCap:         0,
		MaxOutputBytes:  1 << 20,
		FetchChunkBytes: 64 << 10,
		DefaultWait:     5 * time.Second,
		MaxWait:         60 * time.Second,
	}
}

// withDefaults fills zero fields from DefaultLimits.
func (l Limits) withDefaults() Limits {
	def := DefaultLimits()
	if l.InFlight == 0 {
		l.InFlight = def.InFlight
	}
	if l.Records == 0 {
		l.Records = def.Records
	}
	if l.MaxOutputBytes == 0 {
		l.MaxOutputBytes = def.MaxOutputBytes
	}
	if l.FetchChunkBytes == 0 {
		l.FetchChunkBytes = def.FetchChunkBytes
	}
	if l.DefaultWait == 0 {
		l.DefaultWait = def.DefaultWait
	}
	if l.MaxWait == 0 {
		l.MaxWait = def.MaxWait
	}
	return l
}

// options is the mutable configuration assembled by Option values.
type options struct {
	executors       map[Kind]Executor
	limits          Limits
	scopeResolver   func(context.Context) Scope
	subjectResolver func(context.Context) string
	sessionResolver func(context.Context) string
	reclaimer       func(context.Context, Scope) error
	clock           func() time.Time
	outputDir       string
}

// Option configures a Manager at construction time.
type Option func(*options)

// WithExecutor registers an Executor. Two executors for the same kind are a
// construction error: silently keeping the last one would make "which code
// runs this job" depend on option order.
func WithExecutor(executor Executor) Option {
	return func(o *options) {
		if executor == nil || executor.Kind() == "" {
			return
		}
		if o.executors == nil {
			o.executors = map[Kind]Executor{}
		}
		o.executors[executor.Kind()] = executor
	}
}

// WithLimits overrides the resource limits.
func WithLimits(limits Limits) Option { return func(o *options) { o.limits = limits } }

// WithScopeResolver resolves the caller's full scope from the dispatch /
// management context. It is the preferred hook; WithSessionResolver and
// WithSubjectResolver are the two-field shorthand for products that keep the
// fields in separate context keys.
func WithScopeResolver(resolver func(context.Context) Scope) Option {
	return func(o *options) { o.scopeResolver = resolver }
}

// WithSubjectResolver resolves the caller's subject from the context
// (e.g. an "emp_<role>" employee subject).
func WithSubjectResolver(resolver func(context.Context) string) Option {
	return func(o *options) { o.subjectResolver = resolver }
}

// WithSessionResolver resolves the caller's session key from the context. The
// session key drives isolation and reclamation, so it must be the same value
// the product's session router uses.
func WithSessionResolver(resolver func(context.Context) string) Option {
	return func(o *options) { o.sessionResolver = resolver }
}

// WithReclaimer registers a product hook invoked by Reclaim before the jobs of
// the scope are killed, so a product can tear down resources the framework
// does not know about (e.g. a git worktree).
func WithReclaimer(reclaimer func(context.Context, Scope) error) Option {
	return func(o *options) { o.reclaimer = reclaimer }
}

// WithClock overrides wall time, for deterministic tests.
func WithClock(clock func() time.Time) Option { return func(o *options) { o.clock = clock } }

// WithOutputDir overrides the directory holding per-job output files. When it
// is empty the manager creates a private temporary directory.
func WithOutputDir(dir string) Option { return func(o *options) { o.outputDir = dir } }
