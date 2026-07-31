package accountpool

import (
	"context"
	"fmt"
)

// ClientFactory creates a client from caller-owned configuration. accountpool
// deliberately does not prescribe API keys, endpoints, or provider formats.
type ClientFactory[Config, Client any] interface {
	Create(context.Context, Config) (Client, error)
}

// ClientFactoryFunc adapts a function into ClientFactory.
type ClientFactoryFunc[Config, Client any] func(context.Context, Config) (Client, error)

func (f ClientFactoryFunc[Config, Client]) Create(ctx context.Context, config Config) (Client, error) {
	return f(ctx, config)
}

// AccountSpec couples caller-owned client configuration with generic pool
// registration attributes.
type AccountSpec[Config any] struct {
	ID             string
	Config         Config
	MaxConcurrency int
	Metadata       map[string]string
}

// MaterializeAccount uses an injected factory to build a pool account.
func MaterializeAccount[Config, Client any](
	ctx context.Context,
	spec AccountSpec[Config],
	factory ClientFactory[Config, Client],
) (Account[Client], error) {
	if factory == nil {
		var zero Account[Client]
		return zero, fmt.Errorf("%w: client factory is nil", ErrInvalidAccount)
	}
	client, err := factory.Create(ctx, spec.Config)
	if err != nil {
		var zero Account[Client]
		return zero, fmt.Errorf("accountpool: create client for %q: %w", spec.ID, err)
	}
	return Account[Client]{
		ID:             spec.ID,
		Value:          client,
		MaxConcurrency: spec.MaxConcurrency,
		Metadata:       cloneMetadata(spec.Metadata),
	}, nil
}

// StaticResolver adapts a hard-coded client to the same resolver contract used
// by P2CPool. Its leases have no concurrency accounting.
type StaticResolver[T any] struct {
	id     string
	client T
}

func NewStaticResolver[T any](id string, client T) *StaticResolver[T] {
	return &StaticResolver[T]{id: id, client: client}
}

func (r *StaticResolver[T]) Resolve(ctx context.Context, request AcquireRequest) (ClientLease[T], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.AccountID != "" && request.AccountID != r.id {
		return nil, fmt.Errorf("%w: %q", ErrAccountNotFound, request.AccountID)
	}
	snapshot := AccountSnapshot{ID: r.id, MaxConcurrency: 1, Available: 1}
	if !matches(snapshot, request) {
		return nil, fmt.Errorf("%w: static account %q does not match filters", ErrNoEligibleAccount, r.id)
	}
	return &staticLease[T]{id: r.id, client: r.client}, nil
}

type staticLease[T any] struct {
	id     string
	client T
}

func (l *staticLease[T]) AccountID() string { return l.id }
func (l *staticLease[T]) Client() T         { return l.client }
func (l *staticLease[T]) Release() error    { return nil }
func (l *staticLease[T]) Close() error      { return l.Release() }
