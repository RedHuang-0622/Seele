//go:build seele_ab
// +build seele_ab

// Package scheduler_test contains the ab/smoke test for fork concurrency.
// Tests in this file are gated on the `seele_ab` build tag and `RUN_AB=true`
// so they can run on demand without coupling to the default CI pipeline.
//
// Run with:
//
//	RUN_AB=true go test -tags seele_ab ./workplan/runtime/scheduler -run TestAB -v -count=1
package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/runtime/executor"
	"github.com/RedHuang-0622/Seele/workplan/runtime/scheduler"
	"github.com/RedHuang-0622/Seele/workplan/sugar/auto"
)

func requireAB(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AB") != "true" {
		t.Skip("set RUN_AB=true to enable workplan ab tests")
	}
}

// sleepFactory creates an agent that records per-node start/end times and
// sleeps for a fixed duration. It is the smallest fixture that lets us
// observe the scheduler's concurrency behavior end-to-end.
type sleepFactory struct {
	prefix   string
	duration time.Duration
	tracker  *concurrencyTracker
}

func (f sleepFactory) NewAgent(string) node.Agent {
	return sleepAgent{prefix: f.prefix, duration: f.duration, tracker: f.tracker}
}

type sleepAgent struct {
	prefix   string
	duration time.Duration
	tracker  *concurrencyTracker
}

func (a sleepAgent) Chat(ctx context.Context, input string) (string, error) {
	a.tracker.begin(a.prefix)
	defer a.tracker.end(a.prefix)
	select {
	case <-time.After(a.duration):
		return a.prefix + ":" + input, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type concurrencyTracker struct {
	mu          sync.Mutex
	inFlightNow int
	maxInFlight int
	starts      map[string]time.Time
	ends        map[string]time.Time
}

func newConcurrencyTracker() *concurrencyTracker {
	return &concurrencyTracker{
		starts: map[string]time.Time{},
		ends:   map[string]time.Time{},
	}
}

func (c *concurrencyTracker) begin(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlightNow++
	if c.inFlightNow > c.maxInFlight {
		c.maxInFlight = c.inFlightNow
	}
	c.starts[prefix] = time.Now()
}

func (c *concurrencyTracker) end(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlightNow--
	c.ends[prefix] = time.Now()
}

// TestAB_ForkConcurrencyRespectsBound builds an 8-leaf fan-out feeding a
// single integrate node. All leaves take 200ms. With MaxForkConcurrency=4,
// the engine must dispatch them in groups of 4 (wall clock ≈ 2*200ms) and
// must never exceed 4 concurrent branches.
func TestAB_ForkConcurrencyRespectsBound(t *testing.T) {
	requireAB(t)

	tracker := newConcurrencyTracker()
	const leafCount = 8
	const leafDuration = 200 * time.Millisecond

	g := coreplan.New()
	auto.Add(g, "inspect", "inspect", sleepFactory{prefix: "inspect", duration: 50 * time.Millisecond, tracker: tracker})
	for i := 0; i < leafCount; i++ {
		prefix := fmt.Sprintf("leaf%d", i)
		auto.Add(g, prefix, prefix, sleepFactory{prefix: prefix, duration: leafDuration, tracker: tracker})
	}
	auto.Add(g, "integrate", "integrate", sleepFactory{prefix: "integrate", duration: 50 * time.Millisecond, tracker: tracker})
	g.SetEntry("inspect")
	for _, leaf := range leafNames(0, leafCount) {
		g.AddEdge(edge.Edge{From: "inspect", To: leaf})
		g.AddEdge(edge.Edge{From: leaf, To: "integrate"})
	}

	sched := scheduler.New(g, executor.New())
	sched.SetMaxForkConcurrency(4)

	start := time.Now()
	result, err := sched.Run(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("sched.Run error = %v", err)
	}
	if result.Aborted {
		t.Fatalf("execution aborted: %s", result.AbortReason)
	}

	if got := len(result.NodeResults); got != leafCount+2 {
		t.Fatalf("NodeResults = %d, want %d", got, leafCount+2)
	}
	if got := tracker.maxInFlight; got > 4 {
		t.Fatalf("maxInFlight = %d, want ≤ 4", got)
	}
	if got := tracker.maxInFlight; got < 2 {
		t.Fatalf("maxInFlight = %d, expected at least 2 for fan-out", got)
	}
	if elapsed > 2*leafDuration+500*time.Millisecond {
		t.Fatalf("elapsed = %v, want ≤ %v (cap=4 fan-out)", elapsed, 2*leafDuration+500*time.Millisecond)
	}
	t.Logf("ab fork concurrency: maxInFlight=%d elapsed=%v nodeCount=%d", tracker.maxInFlight, elapsed, len(result.NodeResults))
}

// TestAB_ForkIsolationAndIdempotency runs the same fan-out 50 times in a row
// and asserts that each node runs exactly once per iteration.
func TestAB_ForkIsolationAndIdempotency(t *testing.T) {
	requireAB(t)

	tracker := newConcurrencyTracker()
	const iterations = 50
	g := coreplan.New()
	auto.Add(g, "inspect", "inspect", sleepFactory{prefix: "inspect", duration: 5 * time.Millisecond, tracker: tracker})
	leafNames := leafNames(0, 4)
	for _, leaf := range leafNames {
		auto.Add(g, leaf, leaf, sleepFactory{prefix: leaf, duration: 5 * time.Millisecond, tracker: tracker})
	}
	auto.Add(g, "integrate", "integrate", sleepFactory{prefix: "integrate", duration: 5 * time.Millisecond, tracker: tracker})
	g.SetEntry("inspect")
	for _, leaf := range leafNames {
		g.AddEdge(edge.Edge{From: "inspect", To: leaf})
		g.AddEdge(edge.Edge{From: leaf, To: "integrate"})
	}

	var counter atomic.Int64
	sched := scheduler.New(g, executor.New())
	sched.SetMaxForkConcurrency(8)

	for i := 0; i < iterations; i++ {
		result, err := sched.Run(context.Background())
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if result.Aborted {
			t.Fatalf("iter %d aborted: %s", i, result.AbortReason)
		}
		counter.Add(int64(len(result.NodeResults)))
	}

	// Each iteration must run inspect + 4 leaves + integrate = 6 nodes.
	want := int64(iterations * (1 + len(leafNames) + 1))
	if got := counter.Load(); got != want {
		t.Fatalf("total node executions = %d, want %d", got, want)
	}
	t.Logf("ab fork idempotency: %d iterations × %d nodes = %d executions", iterations, 1+len(leafNames)+1, want)
}

// TestAB_ForkConcurrencyCancelByDeadline asserts that a 200ms parent deadline
// cleanly cancels a 2s fan-out and that the scheduler returns promptly.
func TestAB_ForkConcurrencyCancelByDeadline(t *testing.T) {
	requireAB(t)

	tracker := newConcurrencyTracker()
	g := coreplan.New()
	auto.Add(g, "start", "start", sleepFactory{prefix: "start", duration: 50 * time.Millisecond, tracker: tracker})
	slowIDs := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		prefix := fmt.Sprintf("slow%d", i)
		slowIDs = append(slowIDs, prefix)
		auto.Add(g, prefix, prefix, sleepFactory{prefix: prefix, duration: 2 * time.Second, tracker: tracker})
	}
	g.SetEntry("start")
	for _, slow := range slowIDs {
		g.AddEdge(edge.Edge{From: "start", To: slow})
	}

	sched := scheduler.New(g, executor.New())
	sched.SetMaxForkConcurrency(6)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sched.Run(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context deadline exceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("deadline took too long to propagate: %v", elapsed)
	}
	t.Logf("ab fork deadline: elapsed=%v err=%v", elapsed, err)
}

func leafNames(start, count int) []string {
	out := make([]string, count)
	for i := 0; i < count; i++ {
		out[i] = fmt.Sprintf("leaf%d", start+i)
	}
	sort.Strings(out)
	return out
}
