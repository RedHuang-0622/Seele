package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// IDGenerator creates opaque identifiers. Callers can inject one to align
// event IDs with their own tracing or persistence system.
type IDGenerator interface {
	NewID() (string, error)
}

// IDGeneratorFunc adapts a function to IDGenerator.
type IDGeneratorFunc func() (string, error)

// NewID implements IDGenerator.
func (f IDGeneratorFunc) NewID() (string, error) {
	if f == nil {
		return "", fmt.Errorf("event ID generator is nil")
	}
	return f()
}

type randomIDGenerator struct{}

func (randomIDGenerator) NewID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("read event ID entropy: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// Clock is injected for deterministic tests and specialized runtimes.
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// ErrorHandler receives Sink delivery failures. Observation failures never
// change the control-flow result of the operation that emitted an event.
type ErrorHandler func(context.Context, error)

// Recorder normalizes events from one runtime execution. It serializes Sink
// delivery, so events of the same recorder reach a synchronous Sink in strict
// Sequence order even when producers execute concurrently.
type Recorder struct {
	mu          sync.Mutex
	sink        Sink
	scope       Scope
	locations   []Location
	source      string
	idGenerator IDGenerator
	clock       Clock
	onError     ErrorHandler
	sequence    uint64
	closed      bool
	heartbeats  *HeartbeatManager
}

// RecorderOption configures one independently-owned Recorder instance.
type RecorderOption func(*recorderConfig)

type recorderConfig struct {
	scope           Scope
	locations       []Location
	source          string
	idGenerator     IDGenerator
	clock           Clock
	onError         ErrorHandler
	heartbeatPolicy HeartbeatPolicy
}

// WithScope supplies default correlation identifiers for every event.
func WithScope(scope Scope) RecorderOption {
	return func(config *recorderConfig) { config.scope = scope }
}

// WithLocators supplies module-owned resource locations for every event.
func WithLocators(locators ...Locator) RecorderOption {
	return func(config *recorderConfig) {
		config.locations = append(config.locations, locationsFrom(locators)...)
	}
}

// WithSource supplies the default source for every event.
func WithSource(source string) RecorderOption {
	return func(config *recorderConfig) { config.source = source }
}

// WithIDGenerator overrides random event and run identifier generation.
func WithIDGenerator(generator IDGenerator) RecorderOption {
	return func(config *recorderConfig) { config.idGenerator = generator }
}

// WithClock overrides wall time used to normalize event timestamps.
func WithClock(clock Clock) RecorderOption {
	return func(config *recorderConfig) { config.clock = clock }
}

// WithErrorHandler receives delivery failures from the configured Sink.
func WithErrorHandler(handler ErrorHandler) RecorderOption {
	return func(config *recorderConfig) { config.onError = handler }
}

// WithHeartbeatPolicy enables shared heartbeat delivery for active scopes.
func WithHeartbeatPolicy(policy HeartbeatPolicy) RecorderOption {
	return func(config *recorderConfig) { config.heartbeatPolicy = policy }
}

// NewRecorder creates a per-run event recorder. A RunID is generated when the
// supplied default Scope does not contain one.
func NewRecorder(sink Sink, options ...RecorderOption) (*Recorder, error) {
	config := recorderConfig{
		idGenerator: randomIDGenerator{},
		clock:       systemClock{},
	}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	if config.idGenerator == nil {
		return nil, fmt.Errorf("event ID generator is required")
	}
	if config.clock == nil {
		return nil, fmt.Errorf("event clock is required")
	}
	if config.scope.RunID == "" {
		id, err := config.idGenerator.NewID()
		if err != nil {
			return nil, fmt.Errorf("create event run ID: %w", err)
		}
		config.scope.RunID = "run_" + id
	}
	if sink == nil {
		sink = NoopSink{}
	}
	recorder := &Recorder{
		sink: sink, scope: config.scope, source: config.source,
		locations: cloneLocations(config.locations), idGenerator: config.idGenerator,
		clock: config.clock, onError: config.onError,
	}
	if config.heartbeatPolicy.Enabled() {
		recorder.heartbeats = newHeartbeatManager(recorder, config.heartbeatPolicy, config.clock)
	}
	return recorder, nil
}

// Publish normalizes and delivers an event. Sink failures are reported through
// the configured ErrorHandler and never alter the caller's control flow.
func (r *Recorder) Publish(ctx context.Context, event Event) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if event.ID == "" {
		id, err := r.idGenerator.NewID()
		if err != nil {
			r.mu.Unlock()
			r.reportError(ctx, fmt.Errorf("create event ID: %w", err))
			return
		}
		event.ID = "evt_" + id
	}
	r.sequence++
	event.Sequence = r.sequence
	if event.OccurredAt.IsZero() {
		event.OccurredAt = r.clock.Now()
	}
	if event.Source == "" {
		event.Source = r.source
	}
	event.Scope = mergeScope(r.scope, event.Scope)
	event.Locations = append(cloneLocations(r.locations), cloneLocations(event.Locations)...)
	deliveryErr := r.sink.Append(ctx, cloneEvent(event))
	r.mu.Unlock()
	if deliveryErr != nil {
		r.reportError(ctx, fmt.Errorf("append event %s: %w", event.ID, deliveryErr))
	}
}

// StartHeartbeat registers one active scope with the recorder's shared
// heartbeat manager. It returns a no-op lease when heartbeats are disabled.
func (r *Recorder) StartHeartbeat(ctx context.Context, scope Scope, attributes map[string]string, locators ...Locator) HeartbeatLease {
	if r == nil || r.heartbeats == nil {
		return noopHeartbeatLease{}
	}
	return r.heartbeats.Start(ctx, mergeScope(r.scope, scope), append(cloneLocations(r.locations), locationsFrom(locators)...), attributes)
}

// Close stops heartbeat delivery and rejects future events from this recorder.
func (r *Recorder) Close() {
	if r == nil {
		return
	}
	if r.heartbeats != nil {
		r.heartbeats.Close()
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
}

func (r *Recorder) reportError(ctx context.Context, err error) {
	if err != nil && r.onError != nil {
		r.onError(ctx, err)
	}
}

type recorderContextKey struct{}

// WithRecorder attaches a per-run recorder to a context for lower-level
// runtime components. It does not create global state.
func WithRecorder(ctx context.Context, recorder *Recorder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

// RecorderFromContext returns the recorder attached by WithRecorder, if any.
func RecorderFromContext(ctx context.Context) *Recorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(recorderContextKey{}).(*Recorder)
	return recorder
}
