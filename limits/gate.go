package limits

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// Stats is a point-in-time view of one gate. All counters are monotonic except
// the level/gauges, and every counter is safe to log.
type Stats struct {
	Enabled        bool    `json:"enabled"`
	MaxConcurrency int     `json:"max_concurrency"`
	ImageWeight    float64 `json:"image_weight"`
	RequestsPerMin float64 `json:"requests_per_min"`
	TokensPerMin   float64 `json:"tokens_per_min"`

	InFlight     int     `json:"in_flight"`      // weighted slots in use
	Waiting      int     `json:"waiting"`        // callers inside admission
	PeakInFlight int     `json:"peak_in_flight"` // highest weighted slots seen
	RequestLevel float64 `json:"request_level"`  // request bucket tokens left
	RequestBurst float64 `json:"request_burst"`
	TokenLevel   float64 `json:"token_level"` // token bucket tokens left
	TokenBurst   float64 `json:"token_burst"`

	Admitted      int64         `json:"admitted"`
	Completed     int64         `json:"completed"`
	Requests      int64         `json:"requests"`
	Images        int64         `json:"images"`
	Estimated     int64         `json:"estimated_tokens"`
	Actual        int64         `json:"actual_tokens"`
	TokenDebt     int64         `json:"token_debt"`
	RateWaited    int64         `json:"rate_waited"`
	RateWaitNanos time.Duration `json:"rate_wait_nanos"`
	QueueWaited   int64         `json:"queue_waited"`
	QueueWait     time.Duration `json:"queue_wait"`
	PeakQueueWait time.Duration `json:"peak_queue_wait"`
	QueueTimeouts int64         `json:"queue_timeouts"`
	Rejected      int64         `json:"rejected"`
	Retried       int64         `json:"retried"`
	Oversized     int64         `json:"oversized_requests"`
}

// Gate is one admission point: a weighted in-flight cap plus a request-rate
// bucket and a token-rate bucket, all runtime-mutable.
//
// Acquire order is in-flight slot, then request rate, then token rate. Taking
// the local slot first keeps the shared rate buckets accurate (no token is
// consumed while queueing) and the slot is released again if a rate wait fails.
type Gate struct {
	key string

	mu     sync.Mutex
	params Params
	wake   chan struct{}

	active float64
	stats  Stats

	rpm *bucket
	tpm *bucket

	now func() time.Time
}

// NewGate builds a gate for one key (an account name, a role, or "default").
// The params are normalized and validated before use.
func NewGate(key string, params Params) (*Gate, error) {
	if key == "" {
		key = sharedKey
	}
	params = params.Normalize()
	if err := params.Validate(); err != nil {
		return nil, err
	}
	now := time.Now
	gate := &Gate{
		key:    key,
		params: params,
		wake:   make(chan struct{}),
		now:    now,
		rpm:    newBucket(params.RequestsRate(), float64(params.Burst), now),
		tpm:    newBucket(params.TokensRate(), float64(params.TokenBurst), now),
	}
	gate.stats.Enabled = params.Enabled
	gate.stats.MaxConcurrency = params.MaxConcurrency
	gate.stats.ImageWeight = params.ImageWeight
	gate.stats.RequestsPerMin = params.RequestsPerMin
	gate.stats.TokensPerMin = params.TokensPerMin
	return gate, nil
}

// setClock replaces the clock. Test-only helper kept unexported so production
// callers cannot desynchronize the buckets.
func (g *Gate) setClock(now func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now = now
	g.rpm = newBucket(g.params.RequestsRate(), float64(g.params.Burst), now)
	g.tpm = newBucket(g.params.TokensRate(), float64(g.params.TokenBurst), now)
}

// Key reports which key this gate was built for.
func (g *Gate) Key() string { return g.key }

// Params returns the active parameters.
func (g *Gate) Params() Params {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.params
}

// SetParams replaces every parameter at runtime. In-flight permits are never
// interrupted; a shrunk capacity simply admits nothing new until the weight
// drops below it, and waiters are re-checked immediately.
func (g *Gate) SetParams(params Params) error {
	params = params.Normalize()
	if err := params.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	g.params = params
	g.stats.Enabled = params.Enabled
	g.stats.MaxConcurrency = params.MaxConcurrency
	g.stats.ImageWeight = params.ImageWeight
	g.stats.RequestsPerMin = params.RequestsPerMin
	g.stats.TokensPerMin = params.TokensPerMin
	g.wakeAllLocked()
	g.mu.Unlock()

	g.rpm.set(params.RequestsRate(), float64(params.Burst))
	g.tpm.set(params.TokensRate(), float64(params.TokenBurst))
	return nil
}

// SetConcurrency changes only the weighted in-flight cap.
func (g *Gate) SetConcurrency(max int) error {
	return g.mutate(func(p Params) Params { return p.WithConcurrency(max) })
}

// SetRate changes only the request and token rates, re-deriving both bursts.
func (g *Gate) SetRate(requestsPerMin, tokensPerMin float64) error {
	return g.mutate(func(p Params) Params { return p.WithRate(requestsPerMin, tokensPerMin) })
}

// SetBurst sets both bucket capacities explicitly.
func (g *Gate) SetBurst(requests, tokens int) error {
	return g.mutate(func(p Params) Params { return p.WithBurst(requests, tokens) })
}

// SetRetry changes only the retry policy.
func (g *Gate) SetRetry(policy RetryPolicy) error {
	return g.mutate(func(p Params) Params { return p.WithRetry(policy) })
}

// SetQueueTimeout changes only the admission budget.
func (g *Gate) SetQueueTimeout(timeout time.Duration) error {
	return g.mutate(func(p Params) Params { return p.WithQueueTimeout(timeout) })
}

// SetImageWeight changes only the per-image in-flight weight.
func (g *Gate) SetImageWeight(weight float64) error {
	return g.mutate(func(p Params) Params { return p.WithImageWeight(weight) })
}

// SetEnabled toggles admission without touching the configured caps.
func (g *Gate) SetEnabled(enabled bool) error {
	return g.mutate(func(p Params) Params { return p.WithEnabled(enabled) })
}

// mutate applies a parameter transformation under the gate lock.
func (g *Gate) mutate(transform func(Params) Params) error {
	g.mu.Lock()
	next := transform(g.params)
	g.mu.Unlock()
	return g.SetParams(next)
}

// Stats returns a consistent snapshot including current bucket levels.
func (g *Gate) Stats() Stats {
	g.mu.Lock()
	snapshot := g.stats
	snapshot.Enabled = g.params.Enabled
	snapshot.MaxConcurrency = g.params.MaxConcurrency
	snapshot.ImageWeight = g.params.ImageWeight
	snapshot.RequestsPerMin = g.params.RequestsPerMin
	snapshot.TokensPerMin = g.params.TokensPerMin
	g.mu.Unlock()

	snapshot.RequestLevel = g.rpm.level()
	snapshot.RequestBurst = g.rpm.capacity()
	snapshot.TokenLevel = g.tpm.level()
	snapshot.TokenBurst = g.tpm.capacity()
	snapshot.Oversized = int64(g.rpm.raiseCount() + g.tpm.raiseCount())
	return snapshot
}

// Acquire admits one call. The returned permit must be released by the caller
// (the wrapper does this for you); Settle may be called once before Release to
// reconcile the token estimate with real usage.
func (g *Gate) Acquire(ctx context.Context, cost Cost) (*Permit, error) {
	if err := cost.validate(); err != nil {
		return nil, err
	}
	cost = cost.normalize()
	if ctx == nil {
		ctx = context.Background()
	}

	g.mu.Lock()
	params := g.params
	g.mu.Unlock()

	if !params.Enabled {
		return g.newPermit(cost, 0), nil
	}

	admitCtx, cancel, bounded := admissionContext(ctx, params)
	if bounded {
		defer cancel()
	}
	// QueueTimeout == 0 means "do not queue": admission is a single
	// non-blocking attempt and a saturated gate fails fast instead of hanging.
	queue := params.QueueTimeout.Duration() > 0

	start := g.now()
	weight := cost.Weight(params.ImageWeight)

	if err := g.acquireSlot(admitCtx, weight, queue); err != nil {
		g.recordRejected()
		return nil, g.classifyWait(ctx, err, ErrConcurrencyExceeded, "concurrency", start)
	}
	if err := g.waitBucket(admitCtx, g.rpm, float64(cost.Requests), queue); err != nil {
		g.releaseSlot(weight)
		return nil, g.classifyWait(ctx, err, ErrRateLimited, "requests_per_min", start)
	}
	if err := g.waitBucket(admitCtx, g.tpm, float64(cost.Tokens()), queue); err != nil {
		g.rpm.give(float64(cost.Requests))
		g.releaseSlot(weight)
		return nil, g.classifyWait(ctx, err, ErrRateLimited, "tokens_per_min", start)
	}

	permit := g.newPermit(cost, weight)
	wait := g.now().Sub(start)
	permit.waited = wait
	g.recordAdmitted(cost, wait)
	return permit, nil
}

// acquireSlot takes weighted in-flight capacity. With queue enabled it waits
// for a release or a capacity change using the same wake-channel pattern
// accountpool uses, so both layers behave identically under ctx cancellation.
// Without queue it fails immediately with ErrConcurrencyExceeded.
func (g *Gate) acquireSlot(ctx context.Context, weight float64, queue bool) error {
	for {
		g.mu.Lock()
		capacity := float64(g.params.MaxConcurrency)
		if capacity <= 0 || g.active+weight <= capacity {
			g.active += weight
			inFlight := int(math.Ceil(g.active))
			g.stats.InFlight = inFlight
			if inFlight > g.stats.PeakInFlight {
				g.stats.PeakInFlight = inFlight
			}
			g.mu.Unlock()
			return nil
		}
		if !queue {
			g.mu.Unlock()
			return ErrConcurrencyExceeded
		}
		wake := g.wake
		g.stats.Waiting++
		g.mu.Unlock()

		select {
		case <-ctx.Done():
			g.mu.Lock()
			g.stats.Waiting--
			g.mu.Unlock()
			return ctx.Err()
		case <-wake:
			g.mu.Lock()
			g.stats.Waiting--
			g.mu.Unlock()
		}
	}
}

// releaseSlot returns weighted in-flight capacity and wakes waiters.
func (g *Gate) releaseSlot(weight float64) {
	g.mu.Lock()
	g.active -= weight
	if g.active < 0 {
		g.active = 0
	}
	g.stats.InFlight = int(math.Ceil(g.active))
	g.wakeAllLocked()
	g.mu.Unlock()
}

// wakeAllLocked releases every waiter so each re-evaluates the current state.
// Callers must hold g.mu.
func (g *Gate) wakeAllLocked() {
	close(g.wake)
	g.wake = make(chan struct{})
}

// waitBucket consumes tokens, sleeping as the bucket refills. It counts only
// the sleeps it actually performed. When queueing is disabled, a shortfall is
// reported immediately as ErrRateLimited.
func (g *Gate) waitBucket(ctx context.Context, b *bucket, amount float64, queue bool) error {
	if amount <= 0 || b.unlimited() {
		return nil
	}
	for {
		ok, wait := b.tryTake(amount)
		if ok {
			return nil
		}
		if !queue {
			return ErrRateLimited
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		g.mu.Lock()
		g.stats.RateWaited++
		g.stats.RateWaitNanos += wait
		g.mu.Unlock()
	}
}

// classifyWait converts a wait failure into a stable, retryable error.
//
// Three outcomes are distinguished on purpose:
//   - the caller's own context ended  → propagate it (never retry);
//   - the admission budget ran out    → ErrQueueTimeout wrapping the cause;
//   - queueing was disabled           → ErrQueueTimeout wrapping the cause.
//
// Every rejection is retryable later, which is what lets a UI say "queued"
// instead of "broken".
func (g *Gate) classifyWait(ctx context.Context, err error, cause error, resource string, start time.Time) error {
	if parentErr := ctx.Err(); parentErr != nil {
		return fmt.Errorf("limits: admission canceled (%s): %w", resource, parentErr)
	}
	switch {
	case errors.Is(err, ErrConcurrencyExceeded), errors.Is(err, ErrRateLimited):
		g.recordQueueTimeout(start)
		return fmt.Errorf("%w (%s): %w", ErrQueueTimeout, resource, cause)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		g.recordQueueTimeout(start)
		return fmt.Errorf("%w (%s): %w", ErrQueueTimeout, resource, cause)
	default:
		return fmt.Errorf("%w: %s: %w", cause, resource, err)
	}
}

// recordQueueTimeout counts one admission that ran out of budget or had
// queueing disabled.
func (g *Gate) recordQueueTimeout(start time.Time) {
	wait := g.now().Sub(start)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.QueueTimeouts++
	g.stats.QueueWaited++
	g.stats.QueueWait += wait
	if wait > g.stats.PeakQueueWait {
		g.stats.PeakQueueWait = wait
	}
}

// recordRejected counts one admission refused because the caller gave up.
func (g *Gate) recordRejected() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Rejected++
}

// admissionContext applies QueueTimeout when the caller's context does not
// already expire sooner.
func admissionContext(ctx context.Context, params Params) (context.Context, context.CancelFunc, bool) {
	timeout := params.QueueTimeout.Duration()
	if timeout <= 0 {
		return ctx, func() {}, false
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return ctx, func() {}, false
	}
	admitCtx, cancel := context.WithTimeout(ctx, timeout)
	return admitCtx, cancel, true
}

// newPermit builds a permit. A zero gate means "unlimited": the permit still
// carries the cost so the wrapper can report it.
func (g *Gate) newPermit(cost Cost, weight float64) *Permit {
	return &Permit{gate: g, cost: cost, weight: weight, chargedTokens: cost.Tokens()}
}

// recordAdmitted updates admission counters.
func (g *Gate) recordAdmitted(cost Cost, wait time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Admitted++
	g.stats.Requests += int64(cost.Requests)
	g.stats.Images += int64(cost.Images)
	g.stats.Estimated += int64(cost.Tokens())
	g.stats.QueueWaited++
	g.stats.QueueWait += wait
	if wait > g.stats.PeakQueueWait {
		g.stats.PeakQueueWait = wait
	}
}

// recordRetry counts one wrapper retry.
func (g *Gate) recordRetry() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stats.Retried++
}

// Permit is one admitted call. Release is idempotent; Settle is idempotent and
// must happen before Release to be accounted.
type Permit struct {
	gate   *Gate
	cost   Cost
	weight float64
	waited time.Duration

	mu            sync.Mutex
	released      bool
	settled       bool
	chargedTokens int
}

// Cost returns the cost this permit was admitted for.
func (p *Permit) Cost() Cost { return p.cost }

// Wait reports how long admission took (queue and rate waits combined).
func (p *Permit) Wait() time.Duration { return p.waited }

// Key reports the gate key this permit belongs to.
func (p *Permit) Key() string {
	if p.gate == nil {
		return ""
	}
	return p.gate.key
}

// Settle reconciles the estimated token charge with real usage. Overruns are
// charged from the bucket without waiting and any remainder is recorded as
// TokenDebt; underruns are returned to the bucket.
func (p *Permit) Settle(usage types.Usage) {
	if p == nil || p.gate == nil {
		return
	}
	actual := usage.TotalTokens
	if actual == 0 {
		actual = usage.PromptTokens + usage.CompletionTokens
	}
	if actual < 0 {
		actual = 0
	}
	p.mu.Lock()
	if p.settled {
		p.mu.Unlock()
		return
	}
	p.settled = true
	delta := actual - p.chargedTokens
	p.mu.Unlock()

	if delta == 0 {
		return
	}
	if delta < 0 {
		p.gate.tpm.give(float64(-delta))
	} else {
		debt := p.gate.tpm.charge(float64(delta))
		if debt > 0 {
			p.gate.mu.Lock()
			p.gate.stats.TokenDebt += int64(debt)
			p.gate.mu.Unlock()
		}
	}
	p.gate.mu.Lock()
	p.gate.stats.Actual += int64(actual)
	p.gate.mu.Unlock()
}

// Release returns the in-flight slot. It is idempotent and safe to call from a
// deferred statement on every return path.
func (p *Permit) Release() {
	if p == nil || p.gate == nil {
		return
	}
	p.mu.Lock()
	if p.released {
		p.mu.Unlock()
		return
	}
	p.released = true
	p.mu.Unlock()

	if p.weight > 0 {
		p.gate.releaseSlot(p.weight)
	}
	p.gate.mu.Lock()
	p.gate.stats.Completed++
	p.gate.mu.Unlock()
}

// Released reports whether Release already ran (diagnostics and tests).
func (p *Permit) Released() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.released
}
