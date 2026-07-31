package accountpool

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

type accountState[T any] struct {
	id       string
	value    T
	metadata map[string]string
	sem      chan struct{}

	mu       sync.Mutex
	disabled bool
	removed  bool
}

func newAccountState[T any](account Account[T]) *accountState[T] {
	return &accountState[T]{
		id:       account.ID,
		value:    account.Value,
		metadata: cloneMetadata(account.Metadata),
		sem:      make(chan struct{}, account.MaxConcurrency),
	}
}

func (s *accountState[T]) snapshot() AccountSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := len(s.sem)
	capacity := cap(s.sem)
	disabled := s.disabled || s.removed
	available := capacity - active
	if disabled {
		available = 0
	}
	return AccountSnapshot{
		ID:             s.id,
		MaxConcurrency: capacity,
		Active:         active,
		Available:      available,
		Disabled:       disabled,
		Load:           float64(active) / float64(capacity),
		Metadata:       cloneMetadata(s.metadata),
	}
}

func (s *accountState[T]) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled || s.removed {
		return false
	}
	select {
	case s.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *accountState[T]) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.sem:
	default:
	}
}

// P2CPool stores account states in sync.Map and uses a channel semaphore per
// account. It contains no provider, credential, or transport semantics.
type P2CPool[T any] struct {
	accounts sync.Map
	selector Selector

	wakeMu sync.Mutex
	wake   chan struct{}
}

// Option configures a P2CPool without coupling it to a client type.
type Option func(*poolOptions)

type poolOptions struct {
	selector Selector
}

// WithSelector replaces the default occupancy-based P2C selector.
func WithSelector(selector Selector) Option {
	return func(options *poolOptions) { options.selector = selector }
}

// New constructs an empty P2C account pool.
func New[T any](options ...Option) *P2CPool[T] {
	config := poolOptions{}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.selector == nil {
		config.selector = NewP2CSelector(nil, nil)
	}
	return &P2CPool[T]{selector: config.selector, wake: make(chan struct{})}
}

// Register adds an account. Account IDs are immutable and unique.
func (p *P2CPool[T]) Register(account Account[T]) error {
	if account.ID == "" {
		return fmt.Errorf("%w: id is empty", ErrInvalidAccount)
	}
	if account.MaxConcurrency <= 0 {
		return fmt.Errorf("%w: account %q max concurrency must be positive", ErrInvalidAccount, account.ID)
	}
	if _, loaded := p.accounts.LoadOrStore(account.ID, newAccountState(account)); loaded {
		return fmt.Errorf("%w: %q", ErrAccountExists, account.ID)
	}
	p.notify()
	return nil
}

// Unregister removes an idle account. Active leases must be released first.
func (p *P2CPool[T]) Unregister(id string) error {
	state, err := p.load(id)
	if err != nil {
		return err
	}
	state.mu.Lock()
	if len(state.sem) != 0 {
		state.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrAccountBusy, id)
	}
	state.removed = true
	state.mu.Unlock()
	p.accounts.Delete(id)
	p.notify()
	return nil
}

func (p *P2CPool[T]) Enable(id string) error {
	state, err := p.load(id)
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.disabled = false
	state.mu.Unlock()
	p.notify()
	return nil
}

// Disable prevents new leases while allowing existing leases to finish.
func (p *P2CPool[T]) Disable(id string) error {
	state, err := p.load(id)
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.disabled = true
	state.mu.Unlock()
	p.notify()
	return nil
}

// Acquire waits for an eligible semaphore slot. Context cancellation and
// deadlines terminate a saturated wait without consuming capacity.
func (p *P2CPool[T]) Acquire(ctx context.Context, request AcquireRequest) (*Lease[T], error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("accountpool: acquire: %w", err)
		}
		wake := p.waitChannel()
		eligible, err := p.eligible(request)
		if err != nil {
			return nil, err
		}
		available := availableAccounts(eligible)
		if len(available) != 0 {
			selected, selectErr := p.selector.Select(available)
			if selectErr != nil {
				return nil, fmt.Errorf("accountpool: select account: %w", selectErr)
			}
			if err := validateSelection(selected, available); err != nil {
				return nil, err
			}
			state, loadErr := p.load(selected.ID)
			if loadErr == nil && state.tryAcquire() {
				return &Lease[T]{state: state, pool: p}, nil
			}
			continue
		}

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("accountpool: acquire: %w", ctx.Err())
		case <-wake:
		}
	}
}

// Resolve implements ClientResolver.
func (p *P2CPool[T]) Resolve(ctx context.Context, request AcquireRequest) (ClientLease[T], error) {
	return p.Acquire(ctx, request)
}

func (p *P2CPool[T]) Snapshot(id string) (AccountSnapshot, error) {
	state, err := p.load(id)
	if err != nil {
		return AccountSnapshot{}, err
	}
	return state.snapshot(), nil
}

// Lookup returns the opaque registered value and its current public snapshot.
// It does not reserve a semaphore slot and must not be used for dispatch.
func (p *P2CPool[T]) Lookup(id string) (T, AccountSnapshot, error) {
	state, err := p.load(id)
	if err != nil {
		var zero T
		return zero, AccountSnapshot{}, err
	}
	return state.value, state.snapshot(), nil
}

// Entries returns inspection views sorted by account ID. Values are returned
// as registered; snapshots are copied and never expose pool internals.
func (p *P2CPool[T]) Entries() []Entry[T] {
	entries := make([]Entry[T], 0)
	p.accounts.Range(func(_, value any) bool {
		state := value.(*accountState[T])
		entries = append(entries, Entry[T]{Value: state.value, Snapshot: state.snapshot()})
		return true
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Snapshot.ID < entries[j].Snapshot.ID })
	return entries
}

func (p *P2CPool[T]) Stats() Stats {
	result := Stats{}
	p.accounts.Range(func(_, value any) bool {
		snapshot := value.(*accountState[T]).snapshot()
		result.Accounts = append(result.Accounts, snapshot)
		result.Total++
		result.MaxConcurrency += snapshot.MaxConcurrency
		result.Active += snapshot.Active
		result.Available += snapshot.Available
		if snapshot.Disabled {
			result.Disabled++
		} else {
			result.Enabled++
		}
		return true
	})
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].ID < result.Accounts[j].ID
	})
	return result
}

func (p *P2CPool[T]) load(id string) (*accountState[T], error) {
	value, ok := p.accounts.Load(id)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAccountNotFound, id)
	}
	return value.(*accountState[T]), nil
}

func (p *P2CPool[T]) eligible(request AcquireRequest) ([]AccountSnapshot, error) {
	if request.AccountID != "" {
		state, err := p.load(request.AccountID)
		if err != nil {
			return nil, err
		}
		snapshot := state.snapshot()
		if snapshot.Disabled {
			return nil, fmt.Errorf("%w: %q", ErrAccountDisabled, request.AccountID)
		}
		if !matches(snapshot, request) {
			return nil, fmt.Errorf("%w: pinned account %q does not match filters", ErrNoEligibleAccount, request.AccountID)
		}
		return []AccountSnapshot{snapshot}, nil
	}

	var snapshots []AccountSnapshot
	p.accounts.Range(func(_, value any) bool {
		snapshot := value.(*accountState[T]).snapshot()
		if !snapshot.Disabled && matches(snapshot, request) {
			snapshots = append(snapshots, snapshot)
		}
		return true
	})
	if len(snapshots) == 0 {
		return nil, ErrNoEligibleAccount
	}
	return snapshots, nil
}

func matches(snapshot AccountSnapshot, request AcquireRequest) bool {
	for key, value := range request.Metadata {
		if snapshot.Metadata[key] != value {
			return false
		}
	}
	return request.Predicate == nil || request.Predicate(snapshot)
}

func availableAccounts(accounts []AccountSnapshot) []AccountSnapshot {
	available := make([]AccountSnapshot, 0, len(accounts))
	for _, account := range accounts {
		if account.Available > 0 {
			available = append(available, account)
		}
	}
	return available
}

func (p *P2CPool[T]) waitChannel() <-chan struct{} {
	p.wakeMu.Lock()
	defer p.wakeMu.Unlock()
	return p.wake
}

func (p *P2CPool[T]) notify() {
	p.wakeMu.Lock()
	close(p.wake)
	p.wake = make(chan struct{})
	p.wakeMu.Unlock()
}
