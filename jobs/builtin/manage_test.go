package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/jobs"
)

type scopeKey struct{}

func newTestManager(t *testing.T, executor jobs.Executor) jobs.Manager {
	t.Helper()
	manager, err := jobs.New(
		jobs.WithExecutor(executor),
		jobs.WithScopeResolver(func(ctx context.Context) jobs.Scope {
			scope, _ := ctx.Value(scopeKey{}).(jobs.Scope)
			return scope
		}),
	)
	if err != nil {
		t.Fatalf("jobs.New: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

// call runs the jobs_manage handler and decodes the response.
func call(t *testing.T, provider *Provider, ctx context.Context, payload string) (manageResponse, error) {
	t.Helper()
	entry := provider.Tools()[0]
	raw, err := entry.Handler.Execute(ctx, payload)
	if err != nil {
		return manageResponse{}, err
	}
	var response manageResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, raw)
	}
	return response, nil
}

func TestProviderExposesOneManagementTool(t *testing.T) {
	manager := newTestManager(t, jobs.ExecutorFunc{JobKind: jobs.KindInline})
	provider := New(manager)
	if provider.ProviderName() != ProviderName {
		t.Fatalf("provider name = %q", provider.ProviderName())
	}
	tools := provider.Tools()
	if len(tools) != 1 || tools[0].Definition.Function.Name != ManageToolName {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if tools[0].Meta == nil || tools[0].Meta.Bits == 0 {
		t.Fatal("jobs_manage must declare permission bits")
	}
}

func TestManageToolLifecycle(t *testing.T) {
	release := make(chan struct{})
	executor := jobs.ExecutorFunc{
		JobKind: jobs.KindInline,
		Run: func(ctx context.Context, spec jobs.Spec, sink jobs.Sink) error {
			sink.Note("hello\n")
			<-release
			sink.Note("bye\n")
			sink.Complete(jobs.StateDone, "ok")
			return nil
		},
	}
	manager := newTestManager(t, executor)
	provider := New(manager)
	ctx := context.WithValue(context.Background(), scopeKey{}, jobs.Scope{Session: "s1"})
	handle, err := manager.Dispatch(ctx, jobs.Spec{Kind: jobs.KindInline, Scope: jobs.Scope{Session: "s1"}, Node: "impl", Description: "unit"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	observed, err := call(t, provider, ctx, `{"op":"observe","handle":"`+string(handle)+`"}`)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if observed.State != "running" || observed.Node != "impl" || observed.Description != "unit" {
		t.Fatalf("observe response = %+v", observed)
	}

	fetched, err := call(t, provider, ctx, `{"op":"fetch","handle":"`+string(handle)+`","wait_ms":-1}`)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if fetched.Output != "hello\n" {
		t.Fatalf("first fetch output = %q", fetched.Output)
	}

	listed, err := call(t, provider, ctx, `{"op":"observe"}`)
	if err != nil {
		t.Fatalf("observe list: %v", err)
	}
	if listed.Count != 1 || len(listed.Jobs) != 1 {
		t.Fatalf("scope listing = %+v", listed)
	}

	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := call(t, provider, ctx, `{"op":"fetch","handle":"`+string(handle)+`"}`)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if response.State != "running" {
			if response.Output != "bye\n" || response.State != "done" {
				t.Fatalf("terminal fetch = %+v", response)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not terminate")
		}
	}

	// After delivering the last byte the job is retired: a further fetch must
	// report the retired handle rather than pretend it never existed.
	if _, err := call(t, provider, ctx, `{"op":"fetch","handle":"`+string(handle)+`"}`); !errors.Is(err, jobs.ErrRetired) {
		t.Fatalf("fetch after retire err = %v, want ErrRetired", err)
	}
	if _, err := call(t, provider, ctx, `{"op":"done","handle":"`+string(handle)+`"}`); err != nil {
		t.Fatalf("done after retire: %v", err)
	}
}

func TestManageToolRefusesUnknownAndCrossScope(t *testing.T) {
	release := make(chan struct{})
	executor := jobs.ExecutorFunc{
		JobKind: jobs.KindInline,
		Run: func(ctx context.Context, spec jobs.Spec, sink jobs.Sink) error {
			<-release
			sink.Complete(jobs.StateDone, "")
			return nil
		},
	}
	manager := newTestManager(t, executor)
	provider := New(manager)
	owner := context.WithValue(context.Background(), scopeKey{}, jobs.Scope{Session: "s1", Subject: "emp_a"})
	handle, err := manager.Dispatch(owner, jobs.Spec{Kind: jobs.KindInline, Scope: jobs.Scope{Session: "s1", Subject: "emp_a"}, Description: "x"})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, err := call(t, provider, owner, `{"op":"fetch"}`); err == nil {
		t.Fatal("fetch without handle must fail")
	}
	if _, err := call(t, provider, owner, `{"op":"nonsense","handle":"a1"}`); err == nil {
		t.Fatal("unknown op must fail")
	}
	if _, err := call(t, provider, owner, `{"op":"observe","handle":"a999"}`); err == nil {
		t.Fatal("unknown handle must fail")
	}
	intruder := context.WithValue(context.Background(), scopeKey{}, jobs.Scope{Session: "s2"})
	if _, err := call(t, provider, intruder, `{"op":"kill","handle":"`+string(handle)+`"}`); !errors.Is(err, jobs.ErrCrossScope) {
		t.Fatalf("cross-scope kill err = %v, want ErrCrossScope", err)
	}
	close(release)
}

func TestManageToolRejectsUnknownField(t *testing.T) {
	manager := newTestManager(t, jobs.ExecutorFunc{JobKind: jobs.KindInline})
	provider := New(manager)
	if _, err := call(t, provider, context.Background(), `{"op":"observe","bogus":1}`); err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("unknown field err = %v", err)
	}
}
