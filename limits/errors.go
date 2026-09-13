package limits

import "errors"

// Sentinel errors returned by admission. Callers can match them with
// errors.Is; every one of them is safe to retry later, which is exactly the
// information a UI needs to say "queued" instead of "broken".
var (
	// ErrConcurrencyExceeded reports that no weighted in-flight slot became
	// available within the queue timeout.
	ErrConcurrencyExceeded = errors.New("limits: concurrency limit exceeded")

	// ErrRateLimited reports that the request-rate bucket did not refill
	// within the queue timeout.
	ErrRateLimited = errors.New("limits: request rate limited")

	// ErrQueueTimeout reports that admission exceeded QueueTimeout. It wraps
	// either ErrConcurrencyExceeded or ErrRateLimited, so errors.Is matches
	// both the coarse cause and the timeout itself.
	ErrQueueTimeout = errors.New("limits: admission queue timeout")
)
