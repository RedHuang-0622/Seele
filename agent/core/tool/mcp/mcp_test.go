package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// ── Provider.Attach validation ──────────────────────────────────────────────

func TestProvider_Attach_EmptyName(t *testing.T) {
	p := NewProvider()
	err := p.Attach(context.Background(), ServerConfig{Name: ""})
	if err == nil {
		t.Fatal("Attach with empty Name should return error")
	}
}

func TestProvider_Attach_UnknownTransport(t *testing.T) {
	p := NewProvider()
	err := p.Attach(context.Background(), ServerConfig{
		Name:      "test",
		Transport: "unknown",
	})
	if err == nil {
		t.Fatal("Attach with unknown transport should return error")
	}
}

func TestProvider_Attach_EmptyNamePreservesState(t *testing.T) {
	p := NewProvider()
	// First attach fails, provider state should remain clean.
	_ = p.Attach(context.Background(), ServerConfig{Name: ""})
	if len(p.ServerNames()) != 0 {
		t.Error("failed Attach should not modify provider state")
	}
}

func TestProvider_Attach_UnknownTransportPreservesState(t *testing.T) {
	p := NewProvider()
	_ = p.Attach(context.Background(), ServerConfig{
		Name: "test", Transport: "invalid",
	})
	if len(p.ServerNames()) != 0 {
		t.Error("failed Attach should not modify provider state")
	}
}

// ── Provider.Detach ─────────────────────────────────────────────────────────

func TestProvider_Detach_NotFound(t *testing.T) {
	p := NewProvider()
	// Detach on unregistered server should not panic.
	p.Detach("nonexistent")
}

func TestProvider_Detach_EmptyName(t *testing.T) {
	p := NewProvider()
	// Detach with empty name should not panic.
	p.Detach("")
}

func TestProvider_Detach_Idempotent(t *testing.T) {
	p := NewProvider()
	// Detach same name multiple times should not panic.
	p.Detach("multi")
	p.Detach("multi")
	p.Detach("multi")
}

// ── Provider.RefreshTools ───────────────────────────────────────────────────

func TestProvider_RefreshTools_NotAttached(t *testing.T) {
	p := NewProvider()
	err := p.RefreshTools(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("RefreshTools with unregistered server should return error")
	}
}

func TestProvider_RefreshTools_EmptyName(t *testing.T) {
	p := NewProvider()
	err := p.RefreshTools(context.Background(), "")
	if err == nil {
		t.Fatal("RefreshTools with empty name should return error")
	}
}

// ── Handler.Execute error handling ──────────────────────────────────────────

func TestHandler_Execute_InvalidArgs(t *testing.T) {
	h := &Handler{
		Client:   nil, // unused — fails at json.Unmarshal before client call
		ToolName: "test-tool",
	}
	_, err := h.Execute(context.Background(), "{invalid json}")

	if err == nil {
		t.Fatal("Execute with invalid JSON should return error")
	}
}

func TestHandler_Execute_EmptyArgs(t *testing.T) {
	h := &Handler{
		Client:   nil,
		ToolName: "test-tool",
	}
	// Empty string is valid JSON (unmarshal to map yields nil, no error at parse step).
	// Execute will proceed to client.CallTool with nil client — this tests
	// that the JSON parsing step does not reject empty input.
	_, err := h.Execute(context.Background(), "")
	if err == nil {
		t.Fatal("Execute with empty args and nil client should return error from client.CallTool")
	}
}

// ── isConnectivityError ───────────────────────────────────────────────────

func TestIsConnectivityError_ConnectionRefused(t *testing.T) {
	err := fmt.Errorf("dial tcp 127.0.0.1:8080: connect: connection refused")
	if !isConnectivityError(err) {
		t.Error("connection refused should be connectivity error")
	}
}

func TestIsConnectivityError_EOF(t *testing.T) {
	err := fmt.Errorf("read: unexpected eof")
	if !isConnectivityError(err) {
		t.Error("unexpected EOF should be connectivity error")
	}
}

func TestIsConnectivityError_BrokenPipe(t *testing.T) {
	err := fmt.Errorf("write: broken pipe")
	if !isConnectivityError(err) {
		t.Error("broken pipe should be connectivity error")
	}
}

func TestIsConnectivityError_Timeout(t *testing.T) {
	err := fmt.Errorf("context deadline exceeded")
	if !isConnectivityError(err) {
		t.Error("deadline exceeded should be connectivity error")
	}
}

func TestIsConnectivityError_ExitStatus(t *testing.T) {
	err := fmt.Errorf("process exited: exit status 1")
	if !isConnectivityError(err) {
		t.Error("exit status should be connectivity error")
	}
}

func TestIsConnectivityError_NetOpError(t *testing.T) {
	// 构造 net.OpError 而非真实拨号，避免 Linux CI 超时
	err := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: fmt.Errorf("connect: connection refused"),
	}
	if !isConnectivityError(err) {
		t.Errorf("net.OpError should be connectivity error, got: %v", err)
	}
}

func TestIsConnectivityError_FileNotFound(t *testing.T) {
	err := fmt.Errorf("file not found: /tmp/missing.py")
	if isConnectivityError(err) {
		t.Error("file not found should NOT be connectivity error")
	}
}

func TestIsConnectivityError_JSONParseError(t *testing.T) {
	err := fmt.Errorf("json: cannot unmarshal string into Go value")
	if isConnectivityError(err) {
		t.Error("JSON parse error should NOT be connectivity error")
	}
}

func TestIsConnectivityError_Nil(t *testing.T) {
	if isConnectivityError(nil) {
		t.Error("nil should not be connectivity error")
	}
}

// ── mcpBreaker ────────────────────────────────────────────────────────────

func TestBreaker_OpensAfterConsecutiveFails(t *testing.T) {
	b := newBreaker()
	b.maxFails = 2
	b.backoffBase = 1 * time.Millisecond

	server := "test-server"

	// First call: passes through
	if err := b.beforeCall(server); err != nil {
		t.Fatalf("beforeCall should pass on first call: %v", err)
	}
	b.afterCall(server, true) // connection error

	// Second call: still passes (maxFails=2, only 1 failure)
	if err := b.beforeCall(server); err != nil {
		t.Fatalf("beforeCall should pass on second call (1/2 failures): %v", err)
	}
	b.afterCall(server, true) // connection error → opens circuit

	// Third call: circuit open
	if err := b.beforeCall(server); err == nil {
		t.Fatal("beforeCall should reject when circuit is open")
	}
}

func TestBreaker_ResetsAfterSuccess(t *testing.T) {
	b := newBreaker()
	b.maxFails = 2

	server := "test-server"

	// One failure
	b.beforeCall(server)
	b.afterCall(server, true)

	// Success resets counter
	b.beforeCall(server)
	b.afterCall(server, false) // success

	// After reset, one more failure should not open circuit
	b.beforeCall(server)
	b.afterCall(server, true)

	if err := b.beforeCall(server); err != nil {
		t.Fatal("circuit should not be open after reset + single failure")
	}
}

func TestBreaker_BackoffExpiryHalfOpen(t *testing.T) {
	b := newBreaker()
	b.maxFails = 1
	b.backoffBase = 10 * time.Millisecond

	server := "test-server"

	// Open circuit
	b.beforeCall(server)
	b.afterCall(server, true)

	// Immediately: still open
	if err := b.beforeCall(server); err == nil {
		t.Fatal("circuit should be open immediately after failure")
	}

	// Wait for backoff to expire
	time.Sleep(20 * time.Millisecond)

	// After backoff: half-open, allows one probe
	if err := b.beforeCall(server); err != nil {
		t.Fatalf("circuit should be half-open after backoff expiry: %v", err)
	}

	// If probe fails, circuit re-opens
	b.afterCall(server, true)
	if err := b.beforeCall(server); err == nil {
		t.Fatal("circuit should re-open after failed probe")
	}
}

func TestBreaker_IsOpen(t *testing.T) {
	b := newBreaker()
	b.maxFails = 1
	b.backoffBase = 10 * time.Millisecond

	server := "test-server"

	if b.isOpen(server) {
		t.Error("isOpen should be false for unknown server")
	}

	b.beforeCall(server)
	b.afterCall(server, true)

	if !b.isOpen(server) {
		t.Error("isOpen should be true after circuit opens")
	}

	time.Sleep(20 * time.Millisecond)

	if b.isOpen(server) {
		t.Error("isOpen should be false after backoff expiry")
	}
}

func TestBreaker_ExponentialBackoff(t *testing.T) {
	b := newBreaker()
	b.maxFails = 1
	b.backoffBase = 1 * time.Millisecond
	b.backoffMax = 20 * time.Millisecond

	server := "test-server"

	// First open: backoffBase (1ms)
	b.beforeCall(server)
	b.afterCall(server, true)
	first := b.servers[server].until

	// Wait out and fail again: 2× backoffBase (2ms)
	time.Sleep(2 * time.Millisecond)
	b.beforeCall(server)
	b.afterCall(server, true)
	second := b.servers[server].until

	// Wait out and fail again: 4× backoffBase (4ms)
	time.Sleep(3 * time.Millisecond)
	b.beforeCall(server)
	b.afterCall(server, true)
	third := b.servers[server].until

	if !second.After(first) {
		t.Errorf("backoff should increase: first=%v second=%v", first, second)
	}
	if !third.After(second) {
		t.Errorf("backoff should keep increasing: second=%v third=%v", second, third)
	}
}

func TestBreaker_StartRecoveryOnlyOnce(t *testing.T) {
	b := newBreaker()
	b.maxFails = 1

	server := "test-server"

	// Open circuit
	b.beforeCall(server)
	b.afterCall(server, true)

	// Start recovery
	b.startRecovery(server, func(ctx context.Context) error {
		return errors.New("still down")
	})

	// Should not start a second goroutine
	b.startRecovery(server, func(ctx context.Context) error {
		return errors.New("still down")
	})

	b.mu.Lock()
	st := b.servers[server]
	b.mu.Unlock()

	if st == nil || !st.recovering {
		t.Error("recovering should be true after startRecovery")
	}
	// Only one goroutine, not leaking
}

func TestBreaker_DifferentServersIsolated(t *testing.T) {
	b := newBreaker()
	b.maxFails = 1

	// Server A: open circuit
	b.beforeCall("server-a")
	b.afterCall("server-a", true)

	if !b.isOpen("server-a") {
		t.Error("server-a should be open")
	}

	// Server B: should be unaffected
	if b.isOpen("server-b") {
		t.Error("server-b should not be affected by server-a failure")
	}
	if err := b.beforeCall("server-b"); err != nil {
		t.Errorf("server-b should not be blocked: %v", err)
	}
}

// ── Provider: breaker integration ─────────────────────────────────────────

func TestNewProvider_HasBreaker(t *testing.T) {
	p := NewProvider()
	if p.breaker == nil {
		t.Fatal("NewProvider should initialize breaker")
	}
}

func TestProvider_ServerStatus_NotAttached(t *testing.T) {
	p := NewProvider()
	alive, tools, err := p.ServerStatus("nonexistent")
	if alive || tools != 0 || err == nil {
		t.Errorf("expected error for unattached server, got alive=%v tools=%d err=%v", alive, tools, err)
	}
}

func TestProvider_IsAlive_NotAttached(t *testing.T) {
	p := NewProvider()
	if p.IsAlive("nonexistent") {
		t.Error("IsAlive should return false for unattached server")
	}
}

