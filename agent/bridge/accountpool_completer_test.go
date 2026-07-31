package bridge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/accountpool"
	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/types"
)

type completionFunc func(context.Context, []types.Message, []types.Tool) (types.Message, error)

func (f completionFunc) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return f(ctx, messages, tools)
}

type leaseResolver struct {
	lease   accountpool.ClientLease[agent.Completer]
	request accountpool.AcquireRequest
	err     error
}

func (r *leaseResolver) Resolve(_ context.Context, request accountpool.AcquireRequest) (accountpool.ClientLease[agent.Completer], error) {
	r.request = request
	return r.lease, r.err
}

type observedLease struct {
	id       string
	client   agent.Completer
	released int
	err      error
}

func (l *observedLease) AccountID() string       { return l.id }
func (l *observedLease) Client() agent.Completer { return l.client }
func (l *observedLease) Release() error {
	l.released++
	return l.err
}
func (l *observedLease) Close() error { return l.Release() }

func TestAccountCompleterAcquiresSelectsAndReleases(t *testing.T) {
	content := "ok"
	lease := &observedLease{
		id: "account-a",
		client: completionFunc(func(_ context.Context, messages []types.Message, _ []types.Tool) (types.Message, error) {
			if len(messages) != 1 || messages[0].Role != "user" {
				t.Fatalf("messages = %#v", messages)
			}
			return types.Message{Role: "assistant", Content: &content}, nil
		}),
	}
	resolver := &leaseResolver{lease: lease}
	completer, err := NewAccountCompleter(resolver, WithAccountRequestSelector(func(_ context.Context, _ []types.Message, _ []types.Tool) accountpool.AcquireRequest {
		return accountpool.AcquireRequest{AccountID: "account-a"}
	}))
	if err != nil {
		t.Fatalf("NewAccountCompleter() error = %v", err)
	}
	got, err := completer.Complete(context.Background(), []types.Message{{Role: "user"}}, nil)
	if err != nil || got.Content == nil || *got.Content != "ok" {
		t.Fatalf("Complete() = %#v, %v", got, err)
	}
	if resolver.request.AccountID != "account-a" {
		t.Fatalf("AcquireRequest = %#v", resolver.request)
	}
	if lease.released != 1 {
		t.Fatalf("Release calls = %d, want 1", lease.released)
	}
}

func TestAccountCompleterReleasesOnCompletionError(t *testing.T) {
	lease := &observedLease{
		id: "account-a",
		client: completionFunc(func(context.Context, []types.Message, []types.Tool) (types.Message, error) {
			return types.Message{}, errors.New("provider unavailable")
		}),
	}
	completer, err := NewAccountCompleter(&leaseResolver{lease: lease})
	if err != nil {
		t.Fatalf("NewAccountCompleter() error = %v", err)
	}
	if _, err := completer.Complete(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("Complete() error = %v", err)
	}
	if lease.released != 1 {
		t.Fatalf("Release calls = %d, want 1", lease.released)
	}
}

func TestAccountCompleterRejectsNilResolver(t *testing.T) {
	if _, err := NewAccountCompleter(nil); err == nil {
		t.Fatal("NewAccountCompleter(nil) error = nil")
	}
	var resolver *leaseResolver
	if _, err := NewAccountCompleter(resolver); err == nil {
		t.Fatal("NewAccountCompleter(typed nil) error = nil")
	}
}
