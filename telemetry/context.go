package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// TraceContext is the propagation-only identity carried by context.Context.
type TraceContext struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
}

type traceContextKey struct{}

// ContextWithTrace attaches trace identity without exposing tracer implementation state.
func ContextWithTrace(ctx context.Context, trace TraceContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceContextKey{}, trace)
}

// TraceFromContext returns the current trace identity.
func TraceFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	trace, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return trace, ok && trace.TraceID != "" && trace.SpanID != ""
}

func newIdentifier(byteCount int) (string, error) {
	if byteCount <= 0 {
		return "", fmt.Errorf("identifier byte count must be positive: %d", byteCount)
	}
	buf := make([]byte, byteCount)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
