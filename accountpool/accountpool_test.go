package accountpool

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestP2CSelectorChoosesLowerMetric(t *testing.T) {
	metric := LoadMetricFunc(func(snapshot AccountSnapshot) float64 {
		if snapshot.ID == "fast" {
			return 0.1
		}
		return 0.9
	})
	selector := NewP2CSelector(metric, rand.NewSource(7))
	candidates := []AccountSnapshot{{ID: "fast"}, {ID: "slow"}}
	for i := 0; i < 20; i++ {
		selected, err := selector.Select(candidates)
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		if selected.ID != "fast" {
			t.Fatalf("Select() = %q, want fast", selected.ID)
		}
	}
}

func TestPoolP2CUsesCurrentSemaphoreLoad(t *testing.T) {
	selector := NewP2CSelector(nil, rand.NewSource(11))
	pool := New[string](WithSelector(selector))
	for _, id := range []string{"loaded", "idle"} {
		if err := pool.Register(Account[string]{ID: id, Value: id, MaxConcurrency: 2}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "loaded"})
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Release()

	selected, err := pool.Acquire(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Release()
	if selected.AccountID() != "idle" {
		t.Fatalf("P2C selected %q, want idle", selected.AccountID())
	}
}

func TestPoolMetadataPredicateAndStats(t *testing.T) {
	pool := New[string]()
	if err := pool.Register(Account[string]{
		ID:             "openai",
		Value:          "opaque-client",
		MaxConcurrency: 2,
		Metadata:       map[string]string{"provider": "openai", "model": "gpt"},
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Acquire(context.Background(), AcquireRequest{
		Metadata: map[string]string{"provider": "openai"},
		Predicate: func(snapshot AccountSnapshot) bool {
			return snapshot.Metadata["model"] == "gpt"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Client() != "opaque-client" {
		t.Fatalf("Client() = %q, want opaque-client", lease.Client())
	}
	if got := lease.Snapshot().Active; got != 1 {
		t.Fatalf("Active = %d, want 1", got)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	stats := pool.Stats()
	if stats.Total != 1 || stats.Enabled != 1 || stats.Active != 0 || stats.Available != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	snapshot, err := pool.Snapshot("openai")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Metadata["provider"] = "mutated"
	unchanged, _ := pool.Snapshot("openai")
	if unchanged.Metadata["provider"] != "openai" {
		t.Fatal("Snapshot metadata must be copied")
	}
	value, lookupSnapshot, err := pool.Lookup("openai")
	if err != nil || value != "opaque-client" || lookupSnapshot.ID != "openai" {
		t.Fatalf("Lookup() = %q/%+v/%v", value, lookupSnapshot, err)
	}
	entries := pool.Entries()
	if len(entries) != 1 || entries[0].Value != "opaque-client" {
		t.Fatalf("Entries() = %+v", entries)
	}
}

func TestAcquireDeadlineAndIdempotentRelease(t *testing.T) {
	pool := New[string]()
	if err := pool.Register(Account[string]{ID: "one", Value: "client", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	first, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = pool.Acquire(ctx, AcquireRequest{AccountID: "one"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire() error = %v, want deadline exceeded", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if !first.Released() {
		t.Fatal("Released() = false after Release")
	}
	second, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
}

func TestDisableEnableAndPinnedErrors(t *testing.T) {
	pool := New[string]()
	if err := pool.Register(Account[string]{ID: "one", Value: "client", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Disable("one"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "one"})
	if !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("Acquire() error = %v, want ErrAccountDisabled", err)
	}
	if err := pool.Enable("one"); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if err := pool.Unregister("one"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Acquire(context.Background(), AcquireRequest{AccountID: "one"})
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("Acquire() error = %v, want ErrAccountNotFound", err)
	}
}

func TestFactoryAndStaticResolver(t *testing.T) {
	factory := ClientFactoryFunc[string, string](func(_ context.Context, config string) (string, error) {
		return "client:" + config, nil
	})
	account, err := MaterializeAccount(context.Background(), AccountSpec[string]{
		ID:             "factory",
		Config:         "entry",
		MaxConcurrency: 1,
		Metadata:       map[string]string{"provider": "custom"},
	}, factory)
	if err != nil {
		t.Fatal(err)
	}
	pool := New[string]()
	if err := pool.Register(account); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Resolve(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Client() != "client:entry" {
		t.Fatalf("factory client = %q", lease.Client())
	}
	lease.Close()

	static := NewStaticResolver("hard-coded", "direct-client")
	staticLease, err := static.Resolve(context.Background(), AcquireRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if staticLease.AccountID() != "hard-coded" || staticLease.Client() != "direct-client" {
		t.Fatalf("unexpected static lease: %q/%q", staticLease.AccountID(), staticLease.Client())
	}
	staticLease.Close()
}

func TestConcurrentCapacityAndWakeups(t *testing.T) {
	pool := New[string]()
	for _, id := range []string{"a", "b", "c"} {
		if err := pool.Register(Account[string]{ID: id, Value: id, MaxConcurrency: 2}); err != nil {
			t.Fatal(err)
		}
	}

	var active atomic.Int64
	var maxActive atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			lease, err := pool.Acquire(ctx, AcquireRequest{})
			if err != nil {
				t.Errorf("Acquire() error = %v", err)
				return
			}
			current := active.Add(1)
			for {
				old := maxActive.Load()
				if current <= old || maxActive.CompareAndSwap(old, current) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			if err := lease.Close(); err != nil {
				t.Errorf("Close() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := maxActive.Load(); got > 6 {
		t.Fatalf("global active leases = %d, want <= 6", got)
	}
	if got := pool.Stats().Active; got != 0 {
		t.Fatalf("active leases after workers = %d, want 0", got)
	}
}

func TestUnregisterRejectsActiveLease(t *testing.T) {
	pool := New[string]()
	if err := pool.Register(Account[string]{ID: "busy", Value: "client", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Acquire(context.Background(), AcquireRequest{AccountID: "busy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Unregister("busy"); !errors.Is(err, ErrAccountBusy) {
		t.Fatalf("Unregister() error = %v, want ErrAccountBusy", err)
	}
	lease.Release()
	if err := pool.Unregister("busy"); err != nil {
		t.Fatal(err)
	}
}
