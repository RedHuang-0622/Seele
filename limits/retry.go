package limits

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// defaultRetryableStatuses is the status set treated as transient when a retry
// policy does not list its own.
var defaultRetryableStatuses = []int{429, 500, 502, 503, 504}

// StatusOf extracts the provider HTTP status from an error chain. It prefers
// the typed form reported by Seele's api client and falls back to the stable
// "HTTP <code>" prefix so other clients behave identically.
func StatusOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var statusErr *types.HTTPStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		return statusErr.StatusCode, true
	}
	message := err.Error()
	index := strings.Index(message, "HTTP ")
	if index < 0 {
		return 0, false
	}
	digits := message[index+len("HTTP "):]
	code := 0
	seen := 0
	for seen < len(digits) && digits[seen] >= '0' && digits[seen] <= '9' {
		code = code*10 + int(digits[seen]-'0')
		seen++
	}
	if seen == 0 {
		return 0, false
	}
	return code, true
}

// RetryAfterOf extracts a provider-supplied Retry-After delay when present.
func RetryAfterOf(err error) (time.Duration, bool) {
	var statusErr *types.HTTPStatusError
	if errors.As(err, &statusErr) && statusErr != nil && statusErr.RetryAfter > 0 {
		return statusErr.RetryAfter, true
	}
	return 0, false
}

// IsRetryable reports whether err is worth retrying at all, ignoring policy
// limits. Cancellation and deadline errors from the caller's context are never
// retryable: the caller has already given up.
func IsRetryable(err error, policy RetryPolicy) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if status, ok := StatusOf(err); ok {
		return containsStatus(policy, status)
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"rate limit", "too many requests", "connection reset", "connection refused",
		"broken pipe", "unexpected eof", "temporarily unavailable", "timeout",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func containsStatus(policy RetryPolicy, status int) bool {
	statuses := policy.RetryableStatuses
	if len(statuses) == 0 {
		statuses = defaultRetryableStatuses
	}
	for _, candidate := range statuses {
		if candidate == status {
			return true
		}
	}
	return false
}

// RetryDelay computes the wait before the next attempt.
//
// attempt is the 1-based attempt that just failed. The second return value is
// false when no further attempt should be made: either the policy allows only
// one attempt, or the failure is not retryable.
//
// randomness is a value in [0,1) used for jitter and is injected so tests are
// deterministic.
func RetryDelay(attempt int, policy RetryPolicy, err error, randomness float64) (time.Duration, bool) {
	if policy.MaxAttempts <= 1 || attempt < 1 || attempt >= policy.MaxAttempts {
		return 0, false
	}
	if !IsRetryable(err, policy) {
		return 0, false
	}
	if delay, ok := RetryAfterOf(err); ok && !policy.IgnoreRetryAfter {
		return clampDelay(delay, policy), true
	}

	base := policy.BaseDelay.Duration()
	if base <= 0 {
		base = DefaultBaseDelay
	}
	delay := base
	for i := 1; i < attempt; i++ {
		delay *= 2
		if max := policy.MaxDelay.Duration(); max > 0 && delay >= max {
			delay = max
			break
		}
	}
	if jitter := policy.Jitter; jitter > 0 {
		if randomness < 0 {
			randomness = 0
		}
		if randomness >= 1 {
			randomness = 0.999
		}
		factor := 1 + jitter*(2*randomness-1)
		if factor < 0 {
			factor = 0
		}
		delay = time.Duration(float64(delay) * factor)
	}
	return clampDelay(delay, policy), true
}

func clampDelay(delay time.Duration, policy RetryPolicy) time.Duration {
	if delay < 0 {
		delay = 0
	}
	if max := policy.MaxDelay.Duration(); max > 0 && delay > max {
		delay = max
	}
	return delay
}
