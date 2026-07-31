package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rootpool "github.com/RedHuang-0622/Seele/accountpool"
	"github.com/RedHuang-0622/Seele/types"
)

func TestChatClientCompleteHoldsLeaseForHTTPResponseLifecycle(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-finish
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`))
	}))
	defer server.Close()

	pool := NewAccountPool(&Account{
		Name:           "one",
		Provider:       ProviderOpenAI,
		BaseURL:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		MaxConcurrency: 1,
	})
	client := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	result := make(chan error, 1)
	go func() {
		_, err := client.Complete(context.Background(), nil, nil)
		result <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := pool.Acquire(ctx, rootpool.AcquireRequest{AccountID: "one"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent Acquire() error = %v, want deadline exceeded", err)
	}
	close(finish)
	if err := <-result; err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	lease, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatalf("Acquire() after response = %v", err)
	}
	lease.Release()
}

func TestChatClientCompleteStreamHoldsAndReleasesSameLease(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		flusher.Flush()
		close(started)
		<-finish
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	pool := NewAccountPool(&Account{
		Name:           "stream",
		Provider:       ProviderOpenAI,
		BaseURL:        server.URL,
		APIKey:         "test",
		Model:          "test-model",
		MaxConcurrency: 1,
	})
	client := NewChatClient(types.LLMConfig{}).WithAccountPool(pool)
	result := make(chan error, 1)
	go func() {
		_, _, _, err := client.CompleteStream(context.Background(), nil, nil, nil)
		result <- err
	}()
	<-started
	if active := pool.Stats().Active; active != 1 {
		t.Fatalf("active stream leases = %d, want 1", active)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := pool.Acquire(ctx, rootpool.AcquireRequest{AccountID: "stream"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent stream Acquire() error = %v, want deadline exceeded", err)
	}
	close(finish)
	if err := <-result; err != nil {
		t.Fatalf("CompleteStream() error = %v", err)
	}
	if active := pool.Stats().Active; active != 0 {
		t.Fatalf("active stream leases after EOF = %d, want 0", active)
	}
}

func TestChatClientBuildErrorsReleaseLease(t *testing.T) {
	pool := NewAccountPool(&Account{Name: "one", MaxConcurrency: 1})
	client := NewChatClient(types.LLMConfig{}).
		WithAccountPool(pool).
		WithStrategy(&errorBuildStrategy{})
	if _, err := client.Complete(context.Background(), nil, nil); err == nil {
		t.Fatal("Complete() error = nil")
	}
	lease, err := pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatalf("Acquire() after build error = %v", err)
	}
	lease.Release()

	if _, _, _, err := client.CompleteStream(context.Background(), nil, nil, nil); err == nil {
		t.Fatal("CompleteStream() error = nil")
	}
	lease, err = pool.Acquire(context.Background(), rootpool.AcquireRequest{AccountID: "one"})
	if err != nil {
		t.Fatalf("Acquire() after stream build error = %v", err)
	}
	lease.Release()
}
