//go:build seele_ab
// +build seele_ab

// Package session contains the ab/smoke test for passive context compression.
// Tests in this file are gated on the `seele_ab` build tag and `RUN_AB=true`
// so they can run on demand without coupling to the default CI pipeline.
//
// Run with:
//
//	RUN_AB=true go test -tags seele_ab ./session -run TestAB -v -count=1
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

func skipUnlessAB(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AB") != "true" {
		t.Skip("set RUN_AB=true to enable session ab tests")
	}
}

const summarySentinel = "Context summary of earlier execution"

// TestAB_PassiveCompressionDoesNotFire runs enough history to exceed the
// legacy threshold and verifies that Run forwards it without another model
// call or any hidden rewrite.
func TestAB_PassiveCompressionDoesNotFire(t *testing.T) {
	skipUnlessAB(t)

	mockSrv := newMockLLMServer()
	defer mockSrv.Close()

	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	eng := New(a)
	for i := 0; i < 60; i++ {
		body := make([]byte, 600)
		for j := range body {
			body[j] = byte('a' + (j % 26))
		}
		s := string(body)
		eng.AppendHistory(types.Message{Role: "user", Content: &s})
		eng.AppendHistory(types.Message{Role: "assistant", Content: &s})
	}
	before := eng.History()
	userInput := "next"

	mockSrv.EnqueueText("ok")
	if _, err := eng.Chat(context.Background(), userInput); err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	seen := mockSrv.Seen()
	if len(seen) != 1 {
		t.Fatalf("model calls = %d, want 1", len(seen))
	}
	want := append([]types.Message(nil), before...)
	want = append(want, types.Message{Role: "user", Content: &userInput})
	if !reflect.DeepEqual(seen[0].Messages, want) {
		t.Fatal("Run rewrote caller-owned history")
	}
	t.Logf("ab passive compression: forwarded %d messages in one call", len(want))
}

// TestAB_ExplicitCompressionOnlyRunsOnRequest verifies that the helper changes
// history only after an explicit CompressNow invocation.
func TestAB_ExplicitCompressionOnlyRunsOnRequest(t *testing.T) {
	skipUnlessAB(t)

	mockSrv := newMockLLMServer()
	defer mockSrv.Close()
	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	eng := New(a)
	body := make([]byte, 600)
	for j := range body {
		body[j] = byte('a' + (j % 26))
	}
	s := string(body)
	for i := 0; i < 60; i++ {
		eng.AppendHistory(types.Message{Role: "user", Content: &s})
		eng.AppendHistory(types.Message{Role: "assistant", Content: &s})
	}

	loop := NewReActLoop(a, a.LLM())
	for i := range eng.History() {
		loop.AppendHistory(eng.History()[i])
	}
	beforeBytes := 0
	for _, msg := range loop.History() {
		if msg.Content != nil {
			beforeBytes += len(*msg.Content)
		}
	}

	// Pre-stage the response so the explicit compression call returns a summary.
	mockSrv.EnqueueText("everything trimmed")
	if err := loop.CompressNow(context.Background()); err != nil {
		t.Fatalf("CompressNow failed: %v", err)
	}
	afterBytes := 0
	for _, msg := range loop.History() {
		if msg.Content != nil {
			afterBytes += len(*msg.Content)
		}
	}
	if got := afterBytes; got >= beforeBytes {
		t.Fatalf("after-compress history not smaller: before=%d after=%d", beforeBytes, got)
	}
	sawSummary := false
	for _, msg := range loop.History() {
		if msg.Content != nil && strings.Contains(*msg.Content, summarySentinel) {
			sawSummary = true
			break
		}
	}
	if !sawSummary {
		t.Fatal("explicit compression must inject the legacy summary message")
	}
	t.Logf("ab explicit compression: beforeBytes=%d afterBytes=%d (-%.1f%%)",
		beforeBytes, afterBytes, 100*(1-float64(afterBytes)/float64(beforeBytes)))
}

// TestAB_ConcurrentLoopsCancelIndependently cancels one of 10 sessions before
// dispatch, then verifies the other sessions complete and histories do not
// contain another session's marker.
func TestAB_ConcurrentLoopsCancelIndependently(t *testing.T) {
	skipUnlessAB(t)

	mockSrv := newMockLLMServer()
	defer mockSrv.Close()
	a, err := newTestAgent(mockSrv.URL())
	if err != nil {
		t.Fatalf("agent.New() failed: %v", err)
	}
	defer a.Shutdown()

	for i := 0; i < 9; i++ {
		mockSrv.EnqueueText(fmt.Sprintf("ok-%d", i))
	}

	var wg sync.WaitGroup
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	errs := make([]error, 10)
	histories := make([][]types.Message, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			eng := New(a)
			marker := fmt.Sprintf("[session-%02d]", idx)
			for k := 0; k < 5; k++ {
				body := fmt.Sprintf("%s payload step %d", marker, k)
				eng.AppendHistory(types.Message{Role: "user", Content: &body})
				eng.AppendHistory(types.Message{Role: "assistant", Content: &body})
			}
			ctx := context.Background()
			if idx == 0 {
				ctx = canceledCtx
			}
			_, errs[idx] = eng.Chat(ctx, marker)
			histories[idx] = eng.History()
		}(i)
	}
	wg.Wait()
	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("canceled session error = %v, want context canceled", errs[0])
	}
	for i := 1; i < 10; i++ {
		if errs[i] != nil {
			t.Fatalf("session %d failed: %v", i, errs[i])
		}
	}
	for i, history := range histories {
		own := fmt.Sprintf("[session-%02d]", i)
		if !historyContains(history, own) {
			t.Fatalf("session %d history is missing its own marker", i)
		}
		for other := 0; other < 10; other++ {
			if other == i {
				continue
			}
			foreign := fmt.Sprintf("[session-%02d]", other)
			if historyContains(history, foreign) {
				t.Fatalf("session %d history contains marker from session %d", i, other)
			}
		}
	}
	t.Log("ab concurrent loops: one session canceled, nine completed without history leakage")
}
