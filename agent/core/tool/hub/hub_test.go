package hubprov

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	pb "github.com/RedHuang-0622/microHub/proto/gen/proto"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
	registry "github.com/RedHuang-0622/microHub/service_registry"
)

// ── mockHubHandler ──────────────────────────────────────────────────────────
// Implements hubbase.HubHandler with injectable function fields.

type mockHubHandler struct {
	serviceNameFunc func() string
	executeFunc     func(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error)
	onResultsFunc   func(results []hubbase.DispatchResult)
	addrsFunc       func() []string
}

func (m *mockHubHandler) ServiceName() string {
	if m.serviceNameFunc != nil {
		return m.serviceNameFunc()
	}
	return "test-hub"
}

func (m *mockHubHandler) Execute(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
	if m.executeFunc != nil {
		return m.executeFunc(req)
	}
	return nil, fmt.Errorf("mockHubHandler: not implemented")
}

func (m *mockHubHandler) OnResults(results []hubbase.DispatchResult) {
	if m.onResultsFunc != nil {
		m.onResultsFunc(results)
	}
}

func (m *mockHubHandler) Addrs() []string {
	if m.addrsFunc != nil {
		return m.addrsFunc()
	}
	return nil
}

// ── NewHubProvider ──────────────────────────────────────────────────────────

func TestNewHubProvider_NilHub(t *testing.T) {
	prov, err := NewHubProvider(nil, time.Second)
	if err == nil {
		t.Fatal("NewHubProvider with nil hub should error")
	}
	if prov != nil {
		t.Error("NewHubProvider with nil hub should return nil provider")
	}
}

func TestNewHubProvider(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, err := NewHubProvider(hub, time.Second)
	if err != nil {
		t.Fatalf("NewHubProvider() error = %v", err)
	}
	if prov == nil {
		t.Fatal("NewHubProvider() returned nil")
	}
}

func TestNewHubProvider_ZeroTimeout(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, err := NewHubProvider(hub, 0)
	if err != nil {
		t.Fatalf("NewHubProvider() with zero timeout error = %v", err)
	}
	if prov == nil {
		t.Fatal("NewHubProvider() returned nil")
	}
}

// ── ProviderName ────────────────────────────────────────────────────────────

func TestHubProvider_ProviderName(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, err := NewHubProvider(hub, time.Second)
	if err != nil {
		t.Fatalf("NewHubProvider() error = %v", err)
	}
	if name := prov.ProviderName(); name != "microhub" {
		t.Errorf("ProviderName() = %q, want %q", name, "microhub")
	}
}

func TestHubProvider_ProviderNameConsistent(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, _ := NewHubProvider(hub, 5*time.Second)
	prov2, _ := NewHubProvider(hub, 10*time.Second)

	if prov.ProviderName() != prov2.ProviderName() {
		t.Error("ProviderName() should be consistent across instances")
	}
}

// ── Tools (empty state) ─────────────────────────────────────────────────────

func TestHubProvider_Tools_Empty(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, err := NewHubProvider(hub, time.Second)
	if err != nil {
		t.Fatalf("NewHubProvider() error = %v", err)
	}
	tools := prov.Tools()
	if tools == nil {
		t.Fatal("Tools() returned nil")
	}
	if len(tools) != 0 {
		t.Errorf("Tools() length = %d, want 0 (registry not initialized)", len(tools))
	}
}

// ── Skills (empty state) ────────────────────────────────────────────────────

func TestHubProvider_Skills_Empty(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, err := NewHubProvider(hub, time.Second)
	if err != nil {
		t.Fatalf("NewHubProvider() error = %v", err)
	}
	skills := prov.Skills()
	if skills == nil {
		t.Fatal("Skills() returned nil")
	}
	if len(skills) != 0 {
		t.Errorf("Skills() length = %d, want 0 (registry not initialized)", len(skills))
	}
}

// ── Retire / Restore ────────────────────────────────────────────────────────

func TestHubProvider_Retire(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, _ := NewHubProvider(hub, time.Second)

	prov.Retire("some-skill")

	tools := prov.Tools()
	if tools == nil {
		t.Fatal("Tools() returned nil after Retire")
	}
}

func TestHubProvider_Restore(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, _ := NewHubProvider(hub, time.Second)

	prov.Restore("never-retired")

	prov.Retire("flaky-skill")
	prov.Restore("flaky-skill")

	tools := prov.Tools()
	if tools == nil {
		t.Fatal("Tools() returned nil after Restore")
	}
}

func TestHubProvider_RetireRestoreIdempotent(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, _ := NewHubProvider(hub, time.Second)

	prov.Retire("skill-a")
	prov.Retire("skill-a")

	prov.Restore("skill-a")
	prov.Restore("skill-a")

	prov.Restore("")
}

func TestHubProvider_RetireMultiple(t *testing.T) {
	hub := hubbase.New(NewHubRouter())
	prov, _ := NewHubProvider(hub, time.Second)

	prov.Retire("skill-a")
	prov.Retire("skill-b")
	prov.Retire("skill-c")

	prov.Restore("skill-b")
	prov.Restore("skill-a")
	prov.Restore("skill-c")
}

// ── NewHubRouter ────────────────────────────────────────────────────────────

func TestNewHubRouter(t *testing.T) {
	router := NewHubRouter()
	if router == nil {
		t.Fatal("NewHubRouter() returned nil")
	}
}

func TestHubRouter_ServiceName(t *testing.T) {
	router := NewHubRouter()
	if name := router.ServiceName(); name != "seele-hub" {
		t.Errorf("ServiceName() = %q, want %q", name, "seele-hub")
	}
}

func TestHubRouter_Execute_NilRequest(t *testing.T) {
	router := NewHubRouter()
	targets, err := router.Execute(nil)
	if err != nil {
		t.Errorf("Execute(nil) error = %v, want nil", err)
	}
	if targets != nil {
		t.Errorf("Execute(nil) targets = %v, want nil", targets)
	}
}

func TestHubRouter_Execute_NoMatch(t *testing.T) {
	router := NewHubRouter()
	req := &pb.ToolRequest{Method: "nonexistent-method"}
	targets, err := router.Execute(req)
	if err == nil {
		t.Fatal("Execute(unregistered method) should error")
	}
	if targets != nil {
		t.Error("Execute(unregistered method) should return nil targets")
	}
}

func TestHubRouter_OnResults_TransportError(t *testing.T) {
	router := NewHubRouter()
	// Simulate a dispatch result with a transport error.
	addr := "test-addr-unreachable:9999"
	result := hubbase.DispatchResult{
		Target: hubbase.DispatchTarget{Addr: addr},
		Err:    fmt.Errorf("connection refused"),
	}
	// Should not panic.
	router.OnResults([]hubbase.DispatchResult{result})

	// The address should now be marked offline in the registry.
	if !registry.IsOffline(addr) {
		t.Error("OnResults should mark offline on transport error")
	}
}

func TestHubRouter_OnResults_BusinessError(t *testing.T) {
	router := NewHubRouter()
	// Simulate a response with a business error status.
	result := hubbase.DispatchResult{
		Target: hubbase.DispatchTarget{Addr: "ok-addr:1"},
		Responses: []*pb.ToolResponse{
			{ToolName: "test-tool", Status: "error", Result: []byte("{}")},
		},
	}
	// Should not panic.
	router.OnResults([]hubbase.DispatchResult{result})
}

func TestHubRouter_OnResults_OK(t *testing.T) {
	router := NewHubRouter()
	result := hubbase.DispatchResult{
		Target: hubbase.DispatchTarget{Addr: "ok-addr:1"},
		Responses: []*pb.ToolResponse{
			{ToolName: "test-tool", Status: "ok", Result: []byte(`{"data": 1}`)},
		},
	}
	// Should not panic.
	router.OnResults([]hubbase.DispatchResult{result})
}

func TestHubRouter_OnResults_EmptyResults(t *testing.T) {
	router := NewHubRouter()
	// Empty slice should not panic.
	router.OnResults(nil)
	router.OnResults([]hubbase.DispatchResult{})
}

func TestHubRouter_Addrs_Empty(t *testing.T) {
	router := NewHubRouter()
	addrs := router.Addrs()
	if addrs == nil {
		t.Fatal("Addrs() returned nil")
	}
	if len(addrs) != 0 {
		t.Errorf("Addrs() = %v, want empty (registry not initialized)", addrs)
	}
}

// ── HubToolHandler Execute error handling ───────────────────────────────────

func TestHubToolHandler_Execute_EmptyResult(t *testing.T) {
	// When mockHandler.Execute returns an error, BaseHub.Dispatch returns nil
	// → HubToolHandler sees empty results → ErrToolUnavailable.
	mockCalled := false
	handler := &mockHubHandler{
		executeFunc: func(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
			mockCalled = true
			return nil, fmt.Errorf("no target for method=%q", req.GetMethod())
		},
	}
	hub := hubbase.New(handler)
	toolHandler := &HubToolHandler{Hub: hub, Method: "test", Timeout: time.Second}

	_, err := toolHandler.Execute(context.Background(), `{}`)
	if !mockCalled {
		t.Error("mock handler Execute was not called")
	}
	if err == nil {
		t.Fatal("Execute() should return error for empty dispatch result")
	}
	if !errors.Is(err, interfaces.ErrToolUnavailable) {
		t.Errorf("error should wrap ErrToolUnavailable, got: %v", err)
	}
}

func TestHubToolHandler_Execute_TransportError(t *testing.T) {
	// When dispatchAll fails to connect (no stream pool for target addr),
	// all results have Err set → allTransportErrors == true → ErrToolUnavailable.
	mockExecuted := false
	handler := &mockHubHandler{
		executeFunc: func(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
			mockExecuted = true
			return []hubbase.DispatchTarget{
				{Addr: "no-pool-test:1", Request: req, Stream: true},
			}, nil
		},
		// Addrs returns empty so no stream pools are created for any addr.
		addrsFunc: func() []string { return nil },
	}
	hub := hubbase.New(handler)
	toolHandler := &HubToolHandler{Hub: hub, Method: "test", Timeout: 100 * time.Millisecond}

	_, err := toolHandler.Execute(context.Background(), `{}`)
	if !mockExecuted {
		t.Error("mock handler Execute was not called")
	}
	if err == nil {
		t.Fatal("Execute() should return error for transport failure")
	}
	// The error should wrap ErrToolUnavailable indicating a transport-level issue.
	if !errors.Is(err, interfaces.ErrToolUnavailable) {
		t.Errorf("error should wrap ErrToolUnavailable, got: %v", err)
	}
}

func TestHubToolHandler_Execute_Timeout(t *testing.T) {
	// Zero timeout should cause context deadline exceeded.
	handler := &mockHubHandler{
		executeFunc: func(req *pb.ToolRequest) ([]hubbase.DispatchTarget, error) {
			return []hubbase.DispatchTarget{
				{Addr: "timeout-test:1", Request: req, Stream: true},
			}, nil
		},
		addrsFunc: func() []string { return nil },
	}
	hub := hubbase.New(handler)
	// Use zero timeout — the context will expire immediately.
	toolHandler := &HubToolHandler{Hub: hub, Method: "test", Timeout: 0}

	_, err := toolHandler.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("Execute() should return error with zero timeout")
	}
}
