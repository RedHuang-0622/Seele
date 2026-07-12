package apigw

import (
	"context"
	"testing"

	"github.com/RedHuang-0622/Seele/agent/core/api"
)

func TestNewDefaultGateway(t *testing.T) {
	pool := api.NewAccountPool()
	g := NewDefaultGateway(pool)
	if g == nil {
		t.Fatal("NewDefaultGateway returned nil")
	}
}

func TestDefaultGatewaySelect(t *testing.T) {
	pool := api.NewAccountPool(
		&api.Account{Name: "acct1", Provider: api.ProviderOpenAI, Priority: 1},
		&api.Account{Name: "acct2", Provider: api.ProviderAnthropic, Priority: 2},
	)
	g := NewDefaultGateway(pool)

	acct, err := g.Select(context.Background())
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if acct == nil {
		t.Fatal("Select returned nil account")
	}
	if acct.Name != "acct1" {
		t.Errorf("expected first account 'acct1', got %q", acct.Name)
	}
}

func TestDefaultGatewayHealth(t *testing.T) {
	pool := api.NewAccountPool(
		&api.Account{Name: "alpha", Provider: api.ProviderOpenAI, Priority: 1},
		&api.Account{Name: "beta", Provider: api.ProviderAnthropic, Priority: 2},
	)
	g := NewDefaultGateway(pool)

	health := g.Health(context.Background())
	if len(health) != 2 {
		t.Fatalf("expected 2 accounts in health, got %d", len(health))
	}
	for name, err := range health {
		if err != nil {
			t.Errorf("account %q should be healthy, got: %v", name, err)
		}
	}
	if _, ok := health["alpha"]; !ok {
		t.Error("missing 'alpha' in health")
	}
	if _, ok := health["beta"]; !ok {
		t.Error("missing 'beta' in health")
	}
}

func TestDefaultGatewayRegister(t *testing.T) {
	pool := api.NewAccountPool()
	g := NewDefaultGateway(pool)

	g.Register(&api.Account{Name: "new-acct", Provider: api.ProviderOpenAI, Priority: 1})

	health := g.Health(context.Background())
	if len(health) != 1 {
		t.Fatalf("expected 1 account after Register, got %d", len(health))
	}
	if _, ok := health["new-acct"]; !ok {
		t.Error("missing registered account in health")
	}
}

func TestDefaultGatewaySelectRoundRobin(t *testing.T) {
	pool := api.NewAccountPool(
		&api.Account{Name: "a", Provider: api.ProviderOpenAI, Priority: 1},
		&api.Account{Name: "b", Provider: api.ProviderOpenAI, Priority: 2},
	)
	g := NewDefaultGateway(pool)

	first, _ := g.Select(context.Background())
	second, _ := g.Select(context.Background())
	if first == nil || second == nil {
		t.Fatal("Select returned nil on round-robin")
	}
	if first.Name == second.Name {
		t.Error("expected different accounts across round-robin calls")
	}
}

// Test that *DefaultGateway satisfies the Gateway interface at compile time.
func TestGatewayInterfaceCompileCheck(t *testing.T) {
	var _ Gateway = (*DefaultGateway)(nil)
}
