package limits

import (
	"time"
)

// WithEnabled returns a copy with the master switch set.
func (p Params) WithEnabled(enabled bool) Params {
	p.Enabled = enabled
	return p
}

// WithConcurrency returns a copy with a new in-flight cap.
func (p Params) WithConcurrency(max int) Params {
	p.MaxConcurrency = max
	return p
}

// WithImageWeight returns a copy with a new per-image weight.
func (p Params) WithImageWeight(weight float64) Params {
	p.ImageWeight = weight
	return p
}

// WithRate returns a copy with new rates. Both bursts are reset so Normalize
// re-derives them from the new rates; use WithBurst afterwards to pin explicit
// capacities.
func (p Params) WithRate(requestsPerMin, tokensPerMin float64) Params {
	p.RequestsPerMin = requestsPerMin
	p.TokensPerMin = tokensPerMin
	p.Burst = 0
	p.TokenBurst = 0
	return p
}

// WithBurst returns a copy with explicit bucket capacities.
func (p Params) WithBurst(requests, tokens int) Params {
	p.Burst = requests
	p.TokenBurst = tokens
	return p
}

// WithQueueTimeout returns a copy with a new admission budget.
func (p Params) WithQueueTimeout(timeout time.Duration) Params {
	p.QueueTimeout = Duration(timeout)
	return p
}

// WithRetry returns a copy with a new retry policy.
func (p Params) WithRetry(policy RetryPolicy) Params {
	p.Retry = policy
	return p
}

// WithPerKey returns a copy with per-key gates enabled or disabled.
func (p Params) WithPerKey(perKey bool) Params {
	p.PerKey = perKey
	return p
}
