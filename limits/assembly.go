package limits

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/RedHuang-0622/Seele/types"
)

// sharedKey is the gate name used when per-key gates are disabled.
const sharedKey = "default"

// Observation kinds emitted to an Observer.
const (
	ObservationAdmitted     = "admitted"
	ObservationQueueTimeout = "queue_timeout"
	ObservationRejected     = "rejected"
	ObservationRetry        = "retry"
)

// Observation is one admission or retry event. Observers must be cheap: they
// are called synchronously on the request path.
type Observation struct {
	Kind    string
	Key     string
	Wait    time.Duration
	Attempt int
	Delay   time.Duration
	Err     error
}

// Observer receives requests-path observations. A nil observer is a no-op.
type Observer func(Observation)

// Option configures an Assembly.
type Option func(*Assembly)

// WithEstimator replaces the cost estimator.
func WithEstimator(estimator CostEstimator) Option {
	return func(a *Assembly) {
		if estimator != nil {
			a.estimate = estimator
		}
	}
}

// WithObserver installs an observer.
func WithObserver(observer Observer) Option {
	return func(a *Assembly) { a.observe = observer }
}

// WithClock replaces the clock used for bucket refills and wait accounting.
// Production code should not need it; tests use it for determinism.
func WithClock(now func() time.Time) Option {
	return func(a *Assembly) {
		if now != nil {
			a.now = now
		}
	}
}

// WithRand replaces the jitter source (a value in [0,1) is expected).
func WithRand(randFloat func() float64) Option {
	return func(a *Assembly) {
		if randFloat != nil {
			a.randFloat = randFloat
		}
	}
}

// Snapshot is the assembled view of an Assembly: base params plus per-key
// stats.
type Snapshot struct {
	Params Params           `json:"params"`
	Keys   []string         `json:"keys"`
	Stats  map[string]Stats `json:"stats"`
}

// Assembly is the wiring layer: it maps keys (accounts, roles, nodes) to gates
// and decorates types.ChatCompleter implementations with admission, token
// settlement and retry classification.
//
// Key mapping: with Params.PerKey true every key gets its own gate; otherwise
// all keys share the "default" gate, which is what a single global budget
// wants.
type Assembly struct {
	mu     sync.Mutex
	params Params
	gates  map[string]*Gate

	now       func() time.Time
	estimate  CostEstimator
	observe   Observer
	randFloat func() float64
}

// Assemble builds an assembly. Parameters are normalized and validated once
// here and again on every Set* call, so gate construction cannot fail later.
func Assemble(params Params, options ...Option) (*Assembly, error) {
	params = params.Normalize()
	if err := params.Validate(); err != nil {
		return nil, err
	}
	assembly := &Assembly{
		params:    params,
		gates:     make(map[string]*Gate),
		now:       time.Now,
		estimate:  DefaultEstimator,
		randFloat: func() float64 { return 0.5 },
	}
	for _, option := range options {
		if option != nil {
			option(assembly)
		}
	}
	return assembly, nil
}

// Params returns the current base parameters.
func (a *Assembly) Params() Params {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.params
}

// SetParams replaces the base parameters and applies them to every existing
// gate. This is a global override: per-key parameters set earlier through
// SetKeyParams are replaced as well, which keeps the outcome predictable.
func (a *Assembly) SetParams(params Params) error {
	params = params.Normalize()
	if err := params.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	gates := make([]*Gate, 0, len(a.gates))
	for _, gate := range a.gates {
		gates = append(gates, gate)
	}
	a.params = params
	a.mu.Unlock()

	for _, gate := range gates {
		if err := gate.SetParams(params); err != nil {
			return fmt.Errorf("limits: apply params to gate %q: %w", gate.Key(), err)
		}
	}
	return nil
}

// SetKeyParams overrides the parameters of a single key, creating the gate if
// it does not exist yet.
func (a *Assembly) SetKeyParams(key string, params Params) error {
	gate, err := a.Gate(key)
	if err != nil {
		return err
	}
	return gate.SetParams(params)
}

// SetEnabled toggles admission for every gate.
func (a *Assembly) SetEnabled(enabled bool) error {
	return a.mutate(func(p Params) Params { return p.WithEnabled(enabled) })
}

// SetConcurrency changes the weighted in-flight cap for every gate.
func (a *Assembly) SetConcurrency(max int) error {
	return a.mutate(func(p Params) Params { return p.WithConcurrency(max) })
}

// SetRate changes the request and token rates for every gate, re-deriving the
// bucket capacities.
func (a *Assembly) SetRate(requestsPerMin, tokensPerMin float64) error {
	return a.mutate(func(p Params) Params { return p.WithRate(requestsPerMin, tokensPerMin) })
}

// SetBurst pins the bucket capacities for every gate.
func (a *Assembly) SetBurst(requests, tokens int) error {
	return a.mutate(func(p Params) Params { return p.WithBurst(requests, tokens) })
}

// SetRetry changes the retry policy for every gate.
func (a *Assembly) SetRetry(policy RetryPolicy) error {
	return a.mutate(func(p Params) Params { return p.WithRetry(policy) })
}

// SetQueueTimeout changes the admission budget for every gate.
func (a *Assembly) SetQueueTimeout(timeout time.Duration) error {
	return a.mutate(func(p Params) Params { return p.WithQueueTimeout(timeout) })
}

// SetImageWeight changes the per-image in-flight weight for every gate.
func (a *Assembly) SetImageWeight(weight float64) error {
	return a.mutate(func(p Params) Params { return p.WithImageWeight(weight) })
}

// SetPerKey switches between per-key and shared gating. Existing gates are kept
// so in-flight permits stay valid, but new lookups follow the new mode.
func (a *Assembly) SetPerKey(perKey bool) error {
	return a.mutate(func(p Params) Params { return p.WithPerKey(perKey) })
}

// SetEstimator replaces the cost estimator.
func (a *Assembly) SetEstimator(estimator CostEstimator) {
	if estimator == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.estimate = estimator
}

// SetObserver replaces the observer.
func (a *Assembly) SetObserver(observer Observer) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observe = observer
}

// mutate applies a parameter transformation through SetParams so every gate and
// the base parameters stay in sync.
func (a *Assembly) mutate(transform func(Params) Params) error {
	a.mu.Lock()
	next := transform(a.params)
	a.mu.Unlock()
	return a.SetParams(next)
}

// Enabled reports whether admission is active.
func (a *Assembly) Enabled() bool { return a.Params().Enabled }

// Gate returns the gate for key, creating it on first use.
func (a *Assembly) Gate(key string) (*Gate, error) {
	if key == "" {
		key = sharedKey
	}
	a.mu.Lock()
	if !a.params.PerKey {
		key = sharedKey
	}
	if gate, ok := a.gates[key]; ok {
		a.mu.Unlock()
		return gate, nil
	}
	params := a.params
	a.mu.Unlock()

	gate, err := NewGate(key, params)
	if err != nil {
		return nil, fmt.Errorf("limits: build gate %q: %w", key, err)
	}
	gate.setClock(a.clock())

	a.mu.Lock()
	defer a.mu.Unlock()
	if existing, ok := a.gates[key]; ok {
		return existing, nil
	}
	a.gates[key] = gate
	return gate, nil
}

// Keys lists the gates created so far.
func (a *Assembly) Keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	keys := make([]string, 0, len(a.gates))
	for key := range a.gates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Snapshot returns parameters and per-key stats.
func (a *Assembly) Snapshot() Snapshot {
	a.mu.Lock()
	params := a.params
	gates := make(map[string]*Gate, len(a.gates))
	for key, gate := range a.gates {
		gates[key] = gate
	}
	a.mu.Unlock()

	snapshot := Snapshot{Params: params, Keys: make([]string, 0, len(gates)), Stats: make(map[string]Stats, len(gates))}
	for key, gate := range gates {
		snapshot.Keys = append(snapshot.Keys, key)
		snapshot.Stats[key] = gate.Stats()
	}
	sort.Strings(snapshot.Keys)
	return snapshot
}

// Cost estimates the admission cost of a request using the configured
// estimator.
func (a *Assembly) Cost(messages []types.Message, tools []types.Tool) Cost {
	a.mu.Lock()
	estimator := a.estimate
	a.mu.Unlock()
	if estimator == nil {
		return Cost{Requests: 1}
	}
	return estimator(messages, tools).normalize()
}

// RetryPolicy returns the base retry policy.
func (a *Assembly) RetryPolicy() RetryPolicy { return a.Params().Retry }

// Wrap decorates a completer with the shared gate.
func (a *Assembly) Wrap(next types.ChatCompleter) (types.ChatCompleter, error) {
	return a.WrapFor(sharedKey, next)
}

// WrapFor decorates a completer with the gate of key. It is the assembly entry
// point for account pools: one completer per account, one gate per account.
func (a *Assembly) WrapFor(key string, next types.ChatCompleter) (types.ChatCompleter, error) {
	if next == nil {
		return nil, fmt.Errorf("%w: completer is required", ErrInvalidParams)
	}
	gate, err := a.Gate(key)
	if err != nil {
		return nil, err
	}
	return &wrapper{assembly: a, gate: gate, next: next, key: gate.Key()}, nil
}

// WrapAll decorates a map of completers keyed by account name, preserving the
// input keys. It is the batch form of WrapFor.
func (a *Assembly) WrapAll(completers map[string]types.ChatCompleter) (map[string]types.ChatCompleter, error) {
	wrapped := make(map[string]types.ChatCompleter, len(completers))
	for key, completer := range completers {
		decorated, err := a.WrapFor(key, completer)
		if err != nil {
			return nil, fmt.Errorf("limits: wrap %q: %w", key, err)
		}
		wrapped[key] = decorated
	}
	return wrapped, nil
}

// WrapComplete decorates a completer that only guarantees Complete, which is
// the shape account pools expose (for example seelex's agent.Completer).
//
// When the concrete value also implements types.ChatCompleter its real
// streaming methods are used; otherwise streaming falls back to one Complete
// call delivered as a single text delta, matching agent.NewWithComponents.
// This keeps the assembly usable without widening the caller's interface.
func (a *Assembly) WrapComplete(key string, next CompleteOnly) (types.ChatCompleter, error) {
	if next == nil {
		return nil, fmt.Errorf("%w: completer is required", ErrInvalidParams)
	}
	if full, ok := next.(types.ChatCompleter); ok {
		return a.WrapFor(key, full)
	}
	return a.WrapFor(key, &completeOnlyAdapter{inner: next})
}

// WrapAllComplete is the batch form of WrapComplete.
func (a *Assembly) WrapAllComplete(completers map[string]CompleteOnly) (map[string]types.ChatCompleter, error) {
	wrapped := make(map[string]types.ChatCompleter, len(completers))
	for key, completer := range completers {
		decorated, err := a.WrapComplete(key, completer)
		if err != nil {
			return nil, fmt.Errorf("limits: wrap %q: %w", key, err)
		}
		wrapped[key] = decorated
	}
	return wrapped, nil
}

// clock and observer read the injectable fields under the lock.
func (a *Assembly) clock() func() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.now
}

func (a *Assembly) observer() Observer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.observe
}

func (a *Assembly) randSource() float64 {
	a.mu.Lock()
	source := a.randFloat
	a.mu.Unlock()
	if source == nil {
		return 0.5
	}
	return source()
}

// emit forwards an observation without holding the assembly lock.
func (a *Assembly) emit(observation Observation) {
	observer := a.observer()
	if observer == nil {
		return
	}
	observer(observation)
}
