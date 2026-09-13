// Package limits provides request-rate limiting and concurrency admission for
// LLM calls, together with the assembly that wires them onto any
// types.ChatCompleter.
//
// Division of responsibility with accountpool:
//   - accountpool owns *account slot* concurrency: one lease per in-flight call
//     per account, with per-account MaxConcurrency.
//   - limits owns *provider cost* admission: request rate (RPM), token rate
//     (TPM), an optional weighted in-flight cap that also charges image-bearing
//     calls, queue-timeout semantics, and 429/Retry-After retry classification.
//
// Parameter rules (deliberate, so callers are never surprised):
//   - Zero means unlimited for every rate and concurrency field. Normalize only
//     derives values that cannot be expressed (bucket burst) and never invents
//     a cap; DefaultParams carries the recommended starting policy.
//   - Every parameter is runtime-mutable through the Set* methods; changing
//     them never drops an in-flight request.
package limits

import (
	"fmt"
	"math"
	"time"

	"gopkg.in/yaml.v3"
)

// Recommended defaults. DefaultParams returns these; Normalize never applies
// them, so an explicitly configured zero stays "unlimited".
const (
	DefaultImageWeight = 0.5
	DefaultMaxAttempts = 3
	DefaultBaseDelay   = time.Second
	DefaultMaxDelay    = 60 * time.Second
	DefaultJitter      = 0.2
)

// burstWindow is how much of the configured rate a full bucket holds when
// Burst/TokenBurst are left at zero. Ten seconds smooths short spikes without
// admitting a whole minute of traffic at once.
const burstWindow = 10 * time.Second

// minTokenBurst keeps a zero-derived token bucket usable even for very low
// token rates; a single large prompt may still exceed it and is handled by the
// automatic burst raise documented on Gate.Acquire.
const minTokenBurst = 1024

// Params is the complete parameter model for rate limiting and concurrency.
// It is YAML/JSON-tagged so the same struct can be filled from Seele config,
// a product config file, or programmatically by a Set* call.
type Params struct {
	// Enabled is the master switch. When false the gate admits everything and
	// records stats only; retry classification stays active because it is a
	// transport concern rather than a quota.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// MaxConcurrency caps weighted in-flight requests per gate. Zero means
	// unlimited.
	MaxConcurrency int `yaml:"max_concurrency" json:"max_concurrency"`

	// ImageWeight is the additional in-flight weight of one image attachment.
	// Zero disables image weighting; negative values are rejected.
	ImageWeight float64 `yaml:"image_weight" json:"image_weight"`

	// RequestsPerMin caps request starts per minute. Zero means unlimited.
	RequestsPerMin float64 `yaml:"requests_per_min" json:"requests_per_min"`

	// TokensPerMin caps token consumption per minute. Zero means unlimited.
	TokensPerMin float64 `yaml:"tokens_per_min" json:"tokens_per_min"`

	// Burst is the request bucket capacity. Zero derives burstWindow worth of
	// RequestsPerMin (at least one request).
	Burst int `yaml:"burst" json:"burst"`

	// TokenBurst is the token bucket capacity. Zero derives burstWindow worth
	// of TokensPerMin (at least minTokenBurst).
	TokenBurst int `yaml:"token_burst" json:"token_burst"`

	// QueueTimeout bounds the total time a call may wait for admission.
	// Zero means fail immediately instead of queueing.
	QueueTimeout Duration `yaml:"queue_timeout" json:"queue_timeout"`

	// Retry classifies retryable provider failures.
	Retry RetryPolicy `yaml:"retry" json:"retry"`

	// PerKey makes an Assembly keep one gate per key (for example one per
	// account) instead of a single shared gate.
	PerKey bool `yaml:"per_key" json:"per_key"`
}

// RetryPolicy describes retry behaviour for retryable provider failures.
// MaxAttempts counts the first attempt, so 1 disables retry.
type RetryPolicy struct {
	MaxAttempts       int      `yaml:"max_attempts" json:"max_attempts"`
	BaseDelay         Duration `yaml:"base_delay" json:"base_delay"`
	MaxDelay          Duration `yaml:"max_delay" json:"max_delay"`
	Jitter            float64  `yaml:"jitter" json:"jitter"`
	IgnoreRetryAfter  bool     `yaml:"ignore_retry_after" json:"ignore_retry_after"`
	RetryableStatuses []int    `yaml:"retryable_statuses" json:"retryable_statuses"`
}

// DefaultParams returns the recommended starting policy. It is the only place
// that applies these values; Normalize leaves an explicit zero untouched.
func DefaultParams() Params {
	return Params{
		Enabled:        true,
		MaxConcurrency: 4,
		ImageWeight:    DefaultImageWeight,
		RequestsPerMin: 60,
		TokensPerMin:   120000,
		QueueTimeout:   Duration(30 * time.Second),
		Retry: RetryPolicy{
			MaxAttempts: DefaultMaxAttempts,
			BaseDelay:   Duration(DefaultBaseDelay),
			MaxDelay:    Duration(DefaultMaxDelay),
			Jitter:      DefaultJitter,
		},
		PerKey: true,
	}
}

// Normalize returns a copy with derived values filled in: bucket capacities are
// derived from the configured rates when left at zero.
//
// It never raises a cap, never enables anything the caller left disabled, and
// never silently repairs a negative value — negatives are rejected by Validate
// so a typo cannot turn into a different policy.
func (p Params) Normalize() Params {
	if p.Burst == 0 && p.RequestsPerMin > 0 {
		p.Burst = int(math.Ceil(p.RequestsPerMin * burstWindow.Seconds() / 60))
		if p.Burst < 1 {
			p.Burst = 1
		}
	}
	if p.TokenBurst == 0 && p.TokensPerMin > 0 {
		p.TokenBurst = int(math.Ceil(p.TokensPerMin * burstWindow.Seconds() / 60))
		if p.TokenBurst < minTokenBurst {
			p.TokenBurst = minTokenBurst
		}
	}
	return p
}

// Validate rejects values that cannot be honoured. It expects a normalized
// value (call Normalize first) so that a zero burst is already derived.
func (p Params) Validate() error {
	switch {
	case p.MaxConcurrency < 0:
		return fmt.Errorf("%w: max_concurrency must be >= 0, got %d", ErrInvalidParams, p.MaxConcurrency)
	case p.ImageWeight < 0:
		return fmt.Errorf("%w: image_weight must be >= 0, got %v", ErrInvalidParams, p.ImageWeight)
	case p.RequestsPerMin < 0:
		return fmt.Errorf("%w: requests_per_min must be >= 0, got %v", ErrInvalidParams, p.RequestsPerMin)
	case p.TokensPerMin < 0:
		return fmt.Errorf("%w: tokens_per_min must be >= 0, got %v", ErrInvalidParams, p.TokensPerMin)
	case p.Retry.MaxAttempts < 0:
		return fmt.Errorf("%w: retry.max_attempts must be >= 0, got %d", ErrInvalidParams, p.Retry.MaxAttempts)
	case p.Retry.BaseDelay < 0:
		return fmt.Errorf("%w: retry.base_delay must be >= 0", ErrInvalidParams)
	case p.Retry.MaxDelay < 0:
		return fmt.Errorf("%w: retry.max_delay must be >= 0", ErrInvalidParams)
	case p.Retry.Jitter < 0 || p.Retry.Jitter > 1:
		return fmt.Errorf("%w: retry.jitter must be within [0,1], got %v", ErrInvalidParams, p.Retry.Jitter)
	case p.Burst < 0:
		return fmt.Errorf("%w: burst must be >= 0, got %d", ErrInvalidParams, p.Burst)
	case p.TokenBurst < 0:
		return fmt.Errorf("%w: token_burst must be >= 0, got %d", ErrInvalidParams, p.TokenBurst)
	case p.QueueTimeout < 0:
		return fmt.Errorf("%w: queue_timeout must be >= 0, got %s", ErrInvalidParams, p.QueueTimeout.Duration())
	case p.RequestsPerMin > 0 && p.Burst == 0:
		return fmt.Errorf("%w: requests_per_min is set but burst derives to zero", ErrInvalidParams)
	case p.TokensPerMin > 0 && p.TokenBurst == 0:
		return fmt.Errorf("%w: tokens_per_min is set but token_burst derives to zero", ErrInvalidParams)
	}
	if p.Retry.MaxDelay > 0 && p.Retry.BaseDelay > p.Retry.MaxDelay {
		return fmt.Errorf("%w: retry.base_delay %s exceeds retry.max_delay %s", ErrInvalidParams, p.Retry.BaseDelay.Duration(), p.Retry.MaxDelay.Duration())
	}
	return nil
}

// RequestsRate is the request bucket rate expressed per second.
func (p Params) RequestsRate() float64 { return p.RequestsPerMin / 60 }

// TokensRate is the token bucket rate expressed per second.
func (p Params) TokensRate() float64 { return p.TokensPerMin / 60 }

// Duration is a time.Duration that round-trips through YAML/JSON as either a
// duration string ("30s", "1m30s") or a bare number of seconds (30). Bare
// numbers are accepted because product configs commonly store seconds.
type Duration time.Duration

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// UnmarshalYAML accepts "30s" and 30 (seconds).
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if d == nil {
		return fmt.Errorf("%w: nil duration receiver", ErrInvalidParams)
	}
	switch node.Tag {
	case "!!int", "!!float":
		var seconds float64
		if err := node.Decode(&seconds); err != nil {
			return fmt.Errorf("duration: %w", err)
		}
		if seconds < 0 {
			return fmt.Errorf("%w: negative duration %v", ErrInvalidParams, seconds)
		}
		*d = Duration(time.Duration(seconds * float64(time.Second)))
		return nil
	case "!!str":
		parsed, err := time.ParseDuration(node.Value)
		if err != nil {
			return fmt.Errorf("%w: parse duration %q: %w", ErrInvalidParams, node.Value, err)
		}
		if parsed < 0 {
			return fmt.Errorf("%w: negative duration %q", ErrInvalidParams, node.Value)
		}
		*d = Duration(parsed)
		return nil
	default:
		return fmt.Errorf("%w: unsupported duration tag %q", ErrInvalidParams, node.Tag)
	}
}

// MarshalYAML writes the duration back as a string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// MarshalJSON writes the duration as a string for JSON consumers.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}
