package jobs

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// quietExecutor is the minimal executor these naming tests need: they are about
// who names a job, not about what it runs.
func quietExecutor() ExecutorFunc {
	return ExecutorFunc{
		JobKind: KindInline,
		Run: func(ctx context.Context, spec Spec, sink Sink) error {
			sink.Exit(0)
			sink.Complete(StateDone, "ok")
			return nil
		},
	}
}

// TestNewRequiresNamer pins the boundary the framework gave up: naming is the
// product's vocabulary, so a manager without a namer is refused at construction
// instead of inventing a shape of its own.
func TestNewRequiresNamer(t *testing.T) {
	_, err := New(WithExecutor(quietExecutor()))
	if !errors.Is(err, ErrNoNamer) {
		t.Fatalf("New without a namer: err = %v, want ErrNoNamer", err)
	}
}

// TestNamerDecidesTheHandle pins that the handle a job is known by comes from
// the injected namer (not from the manager, and not from the caller).
func TestNamerDecidesTheHandle(t *testing.T) {
	namer := countingNamer{prefix: "bteam"}
	manager := testManagerWithNamer(t, namer, quietExecutor())
	ctx := scopeCtx(Scope{Session: "s1"})

	handle, err := manager.Dispatch(ctx, Spec{
		Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "greet",
		Handle: "caller-tried-this", // ignored: the handle is proof of registration
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if want := Handle("bteam-1"); handle != want {
		t.Fatalf("handle = %q, want %q", handle, want)
	}
	record := waitTerminal(t, manager, handle)
	if record.Handle != handle {
		t.Fatalf("record.Handle = %q, want %q", record.Handle, handle)
	}
	if record.Seq != 1 {
		t.Fatalf("record.Seq = %d, want 1 (the namer is called with the manager's seq)", record.Seq)
	}
}

// TestNamerRefusesUnusableNames pins the guard that keeps two jobs from sharing
// one identity: an empty or already-taken answer is refused, and the refusal
// happens before the job is visible in the table.
func TestNamerRefusesUnusableNames(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		manager := testManagerWithNamer(t, emptyNamer{}, quietExecutor())
		_, err := manager.Dispatch(scopeCtx(Scope{Session: "s1"}),
			Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "greet"})
		if !errors.Is(err, ErrInvalidHandle) {
			t.Fatalf("Dispatch err = %v, want ErrInvalidHandle", err)
		}
		if records := manager.Snapshot(Scope{Session: "s1"}); len(records) != 0 {
			t.Fatalf("refused dispatch left %d rows in the table: %+v", len(records), records)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		manager := testManagerWithNamer(t, constantNamer("same"), quietExecutor())
		ctx := scopeCtx(Scope{Session: "s1"})
		first, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "first"})
		if err != nil {
			t.Fatalf("first Dispatch: %v", err)
		}
		second, err := manager.Dispatch(ctx, Spec{Kind: KindInline, Scope: Scope{Session: "s1"}, Description: "second"})
		if !errors.Is(err, ErrDuplicateHandle) {
			t.Fatalf("second Dispatch err = %v, want ErrDuplicateHandle (handle %q)", err, second)
		}
		// The first job must be untouched by the refused second dispatch.
		if record, ok := manager.Observe(first); !ok || record.Description != "first" {
			t.Fatalf("first job was disturbed by the refused dispatch: %+v (ok=%v)", record, ok)
		}
		if records := manager.Snapshot(Scope{Session: "s1"}); len(records) != 1 {
			t.Fatalf("table holds %d rows, want 1 (the refused job must not register)", len(records))
		}
	})
}

// countingNamer is the shape a product's namer takes: a namespace token plus the
// registration seq, so handles never collide inside one manager.
type countingNamer struct{ prefix string }

func (n countingNamer) Name(spec Spec, seq int) Handle {
	return Handle(n.prefix + "-" + strconv.Itoa(seq))
}

type emptyNamer struct{}

func (emptyNamer) Name(spec Spec, seq int) Handle { return "" }

type constantNamer string

func (n constantNamer) Name(spec Spec, seq int) Handle { return Handle(n) }

// testManagerWithNamer is testManager with the namer under test injected.
func testManagerWithNamer(t *testing.T, namer Namer, executors ...Executor) Manager {
	t.Helper()
	opts := make([]Option, 0, len(executors)+3)
	for _, executor := range executors {
		opts = append(opts, WithExecutor(executor))
	}
	opts = append(opts, WithNamer(namer))
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
