package api

import (
	"context"
	"fmt"
	"sort"

	rootpool "github.com/RedHuang-0622/Seele/accountpool"
)

// ProviderType identifies an LLM wire-protocol provider.
type ProviderType string

const (
	ProviderOpenAI    ProviderType = "openai"
	ProviderAnthropic ProviderType = "anthropic"

	// DefaultMaxConcurrency is used when an account entry omits
	// max_concurrency. A conservative default prevents accidental unlimited
	// fan-out; callers can raise it per account.
	DefaultMaxConcurrency = 1
)

// Account is the client-specific opaque value stored in the root accountpool.
// Runtime load and enabled state live only in rootpool.P2CPool.
type Account struct {
	Name           string
	Provider       ProviderType
	BaseURL        string
	APIKey         string
	Model          string
	Priority       int
	MaxRPM         int // Deprecated: request-rate policy belongs outside accountpool.
	Disabled       bool
	MaxConcurrency int
	MaxTokens      int
	Timeout        int
	Temperature    float64
}

// AccountPool is a client-specific adapter over the root P2C lease pool. It
// owns no account slice, round-robin cursor, limiter, or duplicate load state.
type AccountPool struct {
	inner *rootpool.P2CPool[*Account]
}

func NewAccountPool(accounts ...*Account) *AccountPool {
	pool := &AccountPool{inner: rootpool.New[*Account]()}
	for _, account := range accounts {
		_ = pool.Register(account)
	}
	return pool
}

// Register adds an account and applies its initial disabled state.
func (p *AccountPool) Register(account *Account) error {
	if account == nil {
		return fmt.Errorf("%w: account is nil", rootpool.ErrInvalidAccount)
	}
	capacity := account.MaxConcurrency
	if capacity <= 0 {
		capacity = DefaultMaxConcurrency
	}
	account.MaxConcurrency = capacity
	if err := p.inner.Register(rootpool.Account[*Account]{
		ID:             account.Name,
		Value:          account,
		MaxConcurrency: capacity,
		Metadata: map[string]string{
			"provider": string(account.Provider),
			"base_url": account.BaseURL,
			"model":    account.Model,
		},
	}); err != nil {
		return err
	}
	if account.Disabled {
		return p.inner.Disable(account.Name)
	}
	return nil
}

// Add is the legacy void adapter. New code should use Register and handle its
// error. It does not create a second state source.
func (p *AccountPool) Add(account *Account) { _ = p.Register(account) }

// Acquire reserves one account slot until the returned lease is closed.
func (p *AccountPool) Acquire(
	ctx context.Context,
	request rootpool.AcquireRequest,
) (*rootpool.Lease[*Account], error) {
	return p.inner.Acquire(ctx, request)
}

func (p *AccountPool) Enable(name string) error  { return p.inner.Enable(name) }
func (p *AccountPool) Disable(name string) error { return p.inner.Disable(name) }

func (p *AccountPool) Stats() rootpool.Stats { return p.inner.Stats() }

// Core exposes the root pool for adapters that already consume its contracts.
func (p *AccountPool) Core() *rootpool.P2CPool[*Account] { return p.inner }

// All returns client configuration copies for inspection, sorted by legacy
// priority then name. Dispatch must use Acquire instead.
func (p *AccountPool) All() []*Account {
	entries := p.inner.Entries()
	accounts := make([]*Account, 0, len(entries))
	for _, entry := range entries {
		copy := *entry.Value
		copy.Disabled = entry.Snapshot.Disabled
		copy.MaxConcurrency = entry.Snapshot.MaxConcurrency
		accounts = append(accounts, &copy)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Priority == accounts[j].Priority {
			return accounts[i].Name < accounts[j].Name
		}
		return accounts[i].Priority < accounts[j].Priority
	})
	return accounts
}

// Lookup returns an inspection copy without reserving capacity.
func (p *AccountPool) Lookup(name string) (*Account, bool) {
	account, snapshot, err := p.inner.Lookup(name)
	if err != nil {
		return nil, false
	}
	copy := *account
	copy.Disabled = snapshot.Disabled
	copy.MaxConcurrency = snapshot.MaxConcurrency
	return &copy, true
}

// Get is a compatibility inspection helper. It briefly acquires and releases a
// lease; request execution must retain the lease itself via Acquire.
func (p *AccountPool) Get() *Account {
	lease, err := p.Acquire(context.Background(), rootpool.AcquireRequest{})
	if err != nil {
		return nil
	}
	defer lease.Release()
	return lease.Client()
}

func (p *AccountPool) GetByProvider(provider ProviderType) *Account {
	lease, err := p.Acquire(context.Background(), rootpool.AcquireRequest{
		Metadata: map[string]string{"provider": string(provider)},
	})
	if err != nil {
		return nil
	}
	defer lease.Release()
	return lease.Client()
}

// Select validates a legacy account name but no longer mutates global pool
// routing. ChatClient.SelectAccount stores the pin on that client instance.
func (p *AccountPool) Select(name string) *Account {
	account, ok := p.Lookup(name)
	if !ok || account.Disabled {
		return nil
	}
	return account
}

// Current returns the first enabled inspection entry for compatibility. P2C
// has no global current cursor.
func (p *AccountPool) Current() *Account {
	for _, account := range p.All() {
		if !account.Disabled {
			return account
		}
	}
	return nil
}
