// Permission-control smoke that drives the real DefaultGateway permission gate
// through the full assembly chain (session.ReAct -> agent -> gateway.Dispatch).
//
// Two layers are covered:
//
//	offline: a scripted OpenAI-compatible mock LLM (no credentials) proves that
//	         the gate blocks/allow tool calls per subject, that control tools are
//	         root-only, and that an approval grants a reusable elevation.
//	real API: an optional case (RUN_REAL_API_SMOKE=true) that asks a real model
//	         to call a tool and asserts the same gate decisions end-to-end.
package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/session"
	roottools "github.com/RedHuang-0622/Seele/tools"
	toolgateway "github.com/RedHuang-0622/Seele/tools/gateway"
	"github.com/RedHuang-0622/Seele/tools/holder"
	"github.com/RedHuang-0622/Seele/tools/permission"
	"github.com/RedHuang-0622/Seele/types"
)

// ── subject plumbing: the product-like resolver reads the subject from ctx ───

type subjectCtxKey struct{}

func withSubject(ctx context.Context, subject permission.Subject) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, subject)
}

func subjectFromCtx(ctx context.Context) permission.Subject {
	if subject, ok := ctx.Value(subjectCtxKey{}).(permission.Subject); ok {
		return subject
	}
	return permission.SubjectAnonymous
}

// ── a tiny meta-carrying provider so the gate has tools to route ─────────────

type permProvider struct {
	name    string
	entries []roottools.ToolEntry
}

func (p *permProvider) ProviderName() string         { return p.name }
func (p *permProvider) Tools() []roottools.ToolEntry { return p.entries }

func permEntry(name string, meta *roottools.ToolMeta) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        name,
				Description: "permission smoke tool " + name,
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
		Handler: roottools.HandlerFunc(func(context.Context, string) (string, error) {
			return "executed:" + name, nil
		}),
		Meta: meta,
	}
}

func permSmokeProvider() *permProvider {
	return &permProvider{
		name: "perm",
		entries: []roottools.ToolEntry{
			permEntry("read_doc", &roottools.ToolMeta{Kind: roottools.ToolKindRead}),
			permEntry("write_doc", &roottools.ToolMeta{Kind: roottools.ToolKindWrite}),
			permEntry("ctl_stop", &roottools.ToolMeta{Kind: roottools.ToolKindControl, Signal: roottools.SignalTerm}),
		},
	}
}

// permSmokeConfig grants editor read+write, guest read only. The rw group is
// allow-by-default so an approval-based elevation must be exercised by a
// dedicated config (see permElevationConfig).
func permSmokeConfig() permission.PermissionConfig {
	return permission.PermissionConfig{
		Mode: permission.ModeManual,
		Groups: []permission.PermissionGroup{
			{Name: "ro", Match: []string{"read_*"}, Mode: permission.BitRead, Default: permission.ActionAllow},
			{Name: "rw", Match: []string{"write_*"}, Mode: permission.BitRead | permission.BitWrite, Default: permission.ActionAllow},
		},
		Subjects: map[permission.Subject]permission.SubjectGrant{
			"editor": {Bits: map[string]permission.GrantBit{
				"ro": {Bits: permission.BitRead},
				"rw": {Bits: permission.BitRead | permission.BitWrite},
			}},
			"guest": {Bits: map[string]permission.GrantBit{
				"ro": {Bits: permission.BitRead},
			}},
		},
	}
}

// permElevationConfig keeps the bit set complete but makes the group ask, so a
// subject that holds the bits still needs an approval before the handler runs.
func permElevationConfig() permission.PermissionConfig {
	return permission.PermissionConfig{
		Mode: permission.ModeManual,
		Groups: []permission.PermissionGroup{
			{Name: "rw", Match: []string{"write_*"}, Mode: permission.BitRead | permission.BitWrite, Default: permission.ActionAsk},
		},
		Subjects: map[permission.Subject]permission.SubjectGrant{
			"editor": {Bits: map[string]permission.GrantBit{
				"rw": {Bits: permission.BitRead | permission.BitWrite},
			}},
		},
	}
}

// ── assembly ────────────────────────────────────────────────────────────────

func newPermSmokeRuntime(t testing.TB, client agent.Completer, cfg permission.PermissionConfig, approval permission.ApprovalHandler) *agent.Agent {
	t.Helper()
	toolHolder := holder.New()
	toolHolder.Register(permSmokeProvider())
	gateway := toolgateway.NewDefaultGateway(toolHolder)
	gateway.SetSubjectResolver(subjectFromCtx)
	gateway.SetPermissionConfig(cfg, approval)

	components := agent.Components{Completer: client, Tools: gateway}
	if streamer, ok := client.(agent.StreamCompleter); ok {
		components.StreamCompleter = streamer
	}
	if events, ok := client.(agent.StreamEventCompleter); ok {
		components.EventCompleter = events
	}
	runtime, err := agent.NewWithComponents(components)
	if err != nil {
		t.Fatalf("agent.NewWithComponents: %v", err)
	}
	return runtime
}

// scriptSingleToolCall makes the mock answer the next user turn with one tool
// call, then answer the tool result with a short final text.
func scriptSingleToolCall(mock *mockLLM, tool, args string) {
	mock.setScenario(&scenario{completions: []completion{
		{
			matcher: func(req openAIRequest) bool {
				return lastRole(req.Messages) == "user" && len(req.Tools) > 0
			},
			respond: func(openAIRequest) openAIResponse {
				return openAIResponse{
					ToolCalls: []openAIToolCall{{
						Index: 0, ID: "call_perm", Type: "function",
						Function: toolFunction{Name: tool, Arguments: args},
					}},
					Stop: "tool_calls",
				}
			},
		},
		{
			matcher: func(req openAIRequest) bool { return lastRole(req.Messages) == "tool" },
			respond: func(openAIRequest) openAIResponse {
				return openAIResponse{Content: "done", Stop: "stop"}
			},
		},
	}})
}

// runPermissionProbe drives one Chat as the given subject and returns the
// observed tool-call outcome for the requested tool.
func runPermissionProbe(t testing.TB, ctx context.Context, runtime *agent.Agent, subject permission.Subject, tool, args, prompt string) (session.ToolCallInfo, bool) {
	t.Helper()
	var mu sync.Mutex
	var info session.ToolCallInfo
	found := false
	chat := session.New(runtime,
		session.WithSystemPrompt("You are a permission-gating smoke agent. You must call the exact tool the user names before you answer."),
		session.WithHooks(&session.LoopHooks{OnToolComplete: func(_ context.Context, ci session.ToolCallInfo) {
			mu.Lock()
			defer mu.Unlock()
			if ci.Name == tool {
				info = ci
				found = true
			}
		}}),
	)
	if _, err := chat.Chat(withSubject(ctx, subject), prompt); err != nil {
		t.Fatalf("chat subject=%s tool=%s: %v", subject, tool, err)
	}
	mu.Lock()
	defer mu.Unlock()
	return info, found
}

// ── offline cases (always runnable) ─────────────────────────────────────────

func TestPermissionChainSubjectGate(t *testing.T) {
	mock := newMockLLM()
	defer mock.Close()
	client := newRealClient(t, mock.URL())
	runtime := newPermSmokeRuntime(t, client, permSmokeConfig(), nil)
	defer runtime.Shutdown()

	// editor holds the rw bits -> the tool executes.
	scriptSingleToolCall(mock, "write_doc", `{}`)
	editor, ok := runPermissionProbe(t, context.Background(), runtime, "editor", "write_doc", `{}`, "Call the write_doc tool now.")
	if !ok || editor.Error != nil || editor.Result != "executed:write_doc" {
		t.Fatalf("editor write_doc = %+v ok=%v, want executed without error", editor, ok)
	}

	// guest lacks the write bit -> not visible ("not in PATH").
	scriptSingleToolCall(mock, "write_doc", `{}`)
	guest, ok := runPermissionProbe(t, context.Background(), runtime, "guest", "write_doc", `{}`, "Call the write_doc tool now.")
	if !ok {
		t.Fatalf("guest write_doc was never dispatched")
	}
	if !errors.Is(guest.Error, roottools.ErrToolNotVisible) {
		t.Fatalf("guest write_doc err = %v, want ErrToolNotVisible", guest.Error)
	}
	if errors.Is(guest.Error, roottools.ErrPermissionDenied) {
		t.Fatalf("guest write_doc must not report ErrPermissionDenied")
	}

	// guest still holds the read bit -> read_doc executes.
	scriptSingleToolCall(mock, "read_doc", `{}`)
	guestRead, ok := runPermissionProbe(t, context.Background(), runtime, "guest", "read_doc", `{}`, "Call the read_doc tool now.")
	if !ok || guestRead.Error != nil || guestRead.Result != "executed:read_doc" {
		t.Fatalf("guest read_doc = %+v ok=%v, want executed without error", guestRead, ok)
	}
	t.Logf("offline subject gate ok; llm_calls=%d", mock.Calls())
}

func TestPermissionChainControlToolRootOnly(t *testing.T) {
	mock := newMockLLM()
	defer mock.Close()
	client := newRealClient(t, mock.URL())
	runtime := newPermSmokeRuntime(t, client, permSmokeConfig(), nil)
	defer runtime.Shutdown()

	// ctl_stop is a control tool -> non-root subjects cannot route it.
	scriptSingleToolCall(mock, "ctl_stop", `{}`)
	editor, ok := runPermissionProbe(t, context.Background(), runtime, "editor", "ctl_stop", `{}`, "Call the ctl_stop tool now.")
	if !ok || !errors.Is(editor.Error, roottools.ErrToolNotVisible) {
		t.Fatalf("editor ctl_stop = %+v ok=%v, want ErrToolNotVisible", editor, ok)
	}

	// root may route it.
	scriptSingleToolCall(mock, "ctl_stop", `{}`)
	root, ok := runPermissionProbe(t, context.Background(), runtime, permission.SubjectRoot, "ctl_stop", `{}`, "Call the ctl_stop tool now.")
	if !ok || root.Error != nil || root.Result != "executed:ctl_stop" {
		t.Fatalf("root ctl_stop = %+v ok=%v, want executed without error", root, ok)
	}
}

func TestPermissionChainApprovalGrantsElevation(t *testing.T) {
	mock := newMockLLM()
	defer mock.Close()
	client := newRealClient(t, mock.URL())

	var mu sync.Mutex
	approvals := 0
	approval := permission.ApprovalHandler(func(*permission.ApprovalContext) (*permission.ApprovalResponse, error) {
		mu.Lock()
		approvals++
		mu.Unlock()
		return &permission.ApprovalResponse{Choice: "allow", Scope: permission.ScopeSession}, nil
	})
	runtime := newPermSmokeRuntime(t, client, permElevationConfig(), approval)
	defer runtime.Shutdown()

	// First call: bits are present but the group asks -> one approval, then the
	// approval records a session-scoped elevation on the shared checker.
	scriptSingleToolCall(mock, "write_doc", `{}`)
	first, ok := runPermissionProbe(t, context.Background(), runtime, "editor", "write_doc", `{}`, "Call the write_doc tool now.")
	if !ok || first.Error != nil || first.Result != "executed:write_doc" {
		t.Fatalf("first write_doc = %+v ok=%v, want executed after approval", first, ok)
	}

	// Second call: the elevation must be reused, so no second approval fires.
	scriptSingleToolCall(mock, "write_doc", `{}`)
	second, ok := runPermissionProbe(t, context.Background(), runtime, "editor", "write_doc", `{}`, "Call the write_doc tool now.")
	if !ok || second.Error != nil || second.Result != "executed:write_doc" {
		t.Fatalf("second write_doc = %+v ok=%v, want executed via elevation", second, ok)
	}

	mu.Lock()
	count := approvals
	mu.Unlock()
	if count != 1 {
		t.Fatalf("approval calls = %d, want 1 (elevation not reused)", count)
	}
}

// ── real API case (opt-in) ──────────────────────────────────────────────────

func TestRealAPIPermissionSmoke(t *testing.T) {
	if os.Getenv("RUN_REAL_API_SMOKE") != "true" {
		t.Skip("set RUN_REAL_API_SMOKE=true and provide SMOKE_CONFIG or SEELE_SMOKE_* credentials")
	}
	options := optionsFromEnvironment()
	client, model, err := newChatClient(options)
	if err != nil {
		t.Fatalf("create real client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()

	runtime := newPermSmokeRuntime(t, client, permSmokeConfig(), nil)
	defer runtime.Shutdown()

	// editor holds the write bit -> the real model's write_doc call must run.
	editor, ok := runPermissionProbe(t, ctx, runtime, "editor", "write_doc", `{}`,
		"Call the write_doc tool with an empty JSON object argument, then tell me its result.")
	if !ok {
		t.Fatalf("model=%s did not call write_doc for editor", model)
	}
	if editor.Error != nil || editor.Result != "executed:write_doc" {
		t.Fatalf("editor write_doc = %+v, want executed without error", editor)
	}

	// guest lacks the write bit -> even if the model calls it, the gate blocks.
	guest, ok := runPermissionProbe(t, ctx, runtime, "guest", "write_doc", `{}`,
		"Call the write_doc tool with an empty JSON object argument, then tell me its result.")
	if !ok {
		t.Fatalf("model=%s did not call write_doc for guest", model)
	}
	if !errors.Is(guest.Error, roottools.ErrToolNotVisible) {
		t.Fatalf("guest write_doc err = %v, want ErrToolNotVisible", guest.Error)
	}
	if strings.Contains(guest.Result, "executed:write_doc") {
		t.Fatalf("guest write_doc handler ran despite missing bit: %q", guest.Result)
	}
	t.Logf("real API permission smoke passed model=%s", model)
}
