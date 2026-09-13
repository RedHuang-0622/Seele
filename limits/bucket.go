package limits

import (
	"fmt"
	"sync"
	"time"
)

// ErrInvalidParams reports a parameter combination that cannot be honoured.
var ErrInvalidParams = fmt.Errorf("limits: invalid params")

// bucket is a monotonic token bucket. rate is tokens per second; 0 means the
// bucket is unlimited and every take succeeds. All state is guarded by mu.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time

	// oversized counts requests bigger than the whole bucket that had to be
	// absorbed as debt, so a mis-sized configuration stays visible.
	oversized int
}

func newBucket(ratePerSecond, capacity float64, now func() time.Time) *bucket {
	if now == nil {
		now = time.Now
	}
	b := &bucket{rate: ratePerSecond, burst: capacity, tokens: capacity, now: now}
	b.last = now()
	return b
}

// unlimited reports whether the bucket imposes no limit.
func (b *bucket) unlimited() bool { return b.rate <= 0 }

// refill adds elapsed tokens, capped at burst. Caller holds mu.
func (b *bucket) refill() {
	now := b.now()
	if b.rate <= 0 {
		b.last = now
		b.tokens = b.burst
		return
	}
	elapsed := now.Sub(b.last)
	if elapsed <= 0 {
		return
	}
	b.last = now
	b.tokens += b.rate * elapsed.Seconds()
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// set replaces the rate and capacity at runtime. The token level is clamped to
// the new capacity so a shrunk bucket cannot keep an over-sized allowance.
func (b *bucket) set(ratePerSecond, capacity float64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	b.rate = ratePerSecond
	b.burst = capacity
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	if b.tokens < 0 {
		b.tokens = 0
	}
	if capacity <= 0 {
		b.burst = 0
		b.tokens = 0
	}
}

// tryTake deducts amount when the bucket allows it. When it does not, it
// returns the time to wait before retrying and leaves the level untouched.
//
// A single request larger than the whole bucket is absorbed immediately as
// debt instead of waiting: waiting could never be satisfied faster than one
// refill cycle, and a large prompt must not be gated by a capacity derived from
// the rate. The debt is really paid back by the refill of subsequent requests,
// and the event is counted so a mis-sized configuration stays visible.
func (b *bucket) tryTake(amount float64) (ok bool, wait time.Duration) {
	if amount <= 0 {
		return true, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlimited() {
		return true, 0
	}
	b.refill()
	if amount > b.burst {
		b.tokens -= amount
		b.oversized++
		return true, 0
	}
	if b.tokens >= amount {
		b.tokens -= amount
		return true, 0
	}
	missing := amount - b.tokens
	wait = time.Duration(missing / b.rate * float64(time.Second))
	if wait <= 0 {
		wait = time.Millisecond
	}
	return false, wait
}

// give returns tokens to the bucket (used when an admission is rolled back or
// when real usage settles below the estimate).
func (b *bucket) give(amount float64) {
	if amount <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlimited() {
		return
	}
	b.refill()
	b.tokens += amount
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
}

// charge takes a settled overrun without waiting, returning the part of the
// debt the bucket could not absorb.
func (b *bucket) charge(amount float64) float64 {
	if amount <= 0 {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.unlimited() {
		return 0
	}
	b.refill()
	b.tokens -= amount
	if b.tokens >= 0 {
		return 0
	}
	debt := -b.tokens
	b.tokens = 0
	return debt
}

// level returns the current token level (for stats).
func (b *bucket) level() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens
}

// capacity returns the current capacity (for stats).
func (b *bucket) capacity() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.burst
}

// ratePerSecond returns the configured rate (for stats).
func (b *bucket) ratePerSecond() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rate
}

// raiseCount returns how often an oversized request had to be absorbed as
// debt instead of being served from the bucket allowance.
func (b *bucket) raiseCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.oversized
}
