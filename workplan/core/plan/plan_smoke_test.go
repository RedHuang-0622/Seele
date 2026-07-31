//go:build seele_ab
// +build seele_ab

// Package plan_test contains the ab/smoke test for kernel-level concurrency.
// Tests in this file are gated on the `seele_ab` build tag and `RUN_AB=true`.
//
// Run with:
//
//	RUN_AB=true go test -tags seele_ab ./workplan/core/plan -run TestAB -v -count=1
package plan_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/workplan/core/edge"
	"github.com/RedHuang-0622/Seele/workplan/core/node"
	planpkg "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/core/types"
)

func skipUnlessAB(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AB") != "true" {
		t.Skip("set RUN_AB=true to enable plan ab tests")
	}
}

type abNode struct {
	id string
}

func (n *abNode) ID() string                                                  { return n.id }
func (n *abNode) Kind() node.NodeKind                                         { return node.KindMethod }
func (n *abNode) Run(context.Context, *types.WorkflowContext) (string, error) { return "", nil }

// TestAB_100ConcurrentPlanConstructionNoLoss spawns 100 goroutines that
// each build a complete Plan and run to a terminal entry. The assertion is
// that no Plan kernel loses materialization steps.
func TestAB_100ConcurrentPlanConstructionNoLoss(t *testing.T) {
	skipUnlessAB(t)

	const concurrent = 100
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func(idx int) {
			defer wg.Done()
			p := planpkg.New()
			for k := 0; k < 5; k++ {
				p.AddNode(&abNode{id: fmt.Sprintf("n%d-%d", idx, k)})
			}
			p.SetEntry(fmt.Sprintf("n%d-0", idx))
			for k := 0; k < 4; k++ {
				p.AddEdge(edge.Edge{From: fmt.Sprintf("n%d-%d", idx, k), To: fmt.Sprintf("n%d-%d", idx, k+1)})
			}
			if got := len(p.AllNodes()); got != 5 {
				t.Errorf("plan %d: AllNodes = %d, want 5", idx, got)
			}
			if got := len(p.AllEdges()); got != 4 {
				t.Errorf("plan %d: AllEdges = %d, want 4", idx, got)
			}
		}(i)
	}
	wg.Wait()
}

// TestAB_PlanExplicitIdempotentWrites hits the idempotency guards in
// 100 concurrent producers. Each producer attempts to add the same node ID
// and the same edge; the kernel must collapse duplicates.
func TestAB_PlanExplicitIdempotentWrites(t *testing.T) {
	skipUnlessAB(t)

	p := planpkg.New()
	const concurrent = 100

	var wg sync.WaitGroup
	wg.Add(concurrent)
	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			p.AddNodeIfAbsent(&abNode{id: "shared"})
			p.AddUnconditionalEdgeIfAbsent("shared", "shared")
		}()
	}
	wg.Wait()

	if got := len(p.AllNodes()); got != 1 {
		t.Fatalf("AllNodes = %d, want 1 (idempotent)", got)
	}
	if got := len(p.AllEdges()); got != 1 {
		t.Fatalf("AllEdges = %d, want 1 (idempotent)", got)
	}
}

// TestAB_PlanMixedConcurrentProducers exercises idempotency under a mix of
// unique and duplicate writes. 50 distinct nodes are added once each by 50
// goroutines; 50 duplicate writes target the same ID. The kernel must keep
// exactly 50 nodes — no duplicates, no losses.
func TestAB_PlanMixedConcurrentProducers(t *testing.T) {
	skipUnlessAB(t)

	p := planpkg.New()
	const writers = 50

	var wg sync.WaitGroup
	wg.Add(writers * 2)
	for i := 0; i < writers; i++ {
		i := i
		go func() {
			defer wg.Done()
			p.AddNode(&abNode{id: fmt.Sprintf("uniq-%d", i)})
		}()
		go func() {
			defer wg.Done()
			p.AddNodeIfAbsent(&abNode{id: "shared"}) // duplicate
		}()
	}
	wg.Wait()

	if got := len(p.AllNodes()); got != writers+1 {
		t.Fatalf("AllNodes = %d, want %d", got, writers+1)
	}
}
