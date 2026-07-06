package account

import (
	"testing"
)

func TestNewAccountPool(t *testing.T) {
	pool := NewAccountPool()
	if pool == nil {
		t.Fatal("NewAccountPool() returned nil")
	}
	if len(pool.All()) != 0 {
		t.Fatalf("expected empty pool, got %d accounts", len(pool.All()))
	}
}

func TestNewAccountPool_WithAccounts(t *testing.T) {
	a1 := &Account{Name: "a", Provider: ProviderOpenAI, Priority: 2}
	a2 := &Account{Name: "b", Provider: ProviderAnthropic, Priority: 1}
	pool := NewAccountPool(a1, a2)

	all := pool.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(all))
	}
	// Should be sorted by priority ascending
	if all[0].Name != "b" {
		t.Fatalf("expected first account 'b' (priority 1), got %q", all[0].Name)
	}
	if all[1].Name != "a" {
		t.Fatalf("expected second account 'a' (priority 2), got %q", all[1].Name)
	}
}

func TestAccountPool_Add(t *testing.T) {
	pool := NewAccountPool()
	pool.Add(&Account{Name: "a", Priority: 2})
	pool.Add(&Account{Name: "b", Priority: 1})

	all := pool.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(all))
	}
	if all[0].Name != "b" {
		t.Fatalf("expected 'b' first (priority 1), got %q", all[0].Name)
	}
}

func TestAccountPool_Get_Empty(t *testing.T) {
	pool := NewAccountPool()
	acct := pool.Get()
	if acct != nil {
		t.Fatalf("expected nil from empty pool, got %v", acct)
	}
}

func TestAccountPool_Get_RoundRobin(t *testing.T) {
	a1 := &Account{Name: "a", Provider: ProviderOpenAI, Priority: 1}
	a2 := &Account{Name: "b", Provider: ProviderOpenAI, Priority: 1}
	pool := NewAccountPool(a1, a2)

	// Round-robin should cycle through accounts
	got1 := pool.Get()
	got2 := pool.Get()
	got3 := pool.Get()

	if got1 == nil || got2 == nil || got3 == nil {
		t.Fatal("Get() returned nil unexpectedly")
	}

	// With 2 accounts, sequence should be a, b, a, ...
	if got1.Name != "a" || got2.Name != "b" || got3.Name != "a" {
		t.Fatalf("expected round-robin a→b→a, got %q→%q→%q", got1.Name, got2.Name, got3.Name)
	}
}

func TestAccountPool_Get_SkipsDisabled(t *testing.T) {
	a1 := &Account{Name: "a", Provider: ProviderOpenAI, Priority: 1, Disabled: true}
	a2 := &Account{Name: "b", Provider: ProviderOpenAI, Priority: 2}
	pool := NewAccountPool(a1, a2)

	for i := 0; i < 5; i++ {
		acct := pool.Get()
		if acct == nil {
			t.Fatal("Get() returned nil")
		}
		if acct.Name != "b" {
			t.Fatalf("expected 'b', got %q", acct.Name)
		}
	}
}

func TestAccountPool_Get_AllDisabled(t *testing.T) {
	a1 := &Account{Name: "a", Provider: ProviderOpenAI, Priority: 1, Disabled: true}
	a2 := &Account{Name: "b", Provider: ProviderOpenAI, Priority: 2, Disabled: true}
	pool := NewAccountPool(a1, a2)

	acct := pool.Get()
	if acct != nil {
		t.Fatalf("expected nil when all disabled, got %v", acct)
	}
}

func TestAccountPool_GetByProvider(t *testing.T) {
	a1 := &Account{Name: "openai-1", Provider: ProviderOpenAI, Priority: 1}
	a2 := &Account{Name: "anthropic-1", Provider: ProviderAnthropic, Priority: 1}
	pool := NewAccountPool(a1, a2)

	acct := pool.GetByProvider(ProviderAnthropic)
	if acct == nil {
		t.Fatal("GetByProvider(Anthropic) returned nil")
	}
	if acct.Name != "anthropic-1" {
		t.Fatalf("expected 'anthropic-1', got %q", acct.Name)
	}
}

func TestAccountPool_GetByProvider_NotFound(t *testing.T) {
	a1 := &Account{Name: "openai-1", Provider: ProviderOpenAI, Priority: 1}
	pool := NewAccountPool(a1)

	acct := pool.GetByProvider(ProviderAnthropic)
	if acct != nil {
		t.Fatalf("expected nil for unmatched provider, got %v", acct)
	}
}

func TestAccountPool_GetByProvider_SkipsDisabled(t *testing.T) {
	a1 := &Account{Name: "openai-1", Provider: ProviderOpenAI, Priority: 1, Disabled: true}
	a2 := &Account{Name: "openai-2", Provider: ProviderOpenAI, Priority: 2}
	pool := NewAccountPool(a1, a2)

	acct := pool.GetByProvider(ProviderOpenAI)
	if acct == nil {
		t.Fatal("GetByProvider(OpenAI) returned nil")
	}
	if acct.Name != "openai-2" {
		t.Fatalf("expected 'openai-2', got %q", acct.Name)
	}
}

func TestAccountPool_RoundRobin(t *testing.T) {
	a1 := &Account{Name: "a", Provider: ProviderOpenAI, Priority: 1}
	a2 := &Account{Name: "b", Provider: ProviderOpenAI, Priority: 2}
	a3 := &Account{Name: "c", Provider: ProviderOpenAI, Priority: 3}
	pool := NewAccountPool(a1, a2, a3)

	// Sequence: a→b→c→a→b→c
	seq := []string{"a", "b", "c", "a", "b", "c"}
	for i, expected := range seq {
		acct := pool.Get()
		if acct == nil {
			t.Fatalf("step %d: Get() returned nil", i)
		}
		if acct.Name != expected {
			t.Fatalf("step %d: expected %q, got %q", i, expected, acct.Name)
		}
	}
}

func TestAccountPool_All_ReturnsCopy(t *testing.T) {
	a1 := &Account{Name: "a", Priority: 1}
	pool := NewAccountPool(a1)

	all := pool.All()
	all[0] = &Account{Name: "modified"}
	// Original should be unchanged
	if pool.All()[0].Name != "a" {
		t.Fatal("All() did not return a copy")
	}
}
