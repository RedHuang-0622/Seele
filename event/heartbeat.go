package event

import (
	"context"
	"sync"
	"time"
)

// HeartbeatPolicy controls periodic liveness events for active runtime scopes.
// A non-positive Interval disables heartbeat delivery.
type HeartbeatPolicy struct {
	Interval time.Duration
}

// Enabled reports whether the policy schedules heartbeat events.
func (p HeartbeatPolicy) Enabled() bool { return p.Interval > 0 }

// HeartbeatLease unregisters one active scope from heartbeat delivery.
type HeartbeatLease interface {
	Stop()
}

type noopHeartbeatLease struct{}

func (noopHeartbeatLease) Stop() {}

// HeartbeatPayload is emitted as the JSON Content of a heartbeat event.
type HeartbeatPayload struct {
	StartedAt time.Time `json:"started_at"`
	ElapsedMS int64     `json:"elapsed_ms"`
}

type heartbeatLease struct {
	manager *HeartbeatManager
	id      uint64
	once    sync.Once
}

func (l *heartbeatLease) Stop() {
	if l == nil || l.manager == nil {
		return
	}
	l.once.Do(func() { l.manager.stop(l.id) })
}

type activeHeartbeat struct {
	ctx        context.Context
	scope      Scope
	locations  []Location
	attributes map[string]string
	startedAt  time.Time
}

// HeartbeatManager uses one ticker for every active scope of a recorder. It
// avoids allocating one goroutine or ticker per running node.
type HeartbeatManager struct {
	publisher *Recorder
	interval  time.Duration
	clock     Clock

	mu     sync.Mutex
	nextID uint64
	active map[uint64]activeHeartbeat

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newHeartbeatManager(publisher *Recorder, policy HeartbeatPolicy, clock Clock) *HeartbeatManager {
	ctx, cancel := context.WithCancel(context.Background())
	manager := &HeartbeatManager{
		publisher: publisher, interval: policy.Interval, clock: clock,
		active: make(map[uint64]activeHeartbeat), cancel: cancel, done: make(chan struct{}),
	}
	go manager.run(ctx)
	return manager
}

// Start registers a scope until its returned lease is stopped or the manager
// is closed. The sink receives heartbeats with a cancellation-independent
// context; the lifecycle owner remains responsible for stopping the lease.
func (m *HeartbeatManager) Start(ctx context.Context, scope Scope, locations []Location, attributes map[string]string) HeartbeatLease {
	if m == nil {
		return noopHeartbeatLease{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	select {
	case <-m.done:
		m.mu.Unlock()
		return noopHeartbeatLease{}
	default:
	}
	m.nextID++
	id := m.nextID
	m.active[id] = activeHeartbeat{
		ctx: context.WithoutCancel(ctx), scope: scope,
		locations: cloneLocations(locations), attributes: cloneAttributes(attributes), startedAt: m.clock.Now(),
	}
	m.mu.Unlock()
	return &heartbeatLease{manager: m, id: id}
}

// Close stops the shared ticker and removes every active scope.
func (m *HeartbeatManager) Close() {
	if m == nil {
		return
	}
	m.once.Do(func() {
		m.cancel()
		<-m.done
		m.mu.Lock()
		m.active = make(map[uint64]activeHeartbeat)
		m.mu.Unlock()
	})
}

func (m *HeartbeatManager) stop(id uint64) {
	m.mu.Lock()
	delete(m.active, id)
	m.mu.Unlock()
}

func (m *HeartbeatManager) run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	defer close(m.done)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			m.emit(now)
		}
	}
}

func (m *HeartbeatManager) emit(now time.Time) {
	m.mu.Lock()
	active := make([]activeHeartbeat, 0, len(m.active))
	for _, heartbeat := range m.active {
		active = append(active, heartbeat)
	}
	m.mu.Unlock()
	for _, heartbeat := range active {
		content, err := JSON(HeartbeatPayload{
			StartedAt: heartbeat.startedAt,
			ElapsedMS: now.Sub(heartbeat.startedAt).Milliseconds(),
		})
		if err != nil {
			continue
		}
		m.publisher.Publish(heartbeat.ctx, Event{
			Type: TypeHeartbeat, Status: StatusRunning, Scope: heartbeat.scope, Locations: cloneLocations(heartbeat.locations),
			Content: content, Attributes: cloneAttributes(heartbeat.attributes), OccurredAt: now,
		})
	}
}

func cloneAttributes(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
