package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Synthetic exit codes for the two "ended by an outside force" paths. They are
// deliberately the timeout(1) / 128+SIGKILL conventions so a terminal record
// can tell "the command exited non-zero on its own" from "we stopped it".
const (
	timeoutExit = 124
	killedExit  = 137
)

var (
	timeoutNote = fmt.Sprintf("\n[seele:jobs] 作业超过硬上限，执行体已终止（exit=%d）\n", timeoutExit)
	killedNote  = fmt.Sprintf("\n[seele:jobs] 作业被终止（kill 或作用域回收），执行体已退出（exit=%d）\n", killedExit)
	panicNote   = "\n[seele:jobs] 执行体内部错误，已合成失败终态\n"
)

// Manager is the single implementation of dispatch plus the four management
// actions, shared by every job kind: the differences between kinds live in the
// Executor, never here. That is what keeps "observe / fetch / kill / done"
// semantics from drifting apart per tool.
type Manager interface {
	// Dispatch validates and registers a job, starts its executor and returns
	// the handle immediately; it never waits for output. A handle returned by a
	// deduplicated dispatch is the running job's handle.
	Dispatch(ctx context.Context, spec Spec) (Handle, error)
	// Declare validates and registers a job and returns its handle *without
	// starting its executor*. It is the register-only counterpart of Dispatch:
	// a product that owns the execution body (and reports its outcome through
	// Complete) uses it to obtain the handle before the body runs, and a
	// product whose body is a registered Executor calls Start to launch it.
	//
	// Aligning with the register-only semantics of the async face this replaced,
	// Declare does NOT require Spec.Description (a register-only caller may fill
	// the row title later); Dispatch still does.
	Declare(ctx context.Context, spec Spec) (Handle, error)
	// Start launches the executor of a job registered by Declare. It is
	// idempotent (a second call is a no-op) and refuses a kind whose executor is
	// not registered (ErrUnknownKind). A job dispatched by Dispatch is already
	// started.
	Start(ctx context.Context, handle Handle) error
	// Observe is a read-only reading of one job: it does NOT advance the cursor
	// and does not consume output. It is intentionally context-free; a product
	// that needs isolation on the read path must gate it itself (Fetch/Kill/Done
	// all enforce scope).
	Observe(handle Handle) (Record, bool)
	// Peek returns the not-yet-consumed increment *without* advancing the cursor
	// and *without* retiring a terminal job. It is the read-only twin of Fetch,
	// for a two-phase read (peek, then commit by Fetch) or a probe that must not
	// consume. Its FetchBudget has the same meaning as in Fetch.
	Peek(ctx context.Context, handle Handle, budget FetchBudget) (string, Record, error)
	// Fetch returns the not-yet-consumed increment and advances the cursor.
	// A terminal job is retired once its last byte has been delivered.
	Fetch(ctx context.Context, handle Handle, budget FetchBudget) (string, Record, error)
	// Complete performs the terminal transition of a job from *outside* its
	// executor: the external twin of Sink.Complete, for a product that owns the
	// execution body (Declare). It is idempotent - completing a job that is
	// already terminal, or already retired, is a no-op - and refuses a caller
	// outside the job's scope. A non-terminal state argument is coerced to
	// StateFailed.
	Complete(ctx context.Context, handle Handle, state State, exitCode int, summary string) error
	// Kill terminates the execution body (process tree / context cancellation).
	// Already produced output is preserved and still fetchable.
	Kill(ctx context.Context, handle Handle) error
	// Done retires an already-terminal job (idempotent). It never guesses a
	// state: a running job is refused (use Kill).
	Done(ctx context.Context, handle Handle) error
	// Snapshot lists the registered jobs of a scope. An empty query field is a
	// wildcard: {Session} = every job of the session, {Session, Subject} = only
	// that subject's.
	Snapshot(scope Scope) []Record
	// Reclaim cancels and retires every job of a scope. {Session} is
	// session-level reclamation; {Session, Subject} touches only that subject.
	Reclaim(ctx context.Context, scope Scope) error
	// Events is the change-signal port: dispatch / terminal / new bytes. Buffer
	// of one, latest-wins; it never advances a cursor and never enters context.
	// Products project it (UI, panels); the framework never pushes a result into
	// a busy session.
	Events() <-chan struct{}
	// ScopeOf resolves the caller's scope from a management context. It exists so
	// that a management tool can run Snapshot/Reclaim on the caller's own scope
	// without duplicating the manager's resolver wiring.
	ScopeOf(ctx context.Context) Scope
	// RetiredState returns the terminal state literal of a handle that has been
	// retired. It is the tombstone read face: a repeated Done/Fetch can answer
	// with the state a job ended in instead of a bare ErrRetired, so an idempotent
	// receipt stays honest.
	RetiredState(handle Handle) (State, bool)
	// Close cancels every live job, waits a bounded time for them to observe
	// cancellation, then releases the table. Later dispatches are refused.
	Close(ctx context.Context) error
}

// manager is the only implementation of Manager.
type manager struct {
	mu      sync.Mutex
	table   map[Handle]*run
	order   []Handle
	retired map[Handle]State
	// retiredOrder bounds the retired map so it cannot grow without limit.
	retiredOrder []Handle
	seq          int
	closed       bool

	executors       map[Kind]Executor
	limits          Limits
	scopeResolver   func(context.Context) Scope
	subjectResolver func(context.Context) string
	sessionResolver func(context.Context) string
	reclaimer       func(context.Context, Scope) error
	clock           func() time.Time
	outputDir       string
	// ownsDir is true when the manager created its own output directory (the
	// product did not supply one) and must therefore remove it on Close.
	ownsDir bool
	signal  chan struct{}
}

// run is one registered job: the manager-owned half of it. Output state lives
// in run.writer so that no lock is ever held across both tables.
type run struct {
	handle    Handle
	seq       int
	spec      Spec
	state     State
	exitCode  int
	summary   string
	cursor    int64
	degraded  bool
	repeated  bool
	startedAt time.Time
	endedAt   time.Time

	killIntent bool
	capHit     bool
	finalized  bool
	// started is true once the job's executor has been launched (by Dispatch,
	// or by Start after Declare). A registered-but-unstarted job has no body to
	// observe cancellation, so Kill completes it directly.
	started bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	writer outputStore
	sink   *jobSink
}

// New builds a Manager from options. At least one Executor must be registered
// for the manager to be useful, but an executor-less manager is accepted: the
// refusal then happens at Dispatch with ErrUnknownKind (which is a clearer
// failure than a construction error for a product that registers executors
// lazily).
func New(opts ...Option) (Manager, error) {
	config := options{}
	config.limits = DefaultLimits()
	config.clock = time.Now
	for _, option := range opts {
		if option != nil {
			option(&config)
		}
	}
	limits := config.limits.withDefaults()
	dir := config.outputDir
	ownsDir := false
	if dir == "" {
		created, err := os.MkdirTemp("", "seele-jobs-*")
		if err != nil {
			return nil, fmt.Errorf("jobs: create output dir: %w", err)
		}
		dir = created
		ownsDir = true
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("jobs: create output dir %q: %w", dir, err)
	}
	m := &manager{
		table:           map[Handle]*run{},
		retired:         map[Handle]State{},
		executors:       config.executors,
		limits:          limits,
		scopeResolver:   config.scopeResolver,
		subjectResolver: config.subjectResolver,
		sessionResolver: config.sessionResolver,
		reclaimer:       config.reclaimer,
		clock:           config.clock,
		outputDir:       dir,
		ownsDir:         ownsDir,
		signal:          make(chan struct{}, 1),
	}
	return m, nil
}

// Dispatch implements Manager.
func (m *manager) Dispatch(ctx context.Context, spec Spec) (Handle, error) {
	handle, run, executor, err := m.register(ctx, spec, true)
	if err != nil {
		return "", err
	}
	if run != nil {
		m.launch(executor, run)
	}
	return handle, nil
}

// Declare implements Manager.
func (m *manager) Declare(ctx context.Context, spec Spec) (Handle, error) {
	handle, _, _, err := m.register(ctx, spec, false)
	if err != nil {
		return "", err
	}
	return handle, nil
}

// register validates and registers one job. It is the single implementation
// behind Dispatch and Declare, so a declared job and a dispatched one differ
// only in whether the executor is started. A deduplicated dispatch returns the
// existing handle with a nil run (nothing to start).
func (m *manager) register(ctx context.Context, spec Spec, start bool) (Handle, *run, Executor, error) {
	if spec.Kind == "" {
		return "", nil, nil, fmt.Errorf("%w: kind is required", ErrUnknownKind)
	}
	if start && strings.TrimSpace(spec.Description) == "" {
		return "", nil, nil, ErrEmptyDesc
	}
	if spec.Scope.Session == "" {
		return "", nil, nil, ErrEmptySession
	}
	executor := m.executors[spec.Kind]
	if start && executor == nil {
		return "", nil, nil, fmt.Errorf("%w: %q", ErrUnknownKind, spec.Kind)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", nil, nil, ErrManagerClosed
	}
	if spec.Dedup != "" {
		if existing := m.dedupLocked(spec); existing != nil {
			existing.repeated = true
			handle := existing.handle
			m.mu.Unlock()
			m.signalChange()
			return handle, nil, nil, nil
		}
	}
	if m.runningLocked() >= m.limits.InFlight {
		limit := m.limits.InFlight
		m.mu.Unlock()
		return "", nil, nil, fmt.Errorf("%w (in_flight=%d)", ErrInFlightLimit, limit)
	}
	m.seq++
	handle := Handle("a" + strconv.Itoa(m.seq))
	// The handle is assigned here and visible to the executor through
	// Spec.Handle (and to every projection through Record.Handle).
	spec.Handle = handle
	writer, err := newOutputStore(spec.OutputPath, filepath.Join(m.outputDir, string(handle)+".log"), m.limits.MaxOutputBytes, m.signalChange)
	if err != nil {
		m.mu.Unlock()
		return "", nil, nil, err
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	r := &run{
		handle:    handle,
		seq:       m.seq,
		spec:      spec,
		state:     StateRunning,
		startedAt: m.clock(),
		ctx:       jobCtx,
		cancel:    cancel,
		done:      make(chan struct{}),
		writer:    writer,
	}
	r.sink = &jobSink{manager: m, run: r}
	m.table[handle] = r
	m.order = append(m.order, handle)
	m.mu.Unlock()

	m.signalChange()
	return handle, r, executor, nil
}

// launch starts one registered job's executor on its own goroutine.
func (m *manager) launch(executor Executor, r *run) {
	m.mu.Lock()
	if r.started {
		m.mu.Unlock()
		return
	}
	r.started = true
	jobCtx := r.ctx
	m.mu.Unlock()
	go m.execute(executor, jobCtx, r)
}

// Start implements Manager.
func (m *manager) Start(ctx context.Context, handle Handle) error {
	caller := m.resolveScope(ctx)
	m.mu.Lock()
	r, ok := m.table[handle]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
	}
	if !scopeAllows(caller, r.spec.Scope) {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCrossScope, handle)
	}
	if r.started {
		m.mu.Unlock()
		return nil
	}
	if r.finalized {
		m.mu.Unlock()
		return fmt.Errorf("jobs: %s already reached a terminal state, cannot start", handle)
	}
	executor := m.executors[r.spec.Kind]
	if executor == nil {
		m.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrUnknownKind, r.spec.Kind)
	}
	r.started = true
	jobCtx := r.ctx
	m.mu.Unlock()
	go m.execute(executor, jobCtx, r)
	return nil
}

// Observe implements Manager.
func (m *manager) Observe(handle Handle) (Record, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.table[handle]
	if !ok {
		return Record{}, false
	}
	return m.recordLocked(r), true
}

// Peek implements Manager.
func (m *manager) Peek(ctx context.Context, handle Handle, budget FetchBudget) (string, Record, error) {
	return m.read(ctx, handle, budget, false)
}

// Fetch implements Manager.
func (m *manager) Fetch(ctx context.Context, handle Handle, budget FetchBudget) (string, Record, error) {
	return m.read(ctx, handle, budget, true)
}

// read is the single implementation behind Peek and Fetch: it waits out a
// running job's budget, reads the not-yet-consumed increment, and - only when
// consume is set - advances the cursor and retires the job once its last byte
// has been delivered.
func (m *manager) read(ctx context.Context, handle Handle, budget FetchBudget, consume bool) (string, Record, error) {
	caller := m.resolveScope(ctx)
	m.mu.Lock()
	r, ok := m.table[handle]
	if !ok {
		_, wasRetired := m.retired[handle]
		m.mu.Unlock()
		if wasRetired {
			return "", Record{}, fmt.Errorf("%w: %s", ErrRetired, handle)
		}
		return "", Record{}, fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
	}
	if !scopeAllows(caller, r.spec.Scope) {
		m.mu.Unlock()
		return "", Record{}, fmt.Errorf("%w: %s", ErrCrossScope, handle)
	}
	if r.state == StateRunning {
		done := r.done
		m.mu.Unlock()
		if wait := m.waitBudget(budget.WaitMS); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-done:
				timer.Stop()
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return "", Record{}, ctx.Err()
			}
		}
		m.mu.Lock()
		r, ok = m.table[handle]
		if !ok {
			m.mu.Unlock()
			return "", Record{}, fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
		}
	}
	from := r.cursor
	size, lines, truncated := r.writer.stats()
	limit := int64(m.chunkBudget(budget.MaxBytes))
	to := size
	if limit > 0 && from+limit < to {
		to = from + limit
	}
	chunk := ""
	if to > from {
		read, err := readRange(r.writer.path(), from, to-from)
		if err != nil {
			m.mu.Unlock()
			return "", Record{}, err
		}
		chunk = read
		if consume {
			r.cursor = from + int64(len(read))
		}
	}
	record := Record{
		Handle: r.handle, Seq: r.seq, Kind: r.spec.Kind, State: r.state, ExitCode: r.exitCode,
		Scope: r.spec.Scope, Node: r.spec.Node, Batch: r.spec.Batch,
		Description: r.spec.Description, OutputRef: r.writer.path(),
		Bytes: size, Lines: lines, Cursor: r.cursor,
		Truncated: truncated, Degraded: r.degraded || (limit > 0 && to < size),
		Summary: r.summary, StartedAt: r.startedAt, EndedAt: r.endedAt, Repeated: r.repeated,
	}
	var retired outputStore
	terminal := consume && record.State.Terminal() && r.cursor >= size
	if terminal {
		retired = m.retireLocked(r)
	}
	m.mu.Unlock()
	if terminal {
		if retired != nil {
			retired.remove()
		}
		m.signalChange()
	}
	return chunk, record, nil
}

// Kill implements Manager.
func (m *manager) Kill(ctx context.Context, handle Handle) error {
	caller := m.resolveScope(ctx)
	m.mu.Lock()
	r, ok := m.table[handle]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
	}
	if !scopeAllows(caller, r.spec.Scope) {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCrossScope, handle)
	}
	if r.state.Terminal() {
		m.mu.Unlock()
		return nil
	}
	r.killIntent = true
	started, cancel, done := r.started, r.cancel, r.done
	m.mu.Unlock()

	cancel()
	m.signalChange()
	if !started {
		// No body was ever launched to observe cancellation, so the kill is
		// already complete: synthesize the terminal transition directly rather
		// than wait out a goroutine that will never report.
		m.finalize(r, StateKilled, "")
		return nil
	}
	select {
	case <-done:
		return nil
	case <-time.After(m.limits.DefaultWait):
		return fmt.Errorf("jobs: kill %s: 执行体在 %s 内未观察到取消（执行体必须尊重 ctx）", handle, m.limits.DefaultWait)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done implements Manager.
func (m *manager) Done(ctx context.Context, handle Handle) error {
	caller := m.resolveScope(ctx)
	m.mu.Lock()
	r, ok := m.table[handle]
	if !ok {
		_, wasRetired := m.retired[handle]
		m.mu.Unlock()
		if wasRetired {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
	}
	if !scopeAllows(caller, r.spec.Scope) {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCrossScope, handle)
	}
	if !r.state.Terminal() {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s（终态只由执行体判定，要停它用 Kill）", ErrStillRunning, handle)
	}
	retired := m.retireLocked(r)
	m.mu.Unlock()
	if retired != nil {
		retired.remove()
	}
	return nil
}

// Complete implements Manager.
func (m *manager) Complete(ctx context.Context, handle Handle, state State, exitCode int, summary string) error {
	if !state.Terminal() {
		// "Complete" with a live state is not a completion; treat it as the
		// failure it is rather than leaving a half-declared terminal row.
		state = StateFailed
	}
	caller := m.resolveScope(ctx)
	m.mu.Lock()
	if _, wasRetired := m.retired[handle]; wasRetired {
		// Idempotent: an already-terminal-and-retired job is done, not unknown.
		m.mu.Unlock()
		return nil
	}
	r, ok := m.table[handle]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownHandle, handle)
	}
	if !scopeAllows(caller, r.spec.Scope) {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrCrossScope, handle)
	}
	if r.finalized {
		m.mu.Unlock()
		return nil
	}
	if exitCode != 0 {
		r.exitCode = exitCode
	}
	m.mu.Unlock()
	m.finalize(r, state, summary)
	return nil
}

// Snapshot implements Manager.
func (m *manager) Snapshot(scope Scope) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Record, 0, len(m.table))
	for _, handle := range m.order {
		r, ok := m.table[handle]
		if !ok || !scope.Contains(r.spec.Scope) {
			continue
		}
		out = append(out, m.recordLocked(r))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Reclaim implements Manager.
func (m *manager) Reclaim(ctx context.Context, scope Scope) error {
	if scope.IsZero() {
		return errors.New("jobs: reclaim requires at least a session")
	}
	if m.reclaimer != nil {
		if err := m.reclaimer(ctx, scope); err != nil {
			return err
		}
	}
	m.mu.Lock()
	targets := make([]*run, 0, len(m.table))
	for _, handle := range m.order {
		if r, ok := m.table[handle]; ok && scope.Contains(r.spec.Scope) {
			targets = append(targets, r)
		}
	}
	m.mu.Unlock()

	for _, r := range targets {
		m.mu.Lock()
		live := !r.finalized
		r.killIntent = true
		started, cancel, done := r.started, r.cancel, r.done
		m.mu.Unlock()
		if !live {
			continue
		}
		cancel()
		if !started {
			// Nothing to wait for: complete the terminal transition directly.
			m.finalize(r, StateKilled, "")
			continue
		}
		select {
		case <-done:
		case <-time.After(m.limits.DefaultWait):
		case <-ctx.Done():
		}
	}

	m.mu.Lock()
	var retired []outputStore
	for _, handle := range append([]Handle(nil), m.order...) {
		if r, ok := m.table[handle]; ok && scope.Contains(r.spec.Scope) {
			retired = append(retired, m.retireLocked(r))
		}
	}
	m.mu.Unlock()
	for _, store := range retired {
		if store != nil {
			store.remove()
		}
	}
	m.signalChange()
	return nil
}

// Events implements Manager.
func (m *manager) Events() <-chan struct{} { return m.signal }

// ScopeOf implements Manager.
func (m *manager) ScopeOf(ctx context.Context) Scope { return m.resolveScope(ctx) }

// RetiredState implements Manager.
func (m *manager) RetiredState(handle Handle) (State, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.retired[handle]
	return state, ok
}

// Close implements Manager.
func (m *manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	targets := make([]*run, 0, len(m.table))
	for _, handle := range m.order {
		if r, ok := m.table[handle]; ok {
			targets = append(targets, r)
		}
	}
	m.mu.Unlock()

	for _, r := range targets {
		m.mu.Lock()
		live := !r.finalized
		r.killIntent = true
		started, cancel, done := r.started, r.cancel, r.done
		m.mu.Unlock()
		cancel()
		if !live || !started {
			// A job that is already terminal, or that never launched a body,
			// has nothing to wait for.
			continue
		}
		select {
		case <-done:
		case <-time.After(CloseTimeoutDefault):
		case <-ctx.Done():
		}
	}
	m.mu.Lock()
	for _, r := range m.table {
		r.writer.close()
	}
	ownsDir, dir := m.ownsDir, m.outputDir
	m.mu.Unlock()
	// A manager-owned directory is reclaimed on Close: nothing else outlives
	// the manager to remove it. Best-effort (live bodies may still hold files).
	if ownsDir && dir != "" {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// execute runs one job and guarantees exactly one terminal transition (I-1),
// whichever of the four paths ends it.
func (m *manager) execute(executor Executor, ctx context.Context, r *run) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.writer.write(panicNote)
			m.finalize(r, StateFailed, fmt.Sprintf("executor panic: %v", recovered))
		}
	}()
	if hardCap := m.limits.HardCap; hardCap > 0 {
		timer := time.AfterFunc(hardCap, func() {
			m.mu.Lock()
			r.capHit = true
			cancel := r.cancel
			m.mu.Unlock()
			cancel()
		})
		defer timer.Stop()
	}
	if err := executor.Start(ctx, r.spec, r.sink); err != nil {
		r.writer.write("\n[seele:jobs] 执行体报错：" + err.Error() + "\n")
		r.sink.Exit(1)
		m.finalize(r, StateFailed, err.Error())
		return
	}
	m.mu.Lock()
	finished := r.finalized
	m.mu.Unlock()
	if !finished {
		// The executor returned without a terminal transition: synthesize the
		// only honest one rather than leaving a forever-running ghost row.
		m.finalize(r, StateDone, "")
	}
}

// finalize performs the single terminal transition. The second call is a no-op.
func (m *manager) finalize(r *run, state State, summary string) {
	m.mu.Lock()
	if r.finalized {
		m.mu.Unlock()
		return
	}
	r.finalized = true
	switch {
	case r.killIntent:
		state = StateKilled
		if r.exitCode == 0 {
			r.exitCode = killedExit
		}
	case r.capHit:
		// A hard cap is never a success: it is the framework stopping work that
		// overran its budget, regardless of what the executor last claimed.
		state = StateFailed
		if r.exitCode == 0 {
			r.exitCode = timeoutExit
		}
	}
	if state == StateFailed && r.exitCode == 0 {
		r.exitCode = 1
	}
	r.state = state
	if state != StateDone {
		r.degraded = true
	}
	r.endedAt = m.clock()
	if strings.TrimSpace(summary) == "" {
		_, lines, _ := r.writer.stats()
		summary = fmt.Sprintf("exit=%d · %d 行", r.exitCode, lines)
	}
	r.summary = boundSummary(summary)
	killIntent, capHit := r.killIntent, r.capHit
	m.mu.Unlock()

	switch {
	case killIntent:
		r.writer.write(killedNote)
	case capHit:
		r.writer.write(timeoutNote)
	}
	close(r.done)
	m.signalChange()
	m.prune()
}

// prune retires the oldest terminal jobs when the table exceeds its ceiling;
// live jobs are never evicted.
func (m *manager) prune() {
	m.mu.Lock()
	var retired []outputStore
	defer func() {
		m.mu.Unlock()
		for _, store := range retired {
			if store != nil {
				store.remove()
			}
		}
	}()
	if len(m.table) <= m.limits.Records {
		return
	}
	for _, handle := range append([]Handle(nil), m.order...) {
		if len(m.table) <= m.limits.Records {
			return
		}
		if r, ok := m.table[handle]; ok && r.state.Terminal() {
			retired = append(retired, m.retireLocked(r))
		}
	}
}

// retireLocked removes one job from the table and remembers its terminal state
// so that a second Done stays idempotent. It closes the job's write handle and
// returns its output store so the caller can delete a manager-owned file after
// releasing the lock (file I/O never happens under the table lock).
func (m *manager) retireLocked(r *run) outputStore {
	if _, ok := m.table[r.handle]; !ok {
		return nil
	}
	delete(m.table, r.handle)
	for index, handle := range m.order {
		if handle == r.handle {
			m.order = append(m.order[:index], m.order[index+1:]...)
			break
		}
	}
	m.retired[r.handle] = r.state
	m.retiredOrder = append(m.retiredOrder, r.handle)
	for len(m.retiredOrder) > m.limits.Records {
		oldest := m.retiredOrder[0]
		m.retiredOrder = m.retiredOrder[1:]
		delete(m.retired, oldest)
	}
	r.writer.close()
	return r.writer
}

// recordLocked projects one run. Caller holds m.mu.
func (m *manager) recordLocked(r *run) Record {
	size, lines, truncated := r.writer.stats()
	return Record{
		Handle: r.handle, Seq: r.seq, Kind: r.spec.Kind, State: r.state, ExitCode: r.exitCode,
		Scope: r.spec.Scope, Node: r.spec.Node, Batch: r.spec.Batch,
		Description: r.spec.Description, OutputRef: r.writer.path(),
		Bytes: size, Lines: lines, Cursor: r.cursor,
		Truncated: truncated, Degraded: r.degraded,
		Summary: r.summary, StartedAt: r.startedAt, EndedAt: r.endedAt, Repeated: r.repeated,
	}
}

// dedupLocked returns the running job a dispatch should collapse into.
func (m *manager) dedupLocked(spec Spec) *run {
	for _, handle := range m.order {
		r, ok := m.table[handle]
		if !ok || r.state != StateRunning || r.spec.Dedup == "" {
			continue
		}
		if r.spec.Dedup != spec.Dedup || r.spec.Scope != spec.Scope || r.spec.Kind != spec.Kind {
			continue
		}
		if !bytes.Equal(r.spec.Payload, spec.Payload) {
			continue
		}
		return r
	}
	return nil
}

// runningLocked counts live jobs.
func (m *manager) runningLocked() int {
	count := 0
	for _, r := range m.table {
		if r.state == StateRunning {
			count++
		}
	}
	return count
}

// resolveScope resolves the caller's scope from the context.
func (m *manager) resolveScope(ctx context.Context) Scope {
	if m.scopeResolver != nil {
		return m.scopeResolver(ctx)
	}
	var scope Scope
	if m.sessionResolver != nil {
		scope.Session = m.sessionResolver(ctx)
	}
	if m.subjectResolver != nil {
		scope.Subject = m.subjectResolver(ctx)
	}
	return scope
}

// waitBudget clamps a fetch wait request.
func (m *manager) waitBudget(requested int) time.Duration {
	if requested < 0 {
		return 0
	}
	if requested == 0 {
		return m.limits.DefaultWait
	}
	wait := time.Duration(requested) * time.Millisecond
	if wait > m.limits.MaxWait {
		return m.limits.MaxWait
	}
	return wait
}

// chunkBudget clamps a per-fetch byte request.
func (m *manager) chunkBudget(requested int) int {
	if requested <= 0 {
		return m.limits.FetchChunkBytes
	}
	return requested
}

// signalChange pings the change-signal port; it never blocks.
func (m *manager) signalChange() {
	select {
	case m.signal <- struct{}{}:
	default:
	}
}

// scopeAllows reports whether a caller scope may touch a job scope. An empty
// caller field is a wildcard: the main agent (no subject) sees its whole
// session; a teammate (subject set) sees only its own jobs.
func scopeAllows(caller, job Scope) bool {
	if caller.Session != "" && caller.Session != job.Session {
		return false
	}
	if caller.Subject != "" && caller.Subject != job.Subject {
		return false
	}
	return true
}

// boundSummary keeps a terminal summary strictly bounded: it is replayed on
// every turn boundary, so it must never grow.
func boundSummary(summary string) string {
	const limit = 512
	summary = strings.TrimSpace(summary)
	if len(summary) <= limit {
		return summary
	}
	cut := summary[:limit]
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size > 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut
}
