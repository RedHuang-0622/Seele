package types

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPStatusError is a typed provider HTTP failure.
//
// It exists so retry and rate-limit layers can classify a failure without
// importing the HTTP client or matching strings: the error text is kept
// identical to the historical fmt.Errorf("HTTP %d: %.512s", ...) form, so
// existing logs and tests are unaffected, while status and Retry-After become
// available through errors.As.
type HTTPStatusError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration
}

// NewHTTPStatusError builds the typed error from a provider response. header
// may be nil; a Retry-After header is parsed as either delta-seconds or an
// HTTP date.
func NewHTTPStatusError(statusCode int, body []byte, header http.Header) *HTTPStatusError {
	return &HTTPStatusError{
		StatusCode: statusCode,
		Body:       string(body),
		RetryAfter: parseRetryAfter(header),
	}
}

// Error keeps the pre-existing message shape: "HTTP 429: {body}".
func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "HTTP error"
	}
	return fmt.Sprintf("HTTP %d: %.512s", e.StatusCode, e.Body)
}

// Retryable reports whether the status is conventionally transient.
func (e *HTTPStatusError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.StatusCode {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// parseRetryAfter reads the Retry-After header in either supported form.
func parseRetryAfter(header http.Header) time.Duration {
	if header == nil {
		return 0
	}
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
