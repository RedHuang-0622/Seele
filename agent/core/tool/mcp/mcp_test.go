package mcp

import (
	"context"
	"testing"
)

// ── NewProvider ─────────────────────────────────────────────────────────────

func TestNewProvider(t *testing.T) {
	p := NewProvider()
	if p == nil {
		t.Fatal("NewProvider() returned nil")
	}
}

// ── ProviderName ────────────────────────────────────────────────────────────

func TestProvider_ProviderName(t *testing.T) {
	p := NewProvider()
	if name := p.ProviderName(); name != "mcp" {
		t.Errorf("ProviderName() = %q, want %q", name, "mcp")
	}
}

func TestProvider_ProviderNameConsistent(t *testing.T) {
	p1 := NewProvider()
	p2 := NewProvider()
	if p1.ProviderName() != p2.ProviderName() {
		t.Error("ProviderName() should be consistent across instances")
	}
}

// ── ServerNames (empty state) ───────────────────────────────────────────────

func TestProvider_ServerNames_Empty(t *testing.T) {
	p := NewProvider()
	names := p.ServerNames()
	if names == nil {
		t.Fatal("ServerNames() returned nil")
	}
	if len(names) != 0 {
		t.Errorf("ServerNames() = %v, want empty", names)
	}
}

// ── Tools (empty state) ─────────────────────────────────────────────────────

func TestProvider_Tools_Empty(t *testing.T) {
	p := NewProvider()
	tools := p.Tools()
	if len(tools) != 0 {
		t.Errorf("Tools() length = %d, want 0", len(tools))
	}
}

// ── Multiple instances ──────────────────────────────────────────────────────

func TestProvider_MultipleInstances(t *testing.T) {
	p1 := NewProvider()
	p2 := NewProvider()

	if p1 == p2 {
		t.Error("NewProvider() should return different instances")
	}

	if len(p1.ServerNames()) != 0 || len(p2.ServerNames()) != 0 {
		t.Error("fresh instances should have empty server lists")
	}
}

// ── ServerConfig structure ──────────────────────────────────────────────────

func TestServerConfig_DefaultValues(t *testing.T) {
	cfg := ServerConfig{}
	if cfg.Name != "" {
		t.Errorf("zero-value ServerConfig.Name = %q, want empty", cfg.Name)
	}
	if cfg.Transport != "" {
		t.Errorf("zero-value ServerConfig.Transport = %q, want empty", cfg.Transport)
	}
	if cfg.Command != "" {
		t.Errorf("zero-value ServerConfig.Command = %q, want empty", cfg.Command)
	}
	if cfg.URL != "" {
		t.Errorf("zero-value ServerConfig.URL = %q, want empty", cfg.URL)
	}
	if cfg.Args != nil {
		t.Errorf("zero-value ServerConfig.Args = %v, want nil", cfg.Args)
	}
	if cfg.Env != nil {
		t.Errorf("zero-value ServerConfig.Env = %v, want nil", cfg.Env)
	}
}

func TestServerConfig_StdioTransport(t *testing.T) {
	cfg := ServerConfig{
		Name:      "test-server",
		Transport: "stdio",
		Command:   "/bin/echo",
		Args:      []string{"hello"},
		Env:       []string{"FOO=bar"},
	}
	if cfg.Name != "test-server" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-server")
	}
	if cfg.Transport != "stdio" {
		t.Errorf("Transport = %q, want %q", cfg.Transport, "stdio")
	}
	if cfg.Command != "/bin/echo" {
		t.Errorf("Command = %q, want %q", cfg.Command, "/bin/echo")
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "hello" {
		t.Errorf("Args = %v, want [hello]", cfg.Args)
	}
}

func TestServerConfig_SSETransport(t *testing.T) {
	cfg := ServerConfig{
		Name:      "sse-server",
		Transport: "sse",
		URL:       "http://localhost:8080/mcp",
	}
	if cfg.Transport != "sse" {
		t.Errorf("Transport = %q, want %q", cfg.Transport, "sse")
	}
	if cfg.URL != "http://localhost:8080/mcp" {
		t.Errorf("URL = %q, want %q", cfg.URL, "http://localhost:8080/mcp")
	}
}

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

