package api

import (
	"context"
	"errors"
	"testing"
	"time"

	rootpool "github.com/RedHuang-0622/Seele/accountpool"
)

func TestNewAccountPoolUsesDefaultConcurrencyAndPriorityInspectionOrder(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "later", Priority: 10},
		&Account{Name: "first", Priority: 1, MaxConcurrency: 3},
	)
	all := pool.All()
	if len(all) != 2 {
		t.Fatalf("All() len = %d, want 2", len(all))
	}
	if all[0].Name != "first" || all[0].MaxConcurrency != 3 {
		t.Fatalf("first account = %+v", all[0])
	}
	if all[1].Name != "later" || all[1].MaxConcurrency != DefaultMaxConcurrency {
		t.Fatalf("default concurrency account = %+v", all[1])
	}
}

func TestAccountPoolAcquireHoldsSemaphoreUntilRelease(t *testing.T) {
	pool := NewAccountPool(&Account{Name: "only", MaxConcurrency: 1})
	first, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = pool.Acquire(ctx, rootpool.AcquireRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want deadline exceeded", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestAccountPoolProviderFilterAndPinnedAccount(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "openai", Provider: ProviderOpenAI},
		&Account{Name: "anthropic", Provider: ProviderAnthropic},
	)
	providerLease, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{
		Metadata: map[string]string{"provider": string(ProviderAnthropic)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if providerLease.Client().Name != "anthropic" {
		t.Fatalf("provider lease = %q", providerLease.Client().Name)
	}
	providerLease.Release()

	pinned, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Client().Name != "openai" {
		t.Fatalf("pinned lease = %q", pinned.Client().Name)
	}
	pinned.Release()
}

func TestAccountPoolDisableEnableUsesRootState(t *testing.T) {
	pool := NewAccountPool(&Account{Name: "account"})
	if err := pool.Disable("account"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "account"})
	if !errors.Is(err, rootpool.ErrAccountDisabled) {
		t.Fatalf("Acquire() error = %v, want disabled", err)
	}
	all := pool.All()
	if len(all) != 1 || !all[0].Disabled {
		t.Fatalf("All() did not reflect root disabled state: %+v", all)
	}
	if err := pool.Enable("account"); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "account"})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
}

func TestAccountPoolP2CPrefersLowerSemaphoreLoad(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "loaded", MaxConcurrency: 2},
		&Account{Name: "idle", MaxConcurrency: 2},
	)
	loaded, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "loaded"})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Release()
	selected, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Release()
	if selected.Client().Name != "idle" {
		t.Fatalf("P2C selected %q, want idle", selected.Client().Name)
	}
}

func TestAccountPoolRegistrationErrorsAndLegacyInspection(t *testing.T) {
	pool := NewAccountPool()
	if err := pool.Register(nil); !errors.Is(err, rootpool.ErrInvalidAccount) {
		t.Fatalf("Register(nil) error = %v", err)
	}
	account := &Account{Name: "a", Provider: ProviderOpenAI}
	if err := pool.Register(account); err != nil {
		t.Fatal(err)
	}
	if err := pool.Register(account); !errors.Is(err, rootpool.ErrAccountExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if got := pool.GetByProvider(ProviderOpenAI); got == nil || got.Name != "a" {
		t.Fatalf("GetByProvider() = %+v", got)
	}
	if got := pool.Select("a"); got == nil || got.Name != "a" {
		t.Fatalf("Select() = %+v", got)
	}
	if got := pool.Current(); got == nil || got.Name != "a" {
		t.Fatalf("Current() = %+v", got)
	}
}
