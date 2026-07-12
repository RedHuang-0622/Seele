package api

import (
	"sync"
	"testing"
	"time"
)

func TestAccountAllowBasic(t *testing.T) {
	a := &Account{Name: "test", MaxRPM: 2}
	if !a.allow() {
		t.Error("first allow should return true")
	}
	if !a.allow() {
		t.Error("second allow should return true")
	}
	if a.allow() {
		t.Error("third allow should return false (RPM limit reached)")
	}
}

func TestAccountAllowUnlimited(t *testing.T) {
	a := &Account{Name: "unlimited", MaxRPM: 0}
	for i := 0; i < 100; i++ {
		if !a.allow() {
			t.Fatalf("allow should always return true when MaxRPM=0, failed at iteration %d", i)
		}
	}
}

func TestAccountAllowSlidingWindowExpiration(t *testing.T) {
	a := &Account{Name: "sliding", MaxRPM: 2}

	// Pre-populate the window with expired entries (older than 1 minute).
	a.mu.Lock()
	a.window = []time.Time{
		time.Now().Add(-2 * time.Minute),
		time.Now().Add(-90 * time.Second),
	}
	a.mu.Unlock()

	// allow() should clean expired entries and permit a new request.
	if !a.allow() {
		t.Error("expected allow after cleaning expired window entries")
	}
	// The window now has 1 entry (the one just added).
	if !a.allow() {
		t.Error("expected allow, still under limit")
	}
	// The window now has 2 entries, hitting the RPM limit.
	if a.allow() {
		t.Error("expected deny after hitting RPM limit")
	}
}

func TestAccountAllowConcurrent(t *testing.T) {
	a := &Account{Name: "concurrent", MaxRPM: 200}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.allow()
		}()
	}
	wg.Wait()
	// No data race expected. Verified with `go test -race`.
}

func TestNewAccountPoolSortsByPriority(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "high", Priority: 10},
		&Account{Name: "mid", Priority: 5},
		&Account{Name: "low", Priority: 1},
	)
	all := pool.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 accounts, got %d", len(all))
	}
	if all[0].Name != "low" {
		t.Errorf("expected first account 'low' (priority 1), got %q", all[0].Name)
	}
	if all[1].Name != "mid" {
		t.Errorf("expected second account 'mid' (priority 5), got %q", all[1].Name)
	}
	if all[2].Name != "high" {
		t.Errorf("expected third account 'high' (priority 10), got %q", all[2].Name)
	}
}

func TestAccountPoolGetRoundRobin(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
		&Account{Name: "b", Priority: 2},
		&Account{Name: "c", Priority: 3},
	)

	got1 := pool.Get()
	if got1 == nil || got1.Name != "a" {
		t.Fatalf("expected account 'a', got %v", got1)
	}
	got2 := pool.Get()
	if got2 == nil || got2.Name != "b" {
		t.Fatalf("expected account 'b', got %v", got2)
	}
	got3 := pool.Get()
	if got3 == nil || got3.Name != "c" {
		t.Fatalf("expected account 'c', got %v", got3)
	}
	// Next round: should cycle back to 'a'
	got4 := pool.Get()
	if got4 == nil || got4.Name != "a" {
		t.Fatalf("expected account 'a' (round-robin wrap), got %v", got4)
	}
}

func TestAccountPoolGetAllDisabledReturnsNil(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
		&Account{Name: "b", Priority: 2, Disabled: true},
	)
	if got := pool.Get(); got != nil {
		t.Errorf("expected nil when all accounts disabled, got %v", got)
	}
}

func TestAccountPoolGetSomeDisabledSkipsThem(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
		&Account{Name: "b", Priority: 2},
		&Account{Name: "c", Priority: 3, Disabled: true},
	)
	got := pool.Get()
	if got == nil || got.Name != "b" {
		t.Fatalf("expected account 'b' (only non-disabled), got %v", got)
	}
}

func TestAccountPoolGetRateLimitedReturnsNextAvailable(t *testing.T) {
	a1 := &Account{Name: "limited", Priority: 1, MaxRPM: 1}
	a2 := &Account{Name: "available", Priority: 2}
	pool := NewAccountPool(a1, a2)

	// First Get returns a1 (allows the request).
	got1 := pool.Get()
	if got1 == nil || got1.Name != "limited" {
		t.Fatalf("expected 'limited', got %v", got1)
	}

	// Second Get: a1 is rate-limited, should return a2.
	got2 := pool.Get()
	if got2 == nil || got2.Name != "available" {
		t.Fatalf("expected 'available' (limited is rate-limited), got %v", got2)
	}
}

func TestAccountPoolGetByProviderFiltersByProvider(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "openai-main", Priority: 1, Provider: ProviderOpenAI},
		&Account{Name: "anthropic-main", Priority: 2, Provider: ProviderAnthropic},
	)
	got := pool.GetByProvider(ProviderAnthropic)
	if got == nil || got.Name != "anthropic-main" {
		t.Fatalf("expected 'anthropic-main', got %v", got)
	}
}

func TestAccountPoolGetByProviderNoMatchReturnsNil(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "openai-main", Priority: 1, Provider: ProviderOpenAI},
	)
	got := pool.GetByProvider(ProviderAnthropic)
	if got != nil {
		t.Errorf("expected nil for non-matching provider, got %v", got)
	}
}

func TestAccountPoolGetByProviderWithEmptyPoolReturnsNil(t *testing.T) {
	pool := NewAccountPool()
	got := pool.GetByProvider(ProviderOpenAI)
	if got != nil {
		t.Errorf("expected nil for empty pool, got %v", got)
	}
}

func TestAccountPoolAddSortsAgain(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "first", Priority: 10},
	)
	pool.Add(&Account{Name: "second", Priority: 1})
	all := pool.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(all))
	}
	if all[0].Name != "second" {
		t.Errorf("expected 'second' first (priority 1), got %q", all[0].Name)
	}
	if all[1].Name != "first" {
		t.Errorf("expected 'first' second (priority 10), got %q", all[1].Name)
	}
}

func TestAccountPoolAllReturnsCopy(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
		&Account{Name: "b", Priority: 2},
	)
	// Get All, then append to the returned slice.
	copied := pool.All()
	_ = append(copied, &Account{Name: "extra", Priority: 3})

	// The original pool should still have 2 items (not 3).
	all := pool.All()
	if len(all) != 2 {
		t.Errorf("expected pool to still have 2 accounts, got %d", len(all))
	}
	// The returned slice is a copy; modifying an element's fields will
	// still affect the underlying Account pointer (shallow copy).
	if all[0].Name != "a" {
		t.Errorf("expected first account 'a', got %q", all[0].Name)
	}
}

func TestAccountPoolSelectByName(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
		&Account{Name: "b", Priority: 2},
	)
	got := pool.Select("b")
	if got == nil || got.Name != "b" {
		t.Fatalf("expected account 'b', got %v", got)
	}
	// The next Get should return the selected account (round-robin wraps).
	next := pool.Get()
	if next == nil || next.Name != "b" {
		t.Fatalf("expected 'b' as next Get after Select, got %v", next)
	}
}

func TestAccountPoolSelectDisabledReturnsNil(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
		&Account{Name: "b", Priority: 2},
	)
	got := pool.Select("a")
	if got != nil {
		t.Errorf("expected nil for disabled account, got %v", got)
	}
	// current should remain unchanged; Get should still return 'b'.
	next := pool.Get()
	if next == nil || next.Name != "b" {
		t.Fatalf("expected 'b' as next Get, got %v", next)
	}
}

func TestAccountPoolSelectNotFoundReturnsNil(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
	)
	got := pool.Select("nonexistent")
	if got != nil {
		t.Errorf("expected nil for non-existent account, got %v", got)
	}
}

func TestAccountPoolCurrentReturnsCurrentAccount(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1},
		&Account{Name: "b", Priority: 2},
	)
	cur := pool.Current()
	if cur == nil || cur.Name != "a" {
		t.Fatalf("expected 'a' as current, got %v", cur)
	}
}

func TestAccountPoolCurrentWithEmptyPoolReturnsNil(t *testing.T) {
	pool := NewAccountPool()
	if cur := pool.Current(); cur != nil {
		t.Errorf("expected nil for empty pool, got %v", cur)
	}
}

func TestAccountPoolCurrentSkipsDisabled(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
		&Account{Name: "b", Priority: 2},
	)
	cur := pool.Current()
	if cur == nil || cur.Name != "b" {
		t.Fatalf("expected 'b' (skipping disabled 'a'), got %v", cur)
	}
}

func TestAccountPoolGetWithEmptyPoolReturnsNil(t *testing.T) {
	pool := NewAccountPool()
	if got := pool.Get(); got != nil {
		t.Errorf("expected nil for empty pool, got %v", got)
	}
}

func TestAccountPoolNextIndexSkipsDisabled(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
		&Account{Name: "b", Priority: 2},
		&Account{Name: "c", Priority: 3, Disabled: true},
	)
	pool.mu.Lock()
	idx := pool.nextIndex()
	pool.mu.Unlock()
	if idx == -1 {
		t.Fatal("expected non-negative index")
	}
	if pool.accounts[idx].Name != "b" {
		t.Errorf("expected index for 'b', got index %d (account %q)", idx, pool.accounts[idx].Name)
	}
}

func TestAccountPoolNextIndexAllDisabled(t *testing.T) {
	pool := NewAccountPool(
		&Account{Name: "a", Priority: 1, Disabled: true},
	)
	pool.mu.Lock()
	idx := pool.nextIndex()
	pool.mu.Unlock()
	if idx != -1 {
		t.Errorf("expected -1 when all disabled, got %d", idx)
	}
}

func TestAccountPoolNextIndexEmptyPool(t *testing.T) {
	pool := NewAccountPool()
	pool.mu.Lock()
	idx := pool.nextIndex()
	pool.mu.Unlock()
	if idx != -1 {
		t.Errorf("expected -1 for empty pool, got %d", idx)
	}
}
