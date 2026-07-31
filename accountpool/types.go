package accountpool

import (
	"context"
	"sync"
	"sync/atomic"
)

// Account registers an opaque client or client entry with a concurrency limit.
// Metadata is intended for non-sensitive routing attributes. Credentials should
// remain inside Value because snapshots expose Metadata to observers.
type Account[T any] struct {
	ID             string
	Value          T
	MaxConcurrency int
	Metadata       map[string]string
}

// AcquireRequest constrains account selection. Predicate must be safe for
// concurrent invocation and should not mutate its snapshot argument.
type AcquireRequest struct {
	AccountID string
	Metadata  map[string]string
	Predicate func(AccountSnapshot) bool
}

// AccountSnapshot is an observational view of one account. It never exposes
// the registered opaque Value.
type AccountSnapshot struct {
	ID             string
	MaxConcurrency int
	Active         int
	Available      int
	Disabled       bool
	Load           float64
	Metadata       map[string]string
}

// Stats is a point-in-time, non-transactional view of the whole pool.
type Stats struct {
	Total          int
	Enabled        int
	Disabled       int
	MaxConcurrency int
	Active         int
	Available      int
	Accounts       []AccountSnapshot
}

// Entry is an inspection view combining an opaque value with its public
// snapshot. It is intended for configuration/UI/health adapters, not for
// dispatch; dispatch must always use Acquire so the semaphore is held.
type Entry[T any] struct {
	Value    T
	Snapshot AccountSnapshot
}

// ClientLease is the narrow contract consumed by agent/client adapters.
// Close and Release are equivalent and idempotent.
type ClientLease[T any] interface {
	AccountID() string
	Client() T
	Release() error
	Close() error
}

// ClientResolver hides whether a client comes from a pool or is hard-coded.
type ClientResolver[T any] interface {
	Resolve(context.Context, AcquireRequest) (ClientLease[T], error)
}

// Pool is the account registration and lease allocation contract.
type Pool[T any] interface {
	ClientResolver[T]
	Register(Account[T]) error
	Unregister(string) error
	Enable(string) error
	Disable(string) error
	Acquire(context.Context, AcquireRequest) (*Lease[T], error)
	Snapshot(string) (AccountSnapshot, error)
	Lookup(string) (T, AccountSnapshot, error)
	Entries() []Entry[T]
	Stats() Stats
}

// Lease owns one account semaphore slot until Release or Close is called.
// A Lease must not be copied after first use.
type Lease[T any] struct {
	state      *accountState[T]
	pool       *P2CPool[T]
	once       sync.Once
	released   atomic.Bool
	releaseErr error
}

func (l *Lease[T]) AccountID() string { return l.state.id }

// Client returns the opaque registered value. It may be a ready client or a
// client entry understood by an adapter outside accountpool.
func (l *Lease[T]) Client() T { return l.state.value }

// Value is an explicit alias for Client for non-client resource pools.
func (l *Lease[T]) Value() T { return l.state.value }

// Snapshot returns the current account load without exposing its Value.
func (l *Lease[T]) Snapshot() AccountSnapshot { return l.state.snapshot() }

// Released reports whether Release or Close has already run.
func (l *Lease[T]) Released() bool { return l.released.Load() }

// Release returns the semaphore slot to the pool. Repeated calls are harmless.
func (l *Lease[T]) Release() error {
	l.once.Do(func() {
		l.state.release()
		l.released.Store(true)
		l.pool.notify()
	})
	return l.releaseErr
}

// Close is an io.Closer-compatible alias for Release.
func (l *Lease[T]) Close() error { return l.Release() }

func cloneMetadata(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
