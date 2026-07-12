package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent/core/api"
	holder "github.com/RedHuang-0622/Seele/agent/core/tool/holder"
	hubprov "github.com/RedHuang-0622/Seele/agent/core/tool/hub"
	apigw "github.com/RedHuang-0622/Seele/agent/gateway/api"
	toolgw "github.com/RedHuang-0622/Seele/agent/gateway/tool"
	"github.com/RedHuang-0622/Seele/types"
	hubbase "github.com/RedHuang-0622/microHub/root_class/hub"
)

// ── Mock Implementations ─────────────────────────────────────────────────────────

// mockChatCompleter implements types.ChatCompleter for testing the LLM() accessor.
type mockChatCompleter struct{}

func (m *mockChatCompleter) Complete(ctx context.Context, messages []types.Message, tools []types.Tool) (types.Message, error) {
	return types.Message{}, nil
}

func (m *mockChatCompleter) CompleteStream(ctx context.Context, messages []types.Message, tools []types.Tool, onChunk func(delta string)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error) {
	return "", "", nil, nil
}

func (m *mockChatCompleter) CompleteStreamEvents(ctx context.Context, messages []types.Message, tools []types.Tool, onEvent func(types.StreamEvent)) (content string, reasoningContent string, toolCalls []types.ToolCall, err error) {
	return "", "", nil, nil
}

// compile-time check
var _ types.ChatCompleter = (*mockChatCompleter)(nil)

// mockToolGateway implements toolgw.Gateway for testing dispatch paths.
type mockToolGateway struct {
	toolsFunc    func() []types.Tool
	visibleFunc  func(ctx context.Context) []types.Tool
	dispatchFunc func(ctx context.Context, name, argsJSON string) (string, error)
	plugin       string
}

func (m *mockToolGateway) Tools() []types.Tool {
	if m.toolsFunc != nil {
		return m.toolsFunc()
	}
	return nil
}

func (m *mockToolGateway) VisibleTools(ctx context.Context) []types.Tool {
	if m.visibleFunc != nil {
		return m.visibleFunc(ctx)
	}
	return nil
}

func (m *mockToolGateway) Dispatch(ctx context.Context, name, argsJSON string) (string, error) {
	if m.dispatchFunc != nil {
		return m.dispatchFunc(ctx, name, argsJSON)
	}
	return "", nil
}

func (m *mockToolGateway) ActivatePlugin(name string) error { m.plugin = name; return nil }
func (m *mockToolGateway) ActivePlugin() string             { return m.plugin }
func (m *mockToolGateway) DeactivatePlugin()                { m.plugin = "" }

// compile-time check
var _ toolgw.Gateway = (*mockToolGateway)(nil)

// mockAPIGateway implements apigw.Gateway for testing.
type mockAPIGateway struct{}

func (m *mockAPIGateway) Select(ctx context.Context) (*api.Account, error) {
	return &api.Account{Name: "test", Provider: api.ProviderOpenAI, Model: "test-model"}, nil
}

func (m *mockAPIGateway) Health(ctx context.Context) map[string]error { return nil }
func (m *mockAPIGateway) Register(account *api.Account)               {}

// compile-time check
var _ apigw.Gateway = (*mockAPIGateway)(nil)

// testLogger implements Logger for testing.
type testLogger struct {
	mu     sync.Mutex
	infos  []string
	errors []string
}

func (l *testLogger) Info(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, msg)
}

func (l *testLogger) Error(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, msg)
}

// compile-time check
var _ Logger = (*testLogger)(nil)

// ── Test Helpers ─────────────────────────────────────────────────────────────────

// newTestAgent creates an Agent with real lightweight dependencies (Holder,
// HubProvider, AccountPool) and optional mock tool gateway.
//
// When mockGW is nil, a real DefaultGateway backed by the real Holder is used.
// When mockGW is provided, it replaces the tool gateway for precise test control.
func newTestAgent(t *testing.T, mockGW *mockToolGateway) *Agent {
	t.Helper()

	// Real Hub + HubProvider (cheap to create, no server started)
	hubRouter := hubprov.NewHubRouter()
	hub := hubbase.New(hubRouter)
	hubProv, err := hubprov.NewHubProvider(hub, time.Second)
	if err != nil {
		t.Fatalf("NewHubProvider: %v", err)
	}

	// Real Holder
	h := holder.New()
	h.Register(hubProv)

	// Tool gateway: mock or real DefaultGateway
	var tg toolgw.Gateway
	if mockGW != nil {
		tg = mockGW
	} else {
		tg = toolgw.NewDefaultGateway(h)
	}

	// Real AccountPool with a stub account
	pool := api.NewAccountPool(&api.Account{
		Name:     "test",
		Provider: api.ProviderOpenAI,
		Model:    "test-model",
	})

	return &Agent{
		llmClient:   api.NewChatClient(types.LLMConfig{BaseURL: "http://localhost:9999", Model: "test"}),
		tools:       h,
		apiGW:       &mockAPIGateway{},
		toolGW:      tg,
		pool:        pool,
		hub:         hub,
		hubProvider: hubProv,
		opts:        Options{Logger: &testLogger{}, ToolCallTimeOut: time.Second},
		shutdown:    make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// ── Agent Construction ───────────────────────────────────────────────────────────

func TestNew(t *testing.T) {
	a, err := New(Options{
		LLMConfig: types.LLMConfig{
			BaseURL: "http://localhost:9999",
			Model:   "test-model",
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer a.Shutdown()
}

// ── Accessors ────────────────────────────────────────────────────────────────────

func TestAgent_Hub(t *testing.T) {
	a := newTestAgent(t, nil)
	h := a.Hub()
	if h == nil {
		t.Fatal("Hub() returned nil")
	}
	if h.ProviderName() != "microhub" {
		t.Errorf("Hub().ProviderName() = %q, want %q", h.ProviderName(), "microhub")
	}
}

func TestAgent_Tools(t *testing.T) {
	a := newTestAgent(t, nil)
	if h := a.Tools(); h == nil {
		t.Error("Tools() returned nil")
	}
}

func TestAgent_AccountPool(t *testing.T) {
	a := newTestAgent(t, nil)
	if p := a.AccountPool(); p == nil {
		t.Error("AccountPool() returned nil")
	}
}

func TestAgent_LLM(t *testing.T) {
	a := newTestAgent(t, nil)
	if llm := a.LLM(); llm == nil {
		t.Error("LLM() returned nil")
	}
}

func TestAgent_MCP(t *testing.T) {
	a := newTestAgent(t, nil)

	m := a.MCP()
	if m == nil {
		t.Fatal("MCP() returned nil")
	}
	if m.ProviderName() != "mcp" {
		t.Errorf("MCP().ProviderName() = %q, want %q", m.ProviderName(), "mcp")
	}

	// Subsequent call returns the same instance (lazy init singleton)
	m2 := a.MCP()
	if m != m2 {
		t.Error("MCP() should return the same instance on subsequent calls")
	}
}

// ── Lifecycle ────────────────────────────────────────────────────────────────────

func TestAgent_Shutdown(t *testing.T) {
	a := newTestAgent(t, nil)

	// First shutdown completes without error
	a.Shutdown()

	// Second shutdown is a no-op (should not panic)
	a.Shutdown()
}

// ── Tool Registration ────────────────────────────────────────────────────────────

func TestAgent_RegisterTool(t *testing.T) {
	a := newTestAgent(t, nil)
	defer a.Shutdown()

	a.RegisterTool("test-tool", "a test tool",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return "result", nil
		},
	)

	// Verify tool appears in the tools list
	tools := a.Tools().Tools()
	found := false
	for _, tl := range tools {
		if tl.Function.Name == "test-tool" {
			found = true
			if tl.Function.Description != "a test tool" {
				t.Errorf("description = %q, want %q", tl.Function.Description, "a test tool")
			}
			break
		}
	}
	if !found {
		t.Error("test-tool not found in tools list after RegisterTool")
	}
}

// Tests RegisterTool with an optional output schema.
func TestAgent_RegisterToolWithOutputSchema(t *testing.T) {
	a := newTestAgent(t, nil)
	defer a.Shutdown()

	outputSchema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
	}
	a.RegisterTool("schema-tool", "with schema",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return `{"x":"hello"}`, nil
		},
		outputSchema,
	)

	result, err := a.DirectDispatch(context.Background(), "schema-tool", `{}`)
	if err != nil {
		t.Fatalf("DirectDispatch error = %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Errorf("expected result containing 'hello', got %q", result)
	}
}

// ── Visible Tools ────────────────────────────────────────────────────────────────

func TestAgent_VisibleTools(t *testing.T) {
	a := newTestAgent(t, nil)
	defer a.Shutdown()

	a.RegisterTool("vis-tool", "visible tool",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return "ok", nil
		},
	)

	tools := a.VisibleTools(context.Background())
	found := false
	for _, tl := range tools {
		if tl.Function.Name == "vis-tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("vis-tool not found in VisibleTools")
	}
}

// ── DirectDispatch ───────────────────────────────────────────────────────────────

func TestAgent_DirectDispatch(t *testing.T) {
	a := newTestAgent(t, nil)
	defer a.Shutdown()

	a.RegisterTool("dd-tool", "direct dispatch tool",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return "direct-result", nil
		},
	)

	result, err := a.DirectDispatch(context.Background(), "dd-tool", `{}`)
	if err != nil {
		t.Fatalf("DirectDispatch error = %v", err)
	}
	if result != "direct-result" {
		t.Errorf("DirectDispatch result = %q, want %q", result, "direct-result")
	}
}

// ── Dispatch: Error Paths ────────────────────────────────────────────────────────

func TestAgent_DispatchContextCancel(t *testing.T) {
	dispatchCalled := make(chan struct{})
	mockGW := &mockToolGateway{
		dispatchFunc: func(ctx context.Context, name, argsJSON string) (string, error) {
			close(dispatchCalled)
			// Block until context is cancelled, then return the error
			<-ctx.Done()
			return "", ctx.Err()
		},
	}

	a := newTestAgent(t, mockGW)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := a.Dispatch(ctx, "some-tool", `{}`)
		errCh <- err
	}()

	// Wait for dispatch to enter the mock handler, then cancel
	<-dispatchCalled
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from Dispatch with cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch timed out waiting for context cancellation")
	}
}

func TestAgent_DispatchGracefulShutdown(t *testing.T) {
	blockDispatch := make(chan struct{})
	dispatchStarted := make(chan struct{})

	mockGW := &mockToolGateway{
		dispatchFunc: func(ctx context.Context, name, argsJSON string) (string, error) {
			close(dispatchStarted)
			<-blockDispatch
			return "completed", nil
		},
	}

	a := newTestAgent(t, mockGW)

	// Start a dispatch that will block inside the mock handler
	errCh := make(chan error, 1)
	go func() {
		_, err := a.Dispatch(context.Background(), "slow-tool", `{}`)
		errCh <- err
	}()

	<-dispatchStarted

	// Start Shutdown in background — it should block until dispatch finishes
	shutdownDone := make(chan struct{})
	go func() {
		a.Shutdown()
		close(shutdownDone)
	}()

	// Shutdown must NOT complete while dispatch is in-flight
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown completed before in-flight dispatch finished")
	case <-time.After(100 * time.Millisecond):
		// Expected
	}

	// Unblock the dispatch
	close(blockDispatch)

	// Shutdown should now complete
	select {
	case <-shutdownDone:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not complete after dispatch finished")
	}

	// Verify the in-flight dispatch was not interrupted
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Dispatch error after graceful shutdown = %v", err)
		}
	default:
		// Dispatch may have already been collected
	}
}

// ── Dispatch: Shutdown Guard ─────────────────────────────────────────────────────

func TestAgent_DispatchBeforeShutdown(t *testing.T) {
	a := newTestAgent(t, nil)
	defer a.Shutdown()

	a.RegisterTool("pre-shutdown-tool", "tool",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return "from dispatch", nil
		},
	)

	result, err := a.Dispatch(context.Background(), "pre-shutdown-tool", `{}`)
	if err != nil {
		t.Fatalf("Dispatch error = %v", err)
	}
	if result != "from dispatch" {
		t.Errorf("result = %q, want %q", result, "from dispatch")
	}
}

func TestAgent_DispatchAfterShutdown(t *testing.T) {
	a := newTestAgent(t, nil)

	a.RegisterTool("post-shutdown-tool", "tool",
		map[string]interface{}{"type": "object"},
		func(ctx context.Context, argsJSON string) (string, error) {
			return "should not reach", nil
		},
	)

	a.Shutdown()

	_, err := a.Dispatch(context.Background(), "post-shutdown-tool", `{}`)
	if err == nil {
		t.Error("expected error dispatching after shutdown")
	}
}

// ── MCP after Shutdown ───────────────────────────────────────────────────────────

func TestAgent_MCPAfterShutdownReturnsNil(t *testing.T) {
	a := newTestAgent(t, nil)
	a.Shutdown()

	m := a.MCP()
	if m != nil {
		t.Error("MCP() should return nil after shutdown")
	}
}

// ── Options ──────────────────────────────────────────────────────────────────────

func TestOptionsZeroValues(t *testing.T) {
	var o Options
	o.withDefaults()

	if o.HubAddr != ":0" {
		t.Errorf("HubAddr = %q, want %q", o.HubAddr, ":0")
	}
	if o.ToolCallTimeOut != 5*time.Second {
		t.Errorf("ToolCallTimeOut = %v, want 5s", o.ToolCallTimeOut)
	}
	if o.Logger == nil {
		t.Error("Logger should be set to non-nil after withDefaults")
	}
	if o.HubStartupDelay != 100*time.Millisecond {
		t.Errorf("HubStartupDelay = %v, want 100ms", o.HubStartupDelay)
	}
}

// ── Summary ──────────────────────────────────────────────────────────────────────

func TestSummaryFields(t *testing.T) {
	s := Summary{
		Index:     2,
		Label:     "test-label",
		SessionID: "session-xyz",
		MsgCount:  7,
		IsCurrent: true,
	}

	if s.Index != 2 {
		t.Errorf("Index = %d, want 2", s.Index)
	}
	if s.Label != "test-label" {
		t.Errorf("Label = %q, want %q", s.Label, "test-label")
	}
	if s.SessionID != "session-xyz" {
		t.Errorf("SessionID = %q, want %q", s.SessionID, "session-xyz")
	}
	if s.MsgCount != 7 {
		t.Errorf("MsgCount = %d, want 7", s.MsgCount)
	}
	if !s.IsCurrent {
		t.Error("IsCurrent should be true")
	}
}
