package jobs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testManager builds a manager whose caller scope is resolved from a context
// key, so isolation tests can switch identity without a product.
func testManager(t *testing.T, executors ...Executor) Manager {
	t.Helper()
	opts := make([]Option, 0, len(executors)+2)
	for _, executor := range executors {
		opts = append(opts, WithExecutor(executor))
	}
	opts = append(opts, WithScopeResolver(func(ctx context.Context) Scope {
		scope, _ := ctx.Value(scopeKey{}).(Scope)
		return scope
	}))
	manager, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

// internals exposes the concrete manager to white-box tests.
func internals(m Manager) *manager { return m.(*manager) }

type scopeKey struct{}

func scopeCtx(scope Scope) context.Context {
	return context.WithValue(context.Background(), scopeKey{}, scope)
}

// waitTerminal polls until the job reaches a terminal state.
func waitTerminal(t *testing.T, manager Manager, handle Handle) Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if record, ok := manager.Observe(handle); ok && record.State.Terminal() {
			return record
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach a terminal state", handle)
	return Record{}
}

func TestDispatchFetchRetiresOnLastByte(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Note("hello\n")
			sink.Exit(0)
			sink.Complete(StateDone, "ok")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "greet"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	record := waitTerminal(t, manager, handle)
	if record.ExitCode != 0 || record.State != StateDone {
		t.Fatalf("unexpected terminal record: %+v", record)
	}
	chunk, fetched, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if chunk != "hello\n" {
		t.Fatalf("chunk = %q, want %q", chunk, "hello\n")
	}
	if !fetched.State.Terminal() {
		t.Fatalf("fetched record not terminal: %+v", fetched)
	}
	// The terminal job is retired once its last byte was delivered.
	if _, ok := manager.Observe(handle); ok {
		t.Fatal("terminal job should have been retired after delivering all bytes")
	}
	if _, _, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1}); !errors.Is(err, ErrRetired) {
		t.Fatalf("second Fetch err = %v, want ErrRetired", err)
	}
}

func TestFetchIsIncremental(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Note("one\n")
			<-release
			sink.Note("two\n")
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "inc"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		chunk, record, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1})
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if chunk == "one\n" && !record.State.Terminal() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never observed the first increment: chunk=%q record=%+v", chunk, record)
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(release)
	chunk, record, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: 3000})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if chunk != "two\n" || !record.State.Terminal() {
		t.Fatalf("second increment = %q / %+v", chunk, record)
	}
}

func TestDedupCollapsesRunningJob(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindProcess,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-release
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	spec := Spec{Kind: KindProcess, Scope: Scope{Session: "s1"}, Description: "same", Dedup: "same-key", Payload: []byte("payload")}
	first, err := manager.Dispatch(ctx, spec)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	second, err := manager.Dispatch(ctx, spec)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if first != second {
		t.Fatalf("dedup did not collapse: %s vs %s", first, second)
	}
	if record, _ := manager.Observe(second); !record.Repeated {
		t.Fatal("collapsed dispatch should be flagged repeated")
	}
	close(release)
	waitTerminal(t, manager, first)
}

func TestCrossScopeAccessRefused(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-release
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	owner := scopeCtx(Scope{Session: "s1", Subject: "emp_a"})
	handle, err := manager.Dispatch(owner, Spec{Kind: KindInline, Scope: Scope{Session: "s1", Subject: "emp_a"}, Description: "x"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	otherSession := scopeCtx(Scope{Session: "s2"})
	if _, _, err := manager.Fetch(otherSession, handle, FetchBudget{WaitMS: -1}); !errors.Is(err, ErrCrossScope) {
		t.Fatalf("cross-session Fetch err = %v, want ErrCrossScope", err)
	}
	if err := manager.Kill(otherSession, handle); !errors.Is(err, ErrCrossScope) {
		t.Fatalf("cross-session Kill err = %v, want ErrCrossScope", err)
	}
	otherSubject := scopeCtx(Scope{Session: "s1", Subject: "emp_b"})
	if err := manager.Done(otherSubject, handle); !errors.Is(err, ErrCrossScope) {
		t.Fatalf("cross-subject Done err = %v, want ErrCrossScope", err)
	}
	// The main agent (empty subject) still owns its whole session.
	if _, _, err := manager.Fetch(scopeCtx(Scope{Session: "s1"}), handle, FetchBudget{WaitMS: -1}); err != nil {
		t.Fatalf("main-agent Fetch: %v", err)
	}
	close(release)
	waitTerminal(t, manager, handle)
}

func TestSnapshotAndReclaimAreTwoTier(t *testing.T) {
	block := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindProcess,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			select {
			case <-block:
			case <-ctx.Done():
				sink.Complete(StateKilled, "")
				return nil
			}
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := context.Background()
	dispatch := func(session, subject string) Handle {
		handle, err := manager.Dispatch(ctx, Spec{Kind: KindProcess, Scope: Scope{Session: session, Subject: subject}, Description: "job-" + subject})
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		return handle
	}
	a := dispatch("s1", "emp_a")
	b := dispatch("s1", "emp_b")
	c := dispatch("s1", "")
	d := dispatch("s2", "emp_a")

	if got := len(manager.Snapshot(Scope{Session: "s1"})); got != 3 {
		t.Fatalf("session snapshot = %d, want 3", got)
	}
	if got := len(manager.Snapshot(Scope{Session: "s1", Subject: "emp_a"})); got != 1 {
		t.Fatalf("subject snapshot = %d, want 1", got)
	}
	if err := manager.Reclaim(context.Background(), Scope{Session: "s1", Subject: "emp_a"}); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if _, ok := manager.Observe(a); ok {
		t.Fatal("reclaimed teammate job still registered")
	}
	if _, ok := manager.Observe(b); !ok {
		t.Fatal("sibling teammate job was reclaimed by mistake")
	}
	if _, ok := manager.Observe(c); !ok {
		t.Fatal("main-agent job of the same session was reclaimed by mistake")
	}
	if _, ok := manager.Observe(d); !ok {
		t.Fatal("job of another session was reclaimed by mistake")
	}
	close(block)
}

func TestHardCapSynthesizesTimeoutExit(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindProcess,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-ctx.Done()
			sink.Complete(StateKilled, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	internals(manager).limits.HardCap = 20 * time.Millisecond
	handle, err := manager.Dispatch(context.Background(), Spec{Kind: KindProcess, Scope: Scope{Session: "s1"}, Description: "hang"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	record := waitTerminal(t, manager, handle)
	if record.State != StateFailed || record.ExitCode != timeoutExit {
		t.Fatalf("hard cap record = %+v, want failed/%d", record, timeoutExit)
	}
}

func TestKillSynthesizesKilledExit(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindProcess,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-ctx.Done()
			sink.Complete(StateKilled, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindProcess, Scope: Scope{Session: "s1"}, Description: "kill-me"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := manager.Kill(ctx, handle); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	record, ok := manager.Observe(handle)
	if !ok {
		t.Fatal("killed job disappeared without being fetched")
	}
	if record.State != StateKilled || record.ExitCode != killedExit {
		t.Fatalf("killed record = %+v, want killed/%d", record, killedExit)
	}
}

func TestExecutorPanicBecomesFailure(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			panic("boom")
		},
	}
	manager := testManager(t, executor)
	handle, err := manager.Dispatch(context.Background(), Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "panic"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	record := waitTerminal(t, manager, handle)
	if record.State != StateFailed || !strings.Contains(record.Summary, "boom") {
		t.Fatalf("panic record = %+v", record)
	}
}

func TestDoneIsIdempotentAndRefusesRunning(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-release
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "x"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if err := manager.Done(ctx, handle); !errors.Is(err, ErrStillRunning) {
		t.Fatalf("Done on running job err = %v, want ErrStillRunning", err)
	}
	close(release)
	waitTerminal(t, manager, handle)
	if err := manager.Done(ctx, handle); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if err := manager.Done(ctx, handle); err != nil {
		t.Fatalf("second Done: %v", err)
	}
}

func TestInFlightLimitRefusesDispatch(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			<-release
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	internals(manager).limits.InFlight = 2
	ctx := context.Background()
	for index := 0; index < 2; index++ {
		if _, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: fmt.Sprintf("j%d", index)}); err != nil {
			t.Fatalf("Dispatch %d: %v", index, err)
		}
	}
	if _, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "over"}); !errors.Is(err, ErrInFlightLimit) {
		t.Fatalf("over-limit Dispatch err = %v, want ErrInFlightLimit", err)
	}
	close(release)
}

func TestUnknownKindAndEmptyDescriptionRefused(t *testing.T) {
	manager := testManager(t)
	if _, err := manager.Dispatch(context.Background(), Spec{Kind: "", Scope: Scope{Session: "s1"}, Description: "x"}); !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("empty kind err = %v, want ErrUnknownKind", err)
	}
	if _, err := manager.Dispatch(context.Background(), Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "  "}); !errors.Is(err, ErrEmptyDesc) {
		t.Fatalf("empty description err = %v, want ErrEmptyDesc", err)
	}
	if _, err := manager.Dispatch(context.Background(), Spec{Kind: KindInline, Scope: Scope{}, Description: "x"}); !errors.Is(err, ErrEmptySession) {
		t.Fatalf("empty session err = %v, want ErrEmptySession", err)
	}
}

func TestEventsSignalIsLatestWins(t *testing.T) {
	var counter int
	var guard sync.Mutex
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			guard.Lock()
			counter++
			guard.Unlock()
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	events := manager.Events()
	for index := 0; index < 5; index++ {
		if _, err := manager.Dispatch(context.Background(), Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "e"}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no change signal after dispatch")
	}
	guard.Lock()
	defer guard.Unlock()
	_ = counter
}

func TestOutputCapTruncatesWithoutFailing(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Note(strings.Repeat("a", 4096))
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	internals(manager).limits.MaxOutputBytes = 16
	handle, err := manager.Dispatch(context.Background(), Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "big"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitTerminal(t, manager, handle)
	chunk, record, err := manager.Fetch(context.Background(), handle, FetchBudget{WaitMS: -1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(chunk) != 16 || !record.Truncated {
		t.Fatalf("truncation not honoured: len=%d truncated=%v", len(chunk), record.Truncated)
	}
	if record.State != StateDone || record.ExitCode != 0 {
		t.Fatalf("output cap must not change the job outcome: %+v", record)
	}
}

func TestDeclareRegistersWithoutStarting(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			started <- struct{}{}
			<-release
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	// Declare does not require a description: it is the register-only entry.
	handle, err := manager.Declare(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	select {
	case <-started:
		t.Fatal("Declare must not start the executor")
	case <-time.After(50 * time.Millisecond):
	}
	if record, ok := manager.Observe(handle); !ok || record.State != StateRunning {
		t.Fatalf("declared job not registered as running: %+v ok=%v", record, ok)
	}
	if err := manager.Start(ctx, handle); err != nil {
		t.Fatalf("Start: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Start did not launch the executor")
	}
	if err := manager.Start(ctx, handle); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	close(release)
	waitTerminal(t, manager, handle)
}

func TestDeclareAllowsEmptyDescriptionDispatchDoesNot(t *testing.T) {
	manager := testManager(t, ExecutorFunc{JobKind: KindInline})
	ctx := scopeCtx(Scope{Session: "s1"})
	if _, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}}); !errors.Is(err, ErrEmptyDesc) {
		t.Fatalf("Dispatch with empty description err = %v, want ErrEmptyDesc", err)
	}
	if _, err := manager.Declare(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}}); err != nil {
		t.Fatalf("Declare with empty description: %v", err)
	}
}

func TestCompleteIsExternalTerminalAndIdempotent(t *testing.T) {
	// No executor registered: the product owns the body and completes externally.
	manager := testManager(t)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Declare(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "external"})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if err := manager.Complete(ctx, handle, StateDone, 0, "ok"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	record, ok := manager.Observe(handle)
	if !ok || record.State != StateDone || record.ExitCode != 0 {
		t.Fatalf("completed record = %+v ok=%v", record, ok)
	}
	if err := manager.Complete(ctx, handle, StateDone, 0, "again"); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if err := manager.Done(ctx, handle); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if state, retired := manager.RetiredState(handle); !retired || state != StateDone {
		t.Fatalf("RetiredState = %v,%v want done,true", state, retired)
	}
	if err := manager.Complete(ctx, handle, StateDone, 0, "after retire"); err != nil {
		t.Fatalf("Complete after retire: %v", err)
	}
	if _, retired := manager.RetiredState("a999"); retired {
		t.Fatal("unknown handle must not report a tombstone")
	}
}

func TestCompleteRefusesCrossScope(t *testing.T) {
	manager := testManager(t)
	owner := scopeCtx(Scope{Session: "s1", Subject: "emp_a"})
	handle, err := manager.Declare(owner, Spec{Kind: KindInline, Scope: Scope{Session: "s1", Subject: "emp_a"}})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	intruder := scopeCtx(Scope{Session: "s2"})
	if err := manager.Complete(intruder, handle, StateDone, 0, ""); !errors.Is(err, ErrCrossScope) {
		t.Fatalf("cross-scope Complete err = %v, want ErrCrossScope", err)
	}
}

func TestKillUnstartedDeclaredJob(t *testing.T) {
	manager := testManager(t)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Declare(ctx, Spec{Kind: "external", Scope: Scope{Session: "s1"}})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	if err := manager.Kill(ctx, handle); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	record, ok := manager.Observe(handle)
	if !ok || record.State != StateKilled || record.ExitCode != killedExit {
		t.Fatalf("killed record = %+v ok=%v", record, ok)
	}
}

func TestPeekDoesNotAdvanceCursorOrRetire(t *testing.T) {
	release := make(chan struct{})
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Note("hello\n")
			<-release
			sink.Note("bye\n")
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "peek"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		chunk, record, err := manager.Peek(ctx, handle, FetchBudget{WaitMS: -1})
		if err != nil {
			t.Fatalf("Peek: %v", err)
		}
		if chunk == "hello\n" {
			if record.Cursor != 0 {
				t.Fatalf("Peek advanced the cursor to %d", record.Cursor)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never peeked the first chunk: %q", chunk)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Peek is repeatable: it never consumes.
	if chunk, _, err := manager.Peek(ctx, handle, FetchBudget{WaitMS: -1}); err != nil || chunk != "hello\n" {
		t.Fatalf("second Peek = %q err=%v", chunk, err)
	}
	close(release)
	waitTerminal(t, manager, handle)
	// A terminal Peek still does not retire.
	chunk, record, err := manager.Peek(ctx, handle, FetchBudget{WaitMS: -1})
	if err != nil {
		t.Fatalf("terminal Peek: %v", err)
	}
	if chunk != "hello\nbye\n" || !record.State.Terminal() {
		t.Fatalf("terminal Peek = %q / %+v", chunk, record)
	}
	if _, ok := manager.Observe(handle); !ok {
		t.Fatal("Peek must not retire a terminal job")
	}
	// Fetch commits and retires.
	if chunk, _, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1}); err != nil || chunk != "hello\nbye\n" {
		t.Fatalf("Fetch after Peek = %q err=%v", chunk, err)
	}
	if _, ok := manager.Observe(handle); ok {
		t.Fatal("Fetch must retire a terminal job once its bytes are delivered")
	}
	if state, retired := manager.RetiredState(handle); !retired || state != StateDone {
		t.Fatalf("RetiredState = %v,%v want done,true", state, retired)
	}
}

func TestSpecHandleIdentifiesJob(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			// The manager fills Spec.Handle before Start runs.
			sink.Note("handle=" + string(spec.Handle) + "\n")
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "h"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitTerminal(t, manager, handle)
	chunk, _, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if chunk != "handle="+string(handle)+"\n" {
		t.Fatalf("Spec.Handle mismatch: %q", chunk)
	}
}

func TestExternalOutputPathIsProductOwned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.log")
	if err := os.WriteFile(path, []byte("pre-existing\n"), 0o600); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			// Note is a no-op for a product-owned file: the product writes it.
			sink.Note("ignored\n")
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "ext", OutputPath: path})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitTerminal(t, manager, handle)
	chunk, record, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if chunk != "pre-existing\n" {
		t.Fatalf("external output = %q（manager 不该改写产品自有文件）", chunk)
	}
	if record.OutputRef != path {
		t.Fatalf("OutputRef = %q, want %q", record.OutputRef, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manager removed a product-owned output file: %v", err)
	}
}

func TestManagerOwnedOutputIsRemovedOnRetire(t *testing.T) {
	executor := ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Note("x\n")
			sink.Complete(StateDone, "")
			return nil
		},
	}
	manager := testManager(t, executor)
	ctx := scopeCtx(Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "own"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitTerminal(t, manager, handle)
	record, ok := manager.Observe(handle)
	if !ok || record.OutputRef == "" {
		t.Fatalf("expected an OutputRef, got %+v ok=%v", record, ok)
	}
	if _, _, err := manager.Fetch(ctx, handle, FetchBudget{WaitMS: -1}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(record.OutputRef); !os.IsNotExist(err) {
		t.Fatalf("manager-owned output should be gone after retire: err=%v", err)
	}
}
