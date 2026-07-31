// Package builtin provides optional, product-neutral tools implemented by Seele.
//
// Callers decide whether to register this provider. The package does not read a
// workspace, execute commands, inspect Git repositories, or depend on Agent.
package builtin

import (
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

const ProviderName = "seele_builtin"

// Clock is the minimum time source needed by the get_time tool.
type Clock interface {
	Now() time.Time
}

// ClockFunc adapts a function to Clock.
type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Option configures the builtin provider without introducing global state.
type Option func(*Provider)

// WithClock replaces the time source used by get_time. It is useful for
// deterministic tests and for hosts that own their wall-clock policy.
func WithClock(clock Clock) Option {
	return func(provider *Provider) {
		if clock != nil {
			provider.clock = clock
		}
	}
}

// Provider exposes Seele's optional product-neutral builtin tools.
type Provider struct {
	clock Clock
}

// New constructs a builtin provider. Construction has no side effects and
// does not register the provider globally.
func New(options ...Option) *Provider {
	provider := &Provider{clock: systemClock{}}
	for _, option := range options {
		if option != nil {
			option(provider)
		}
	}
	return provider
}

func (p *Provider) ProviderName() string { return ProviderName }

func (p *Provider) Tools() []roottools.ToolEntry {
	return []roottools.ToolEntry{
		newGetTimeEntry(p.clock),
		newCalculateEntry(),
		newTextStatsEntry(),
	}
}
