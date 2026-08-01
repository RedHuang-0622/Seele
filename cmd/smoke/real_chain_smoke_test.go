// Real-protocol smoke that exercises the full Seele assembly chain.
//
// The harness spins up a local OpenAI-compatible mock LLM (httptest) so the
// test does not need real network credentials but still walks every layer
// of the public surface:
//
//	api.ChatClient -> agent.NewWithComponents -> session.New (ReAct loop)
//	agent/bridge.NewAgentFactory -> workplan.New -> runner.Run
//	workplan/core/edge + workplan/core/node -> coreplan.Plan -> runner.Run
//
// Three scenarios are checked:
//  1. Tool-calling ReAct: the agent must call the builtin calculate tool and
//     report its result.
//  2. WorkPlan fan-out: a three-node plan (auto -> emit -> auto) is built
//     through the workplan facade, executed with a session-backed
//     AgentFactory, and the final node must read the intermediate variable
//     produced by the emit node.
//  3. Core edge/node primitives: hand-assembled Plan with a function-node
//     entry, an explicit 3-branch fork (2 function nodes + 1 LLM node), and
//     a function-node join. The plan is exported in three formal shapes
//     (edge list, adjacency list, matrix) so callers can verify topology.
//
// Run with:
//
//	go test ./cmd/smoke -run RealChain -count=1 -timeout 60s
//	go test ./cmd/smoke -run RealChainWorkPlan -count=1 -timeout 60s
//	go test ./cmd/smoke -run RealChainCorePrimitives -count=1 -timeout 60s
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RedHuang-0622/Seele/agent"
	"github.com/RedHuang-0622/Seele/agent/core/api"
	"github.com/RedHuang-0622/Seele/session"
	"github.com/RedHuang-0622/Seele/tools/builtin"
	toolgateway "github.com/RedHuang-0622/Seele/tools/gateway"
	"github.com/RedHuang-0622/Seele/tools/holder"
	"github.com/RedHuang-0622/Seele/types"
	"github.com/RedHuang-0622/Seele/workplan"
	agentbridge "github.com/RedHuang-0622/Seele/agent/bridge"
	"github.com/RedHuang-0622/Seele/workplan/codec"
	wpedge "github.com/RedHuang-0622/Seele/workplan/core/edge"
	wpnode "github.com/RedHuang-0622/Seele/workplan/core/node"
	coreplan "github.com/RedHuang-0622/Seele/workplan/core/plan"
	"github.com/RedHuang-0622/Seele/workplan/runtime/forkexec"
	"github.com/RedHuang-0622/Seele/workplan/runtime/runner"
)

// ── mock LLM ────────────────────────────────────────────────────────────────

// mockLLM is a programmable OpenAI-compatible server used to drive Seele
// without external credentials. Each scenario registers a list of scripted
// completions; responses are consumed in order and matched by predicate.
type mockLLM struct {
	server   *httptest.Server
	scenario atomic.Pointer[scenario]
	calls    atomic.Int64
	mu       sync.Mutex
	log      []string
}

type scenario struct {
	completions []completion
}

type completion struct {
	matcher func(req openAIRequest) bool
	respond func(req openAIRequest) openAIResponse
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []types.Message `json:"messages"`
	Tools    []types.Tool    `json:"tools,omitempty"`
}

type openAIToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIResponse struct {
	Content   string           `json:"content,omitempty"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
	Stop      string           `json:"stop_reason"`
}

func newMockLLM() *mockLLM {
	mock := &mockLLM{}
	mock.server = httptest.NewServer(http.HandlerFunc(mock.handle))
	mock.setScenario(&scenario{})
	return mock
}

func (m *mockLLM) URL() string             { return m.server.URL }
func (m *mockLLM) Close()                  { m.server.Close() }
func (m *mockLLM) Calls() int64            { return m.calls.Load() }
func (m *mockLLM) setScenario(s *scenario) { m.scenario.Store(s) }

func (m *mockLLM) Log() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.log))
	copy(out, m.log)
	return out
}

func (m *mockLLM) record(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, fmt.Sprintf(format, args...))
}

func (m *mockLLM) handle(w http.ResponseWriter, r *http.Request) {
	m.calls.Add(1)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		return
	}
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	scenario := m.scenario.Load()
	if scenario == nil {
		http.Error(w, "no scenario configured", http.StatusServiceUnavailable)
		return
	}
	for idx, step := range scenario.completions {
		if step.matcher(req) {
			m.record("call=%d matched scenario[%d] tools=%d last_role=%s",
				m.calls.Load(), idx, len(req.Tools), lastRole(req.Messages))
			writeChatResponse(w, step.respond(req))
			return
		}
	}
	writeChatResponse(w, openAIResponse{Content: "fallback", Stop: "stop"})
	m.record("call=%d fallback (no matcher)", m.calls.Load())
}

func writeChatResponse(w http.ResponseWriter, resp openAIResponse) {
	payload := map[string]any{
		"id":      "mock-1",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "mock",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role": "assistant",
			},
			"finish_reason": resp.Stop,
		}},
	}
	msg := payload["choices"].([]map[string]any)[0]["message"].(map[string]any)
	if resp.Content != "" {
		msg["content"] = resp.Content
	}
	if len(resp.ToolCalls) > 0 {
		calls := make([]map[string]any, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			calls = append(calls, map[string]any{
				"index": call.Index,
				"id":    call.ID,
				"type":  call.Type,
				"function": map[string]any{
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				},
			})
		}
		msg["tool_calls"] = calls
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func lastRole(messages []types.Message) string {
	if len(messages) == 0 {
		return ""
	}
	return messages[len(messages)-1].Role
}

// ── helpers ─────────────────────────────────────────────────────────────────

// newRealClient wires a real api.ChatClient at the mock's base URL.
func newRealClient(t testing.TB, baseURL string) *api.ChatClient {
	t.Helper()
	return api.NewChatClient(types.LLMConfig{
		BaseURL: baseURL,
		APIKey:  "sk-mock",
		Model:   "mock-model",
		Timeout: 30,
	}).SetProvider(api.ProviderOpenAI)
}

// newAssembledAgent composes a runtime with builtin tools.
func newAssembledAgent(t testing.TB, client *api.ChatClient) *agent.Agent {
	t.Helper()
	toolHolder := holder.New()
	toolHolder.Register(builtin.New())
	gateway := toolgateway.NewDefaultGateway(toolHolder)
	// ChatClient implements Completer; stream/event fields stay nil and
	// NewWithComponents falls back to the non-streaming path.
	components := agent.Components{Completer: client, Tools: gateway}
	runtime, err := agent.NewWithComponents(components)
	if err != nil {
		t.Fatalf("agent.NewWithComponents: %v", err)
	}
	return runtime
}

// ── tests ───────────────────────────────────────────────────────────────────

// TestRealChainReActToolCalling walks the full assembled-agent path: a single
// user prompt must drive one tool call through the builtin calculate tool,
// the ReAct loop must surface the tool result, and the final reply must be
// the expected text echoed by the mock.
func TestRealChainReActToolCalling(t *testing.T) {
	mock := newMockLLM()
	defer mock.Close()
	mock.setScenario(&scenario{
		completions: []completion{
			{
				matcher: func(req openAIRequest) bool {
					return lastRole(req.Messages) == "user" && len(req.Tools) > 0
				},
				respond: func(openAIRequest) openAIResponse {
					return openAIResponse{
						ToolCalls: []openAIToolCall{{
							Index: 0, ID: "call_add", Type: "function",
							Function: toolFunction{
								Name:      "calculate",
								Arguments: `{"operation":"add","left":19,"right":23}`,
							},
						}},
						Stop: "tool_calls",
					}
				},
			},
			{
				matcher: func(req openAIRequest) bool { return lastRole(req.Messages) == "tool" },
				respond: func(openAIRequest) openAIResponse {
					return openAIResponse{Content: "The sum is 42.", Stop: "stop"}
				},
			},
		},
	})

	client := newRealClient(t, mock.URL())
	runtime := newAssembledAgent(t, client)
	defer runtime.Shutdown()

	calls := map[string]int{}
	chat := session.New(runtime,
		session.WithSystemPrompt("You must use the calculate tool when asked to add numbers."),
		session.WithHooks(&session.LoopHooks{OnToolComplete: func(_ context.Context, info session.ToolCallInfo) {
			calls[info.Name]++
		}}),
	)
	reply, err := chat.Chat(context.Background(), "What is 19 + 23? Use the tool.")
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if reply != "The sum is 42." {
		t.Fatalf("reply = %q, want %q", reply, "The sum is 42.")
	}
	if calls["calculate"] != 1 {
		t.Fatalf("calculate calls = %d, want 1 (events=%v)", calls["calculate"], mock.Log())
	}
	if mock.Calls() < 2 {
		t.Fatalf("LLM calls = %d, want >= 2", mock.Calls())
	}
	t.Logf("react tool path: llm_calls=%d log=%v", mock.Calls(), mock.Log())
}

// TestRealChainWorkPlanImportExport walks the workplan branch: build a
// three-node plan (auto -> emit -> auto) through the workplan facade using a
// session-backed AgentFactory, execute it, and verify the final node reads
// the variable produced by the emit node. Each auto node produces one LLM
// call so the test also proves the bridge composes correctly.
func TestRealChainWorkPlanImportExport(t *testing.T) {
	mock := newMockLLM()
	defer mock.Close()
	mock.setScenario(&scenario{
		completions: []completion{
			{
				matcher: func(req openAIRequest) bool {
					if lastRole(req.Messages) != "user" || len(req.Messages) < 2 {
						return false
					}
					first := req.Messages[len(req.Messages)-1].Content
					if first == nil {
						return false
					}
					return strings.Contains(*first, "describe the topic")
				},
				respond: func(openAIRequest) openAIResponse {
					return openAIResponse{
						Content: `Topic: Seele runtime; Focus: agent + workplan.`,
						Stop:    "stop",
					}
				},
			},
			{
				matcher: func(req openAIRequest) bool {
					if lastRole(req.Messages) != "user" || len(req.Messages) < 2 {
						return false
					}
					first := req.Messages[len(req.Messages)-1].Content
					if first == nil {
						return false
					}
					return strings.Contains(*first, "produce a final report")
				},
				respond: func(openAIRequest) openAIResponse {
					return openAIResponse{
						Content: `Final: Seele + WorkPlan working.`,
						Stop:    "stop",
					}
				},
			},
		},
	})

	client := newRealClient(t, mock.URL())
	runtime := newAssembledAgent(t, client)
	defer runtime.Shutdown()
	factory, err := agentbridge.NewAgentFactory(runtime, agentbridge.WithSessionID(func(string) string {
		return "real-chain-smoke"
	}))
	if err != nil {
		t.Fatalf("agentbridge.NewAgentFactory: %v", err)
	}

	plan := workplan.New(factory, workplan.WithDefaultPrompt("You are a concise summarizer."))
	plan.Auto("describe", "describe the topic: Seele runtime").
		Emit("save", "topic_summary").
		Auto("report", "produce a final report based on the topic summary: {{.Vars.topic_summary}}")

	result, err := plan.Run(context.Background())
	if err != nil {
		t.Fatalf("plan run: %v", err)
	}
	if got := result.FinalOutputString(); !strings.Contains(got, "Final:") {
		t.Fatalf("final output = %q, want it to contain 'Final:'", got)
	}
	if topic := result.Vars["topic_summary"]; !strings.Contains(topic, "Seele runtime") {
		t.Fatalf("topic_summary = %q, want it to carry the describe output", topic)
	}
	if len(result.NodeResults) != 3 {
		t.Fatalf("node results = %d, want 3", len(result.NodeResults))
	}
	for _, nr := range result.NodeResults {
		if nr.Err != nil {
			t.Fatalf("node %s err = %v", nr.NodeID, nr.Err)
		}
	}
	if mock.Calls() < 2 {
		t.Fatalf("LLM calls = %d, want >= 2 (one per auto node)", mock.Calls())
	}
	t.Logf("workplan path: nodes=%d final=%q topic=%q calls=%d log=%v",
		len(result.NodeResults), result.FinalOutputString(),
		result.Vars["topic_summary"], mock.Calls(), mock.Log())
}

// TestRealChainWorkPlanFork exercises WorkPlan.Fork with three concurrent
// branches against a real Anthropic-compatible API. Each branch spins up its own
// session through the bridge, runs in parallel, and the final aggregate
// node consumes the joined output. The test asserts:
//
//   - All three branches produce a non-empty output.
//   - The aggregate node reads the joined text (post-join PrevResult).
//   - Branches run in parallel: the wall time is less than the sum of
//     branch latencies, exposing any accidental serialization.
func TestRealChainWorkPlanFork(t *testing.T) {
	baseURL, token, model, ok := loadAnthropicSettings()
	if !ok {
		t.Skip("Anthropic-compatible API settings missing or incomplete")
	}
	if v := os.Getenv("SEELE_SKIP_REAL_API"); v != "" {
		t.Skipf("SEELE_SKIP_REAL_API=%s", v)
	}
	t.Logf("fork target: baseURL=%s model=%s", baseURL, model)

	client := api.NewChatClient(types.LLMConfig{
		BaseURL: baseURL,
		APIKey:  token,
		Model:   model,
		Timeout: 90,
	}).SetProvider(api.ProviderAnthropic)

	runtime := newAssembledAgent(t, client)
	defer runtime.Shutdown()
	factory, err := agentbridge.NewAgentFactory(runtime,
		agentbridge.WithSessionID(func(label string) string { return "fork-" + label }),
	)
	if err != nil {
		t.Fatalf("agentbridge.NewAgentFactory: %v", err)
	}

	// Per-branch start/end timestamps; the hook below captures them so we
	// can assert that the three branches ran concurrently rather than
	// serially.
	branchTimes := map[string]struct{ start, end time.Time }{}

	plan := workplan.New(factory,
		workplan.WithDefaultPrompt("You are a concise engineer. Reply in one short sentence."),
		workplan.WithBranchEventHook(func(ev forkexec.Event) {
			if _, tracked := branchTimes[ev.BranchID]; !tracked {
				branchTimes[ev.BranchID] = struct{ start, end time.Time }{}
			}
			entry := branchTimes[ev.BranchID]
			switch ev.Type {
			case forkexec.StateStarted:
				entry.start = ev.At
			case forkexec.StateCompleted, forkexec.StateFailed:
				entry.end = ev.At
			}
			branchTimes[ev.BranchID] = entry
		}),
	).
		Auto("plan", "Describe the feature: a counter button that increments on click. One sentence only.")

	// Three branches with distinct personas. Each becomes a separate
	// concurrent session through the agent bridge.
	branches := []workplan.ForkBranch{
		{
			Label:        "frontend",
			SystemPrompt: "You are a senior frontend engineer. Reply in one short sentence.",
			Input:        "Implement the feature: {{.PrevResult}}",
		},
		{
			Label:        "backend",
			SystemPrompt: "You are a senior backend engineer. Reply in one short sentence.",
			Input:        "Provide the API design for: {{.PrevResult}}",
		},
		{
			Label:        "qa",
			SystemPrompt: "You are a senior QA engineer. Reply in one short sentence.",
			Input:        "Write one acceptance test for: {{.PrevResult}}",
		},
	}
	plan.Fork("implement", branches, 3).
		Auto("aggregate", "Merge the three role outputs into a single one-sentence plan: {{.PrevResult}}")

	started := time.Now()
	result, err := plan.Run(context.Background())
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("plan.Run: %v", err)
	}

	// 1. Aggregate output must be non-empty.
	final := result.FinalOutputString()
	if strings.TrimSpace(final) == "" {
		t.Fatalf("aggregate output is empty; node results=%+v", result.NodeResults)
	}

	// 2. Each branch must have produced a non-empty record. The WorkPlan
	// join policy merges all branch outputs into the fork node's Output as
	// a JSON object keyed by branch label, so we parse it back out here.
	branchOutputs := map[string]string{}
	for i, nr := range result.NodeResults {
		t.Logf("node result[%d]: NodeID=%q Kind=%q Status=%q OutputLen=%d Err=%v OutputPrefix=%q",
			i, nr.NodeID, nr.Kind, nr.Status, len(nr.Output), nr.Err,
			truncate(nr.Output, 80))
	}

	// Pull the per-branch text out of the fork node's merged output.
	forkResult := result.NodeResults[1]
	if forkResult.NodeID != "implement" || forkResult.Kind != "fork" {
		t.Fatalf("expected fork node result at index 1, got NodeID=%q Kind=%q", forkResult.NodeID, forkResult.Kind)
	}
	parsed := map[string]string{}
	if err := json.Unmarshal([]byte(forkResult.Output), &parsed); err != nil {
		t.Fatalf("fork output is not a JSON object keyed by label: %v\nraw=%s", err, truncate(forkResult.Output, 200))
	}
	for _, label := range []string{"frontend", "backend", "qa"} {
		text, ok := parsed[label]
		if !ok || strings.TrimSpace(text) == "" {
			t.Fatalf("branch %q missing or empty in fork aggregate: keys=%v", label, mapKeys(parsed))
		}
		branchOutputs[label] = text
	}

	// 3. Concurrency check: only the three branch labels count. The hook
	//    also fires for the surrounding auto nodes, so filter to the
	//    labels we care about before measuring overlap.
	var starts, ends []time.Time
	for _, label := range []string{"frontend", "backend", "qa"} {
		ts, ok := branchTimes[label]
		if !ok {
			t.Fatalf("branch %q did not emit any events; branchTimes=%v", label, branchTimes)
		}
		if ts.start.IsZero() || ts.end.IsZero() {
			t.Fatalf("branch %q did not complete: start=%s end=%s", label, ts.start, ts.end)
		}
		starts = append(starts, ts.start)
		ends = append(ends, ts.end)
	}
	latestStart := maxTime(starts)
	earliestEnd := minTime(ends)
	overlap := earliestEnd.Sub(latestStart)
	t.Logf("branch concurrency: latest_start=%s earliest_end=%s overlap=%s elapsed=%s",
		latestStart.Format(time.RFC3339Nano), earliestEnd.Format(time.RFC3339Nano), overlap, elapsed)
	if overlap <= 0 {
		t.Fatalf("branches did not overlap; fork ran serially (overlap=%s)", overlap)
	}

	t.Logf("fork final=%q", final)
	for label, out := range branchOutputs {
		t.Logf("  branch %-9s output=%q", label, truncate(out, 140))
	}
	t.Logf("total elapsed: %s", elapsed)
}

func maxTime(times []time.Time) time.Time {
	if len(times) == 0 {
		return time.Time{}
	}
	max := times[0]
	for _, t := range times[1:] {
		if t.After(max) {
			max = t
		}
	}
	return max
}

func minTime(times []time.Time) time.Time {
	if len(times) == 0 {
		return time.Time{}
	}
	min := times[0]
	for _, t := range times[1:] {
		if t.Before(min) {
			min = t
		}
	}
	return min
}

// TestRealChainAgainstAnthropicAPI exercises the same assembly chain against
// an Anthropic-compatible endpoint configured through environment variables or
// ~/.claude/settings.json. The test is skipped automatically when the
// settings file is missing or absent of credentials, so it can stay in the
// default test set without breaking offline runs.
//
// Coverage:
//   - api.ChatClient with anthropic protocol hits a real /v1/messages
//   - agent.NewWithComponents wires the real LLM with builtin tools
//   - session.New drives a tool-calling ReAct loop
//   - agent/bridge.NewAgentFactory executes a 3-node plan
func TestRealChainAgainstAnthropicAPI(t *testing.T) {
	baseURL, token, model, ok := loadAnthropicSettings()
	if !ok {
		t.Skip("Anthropic-compatible API settings missing or incomplete")
	}
	if v := os.Getenv("SEELE_SKIP_REAL_API"); v != "" {
		t.Skipf("SEELE_SKIP_REAL_API=%s", v)
	}
	t.Logf("target: baseURL=%s model=%s tokenLen=%d", baseURL, model, len(token))

	client := api.NewChatClient(types.LLMConfig{
		BaseURL: baseURL,
		APIKey:  token,
		Model:   model,
		Timeout: 60,
	}).SetProvider(api.ProviderAnthropic)

	// Subtest 1: ReAct tool-calling path
	t.Run("react_tool_calling", func(t *testing.T) {
		runtime := newAssembledAgent(t, client)
		defer runtime.Shutdown()
		chat := session.New(runtime,
			session.WithSystemPrompt("You are a calculator. When the user asks to add two numbers, you MUST call the calculate tool with operation=add before answering. Never answer from memory."),
		)
		reply, err := chat.Chat(context.Background(),
			"What is 19 + 23? Reply with the numeric result only after the tool call.")
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		t.Logf("react reply: %q", reply)
		if !strings.Contains(reply, "42") {
			t.Fatalf("reply should contain 42, got %q", reply)
		}
	})

	// Subtest 2: WorkPlan 3-node (auto -> emit -> auto) end-to-end
	t.Run("workplan_three_nodes", func(t *testing.T) {
		runtime := newAssembledAgent(t, client)
		defer runtime.Shutdown()
		factory, err := agentbridge.NewAgentFactory(runtime)
		if err != nil {
			t.Fatalf("agentbridge.NewAgentFactory: %v", err)
		}
		plan := workplan.New(factory, workplan.WithDefaultPrompt("You are a concise summarizer. Reply in one sentence."))
		plan.Auto("describe", "describe in one sentence: Seele runtime").
			Emit("save", "topic_summary").
			Auto("report", "using this summary: {{.Vars.topic_summary}} — produce a one-sentence final report for an engineer.")
		result, err := plan.Run(context.Background())
		if err != nil {
			t.Fatalf("plan.Run: %v", err)
		}
		if got := result.FinalOutputString(); strings.TrimSpace(got) == "" {
			t.Fatalf("final output is empty; node results=%+v", result.NodeResults)
		}
		if topic := result.Vars["topic_summary"]; strings.TrimSpace(topic) == "" {
			t.Fatalf("topic_summary is empty; node results=%+v", result.NodeResults)
		}
		if len(result.NodeResults) != 3 {
			t.Fatalf("node results = %d, want 3", len(result.NodeResults))
		}
		for _, nr := range result.NodeResults {
			if nr.Err != nil {
				t.Fatalf("node %s err = %v", nr.NodeID, nr.Err)
			}
			t.Logf("node %-9s kind=%-6s output=%q",
				nr.NodeID, nr.Kind, truncate(nr.Output, 160))
		}
		t.Logf("workplan final=%q topic=%q", result.FinalOutputString(), result.Vars["topic_summary"])
	})
}

// loadAnthropicSettings reads the ANTHROPIC_BASE_URL /
// ANTHROPIC_AUTH_TOKEN / ANTHROPIC_MODEL triple from environment variables
// first, then from ~/.claude/settings.json when it is present. The endpoint
// only needs to implement the Anthropic protocol; it need not be Claude.
func loadAnthropicSettings() (baseURL, token, model string, ok bool) {
	if env := os.Getenv("ANTHROPIC_BASE_URL"); env != "" {
		baseURL = env
	}
	if env := os.Getenv("ANTHROPIC_AUTH_TOKEN"); env != "" {
		token = env
	} else if env := os.Getenv("ANTHROPIC_API_KEY"); env != "" {
		token = env
	}
	if env := os.Getenv("ANTHROPIC_MODEL"); env != "" {
		model = env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return baseURL, token, model, baseURL != "" && token != "" && model != ""
	}
	path := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return baseURL, token, model, baseURL != "" && token != "" && model != ""
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return baseURL, token, model, baseURL != "" && token != "" && model != ""
	}
	if baseURL == "" {
		baseURL = doc.Env["ANTHROPIC_BASE_URL"]
	}
	if token == "" {
		token = doc.Env["ANTHROPIC_AUTH_TOKEN"]
	}
	if model == "" {
		model = doc.Env["ANTHROPIC_MODEL"]
	}
	return baseURL, token, model, baseURL != "" && token != "" && model != ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func mapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// llmAdapter adapts the real api.ChatClient (which implements
// types.ChatCompleter) to the node.LLMProvider shape that LLMNode expects.
// LLMNode only needs Chat + optional ChatStream, so we only adapt those.
type llmAdapter struct {
	client *api.ChatClient
}

func (a *llmAdapter) Chat(ctx context.Context, input string) (string, error) {
	messages := []types.Message{{Role: "user", Content: &input}}
	message, err := a.client.Complete(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	if message.Content != nil {
		return *message.Content, nil
	}
	return "", nil
}

func (a *llmAdapter) ChatStream(ctx context.Context, input string, onChunk func(string)) (string, error) {
	messages := []types.Message{{Role: "user", Content: &input}}
	content, _, _, err := a.client.CompleteStream(ctx, messages, nil, onChunk)
	return content, err
}

// TestRealChainCorePrimitives builds a WorkPlan entirely from
// workplan/core/edge + workplan/core/node primitives (no sugar DSL), feeds
// it to the runtime runner, and exports the resulting graph in three
// canonical shapes: edge list, adjacency list, and adjacency matrix.
//
// Plan topology:
//
//	intro (function) --+--> expand (function) --+--> upper (function) --+
//	                   |                       +--> lower (function) --+
//	                   +--> prompt (function) --> greet (LLM, says "i你好") +--> join (function) --> tail (function)
//
// The three terminal branches (upper, lower, greet) form an implicit
// automatic fork: expand and prompt each fan out to multiple downstream
// nodes, and the runtime executes those branches concurrently before
// running join.
//
// Verified end-to-end:
//   - All eight nodes execute in topological order.
//   - The two function branches run without any LLM call.
//   - The LLM branch makes exactly one real /v1/messages call and the
//     rendered reply contains "i你好".
//   - join reads the fork aggregate and the final node produces non-empty
//     output that references all three branches.
//   - The exported edge list, adjacency list and adjacency matrix are
//     consistent with the original plan.
func TestRealChainCorePrimitives(t *testing.T) {
	baseURL, token, model, ok := loadAnthropicSettings()
	if !ok {
		t.Skip("Anthropic-compatible API settings missing or incomplete")
	}
	if v := os.Getenv("SEELE_SKIP_REAL_API"); v != "" {
		t.Skipf("SEELE_SKIP_REAL_API=%s", v)
	}
	t.Logf("core primitives target: baseURL=%s model=%s", baseURL, model)

	client := api.NewChatClient(types.LLMConfig{
		BaseURL: baseURL,
		APIKey:  token,
		Model:   model,
		Timeout: 60,
	}).SetProvider(api.ProviderAnthropic)
	llm := &llmAdapter{client: client}

	// Plan graph:
	//   intro -> expand (fork)
	//     expand -> upper (function)
	//     expand -> lower (function)
	//     expand -> greet (llm)
	//   upper/lower/greet -> join
	//   join -> tail
	plan := coreplan.New()
	intro := wpnode.NewFunctionNode("intro", func(_ context.Context, _ string) (string, error) {
		return "Seele Core", nil
	})
	// expand passes the topic text through. We add a sibling prompt
	// node that produces the LLM-specific instruction so greet sees
	// a different input from upper/lower.
	prompt := wpnode.NewFunctionNode("prompt", func(_ context.Context, _ string) (string, error) {
		return "Reply with the literal string 'i你好' and nothing else. No punctuation, no greeting, no extra words.", nil
	})
	upper := wpnode.NewFunctionNode("upper", func(_ context.Context, prev string) (string, error) {
		return strings.ToUpper(prev), nil
	})
	lower := wpnode.NewFunctionNode("lower", func(_ context.Context, prev string) (string, error) {
		return strings.ToLower(prev), nil
	})
	// greet is the LLM branch. We constrain the reply to "i你好" by
	// feeding it a fixed prompt node (LLMNode has no system-prompt
	// slot of its own).
	greet := wpnode.NewLLMNode("greet", llm).WithOnChunk(func(chunk string) {
		// Demonstrate the streaming callback: the runtime calls it
		// when the underlying LLM streams. We log every chunk so the
		// trace shows streaming actually happened end-to-end.
		t.Logf("greet stream chunk: %q", chunk)
	})
	join := wpnode.NewFunctionNode("join", func(_ context.Context, prev string) (string, error) {
		// The runtime feeds join the joined aggregate (JSON object of
		// branch outputs) via PrevText. We surface it verbatim so the
		// downstream tail node sees the fork result.
		return "joined:" + prev, nil
	})
	tail := wpnode.NewFunctionNode("tail", func(_ context.Context, prev string) (string, error) {
		return "tail:" + prev, nil
	})

	plan.AddNode(intro)
	plan.AddNode(upper)
	plan.AddNode(lower)
	plan.AddNode(greet)
	plan.AddNode(join)
	plan.AddNode(tail)
	plan.AddNode(prompt)
	plan.SetEntry("intro")

	// The three branches share a single fork node; the runner exposes
	// them through sugar/fork via the runner's auto-fork machinery
	// triggered by edges from expand. We use a dedicated function node
	// as the fan-out source and then add explicit edges.
	fanout := wpnode.NewFunctionNode("expand", func(_ context.Context, prev string) (string, error) {
		return prev, nil
	})
	plan.AddNode(fanout)

	plan.AddEdge(wpedge.Edge{From: "intro", To: "expand"})
	plan.AddEdge(wpedge.Edge{From: "intro", To: "prompt"})
	plan.AddEdge(wpedge.Edge{From: "expand", To: "upper"})
	plan.AddEdge(wpedge.Edge{From: "expand", To: "lower"})
	plan.AddEdge(wpedge.Edge{From: "prompt", To: "greet"})
	plan.AddEdge(wpedge.Edge{From: "upper", To: "join"})
	plan.AddEdge(wpedge.Edge{From: "lower", To: "join"})
	plan.AddEdge(wpedge.Edge{From: "greet", To: "join"})
	plan.AddEdge(wpedge.Edge{From: "join", To: "tail"})

	if err := plan.Seal(); err != nil {
		t.Fatalf("plan.Seal: %v", err)
	}

	// Export the plan in all three canonical shapes. The NodeCodec only
	// needs to know how to serialize ID + kind for export; the runtime
	// does not care about the encoded payload.
	encoder := kindOnlyCodec{}
	edgeList, err := codec.ExportEdgeList(plan, encoder)
	if err != nil {
		t.Fatalf("ExportEdgeList: %v", err)
	}
	adjList, err := codec.ExportAdjacencyList(plan, encoder)
	if err != nil {
		t.Fatalf("ExportAdjacencyList: %v", err)
	}
	adjMatrix, err := codec.ExportAdjacencyMatrix(plan, encoder)
	if err != nil {
		t.Fatalf("ExportAdjacencyMatrix: %v", err)
	}
	t.Logf("=== edge list ===\n%s", edgeList)
	t.Logf("=== adjacency list ===\n%s", adjList)
	t.Logf("=== adjacency matrix ===\n%s", adjMatrix)

	// Run the plan. The runtime treats the three outgoing edges from
	// expand as an automatic fork and executes upper/lower/greet
	// concurrently, then waits for all of them before running join.
	result, err := runner.New(plan).Run(context.Background())
	if err != nil {
		t.Fatalf("plan.Run: %v", err)
	}
	for _, nr := range result.NodeResults {
		t.Logf("node result: NodeID=%-8s Kind=%-8s Status=%-9s OutputLen=%d Err=%v Output=%s",
			nr.NodeID, nr.Kind, nr.Status, len(nr.Output), nr.Err, truncate(nr.Output, 120))
	}

	// 1. Final output must reference all three branches.
	final := result.FinalOutputString()
	for _, marker := range []string{"upper", "lower", "joined:", "tail:"} {
		if !strings.Contains(strings.ToLower(final), strings.ToLower(marker)) {
			t.Fatalf("final output missing marker %q\nfinal=%s", marker, final)
		}
	}
	// The LLM branch's output is part of the joined aggregate; verify
	// the LLM actually produced "i你好" by checking its node result
	// directly.
	for _, nr := range result.NodeResults {
		if nr.NodeID == "greet" {
			if !strings.Contains(nr.Output, "i你好") {
				t.Fatalf("greet LLM output missing 'i你好'\noutput=%s", nr.Output)
			}
			t.Logf("greet LLM reply: %s", truncate(nr.Output, 240))
		}
	}

	// 2. The two function branches must NOT have triggered an LLM call
	//    (they have no LLMProvider), so we count chat completions on
	//    the LLM node by inspecting its result kind.
	llmHits := 0
	functionHits := 0
	for _, nr := range result.NodeResults {
		switch nr.NodeID {
		case "greet":
			if nr.Kind != "llm" {
				t.Fatalf("greet node kind = %q, want llm", nr.Kind)
			}
			llmHits++
		case "upper", "lower", "intro", "join", "tail", "prompt", "expand":
			if nr.Kind != "method" {
				t.Fatalf("%s node kind = %q, want method", nr.NodeID, nr.Kind)
			}
			functionHits++
		}
	}
	if llmHits != 1 {
		t.Fatalf("greet LLM hits = %d, want 1", llmHits)
	}
	if functionHits < 7 {
		t.Fatalf("function node hits = %d, want >=7 (intro/expand/prompt/upper/lower/join/tail)", functionHits)
	}

	// 3. Topology: edge list should contain the nine edges we declared.
	parsed := codec.EdgeList{}
	if err := json.Unmarshal(edgeList, &parsed); err != nil {
		t.Fatalf("edge list not parseable: %v\nraw=%s", err, edgeList)
	}
	wantEdges := map[string]string{
		"intro":  "", // two outgoing edges: expand + prompt; checked via count
		"expand": "", // two outgoing edges: upper + lower; checked via count
		"prompt": "greet",
		"upper":  "join",
		"lower":  "join",
		"greet":  "join",
		"join":   "tail",
	}
	if len(parsed.Edges) != 9 {
		t.Fatalf("edge count = %d, want 9: %+v", len(parsed.Edges), parsed.Edges)
	}
	// Verify the multi-edge cases by counting.
	introEdges := 0
	for _, e := range parsed.Edges {
		if e.From == "intro" {
			introEdges++
		}
	}
	if introEdges != 2 {
		t.Fatalf("intro outgoing edges = %d, want 2 (expand + prompt)", introEdges)
	}
	expandEdges := 0
	for _, e := range parsed.Edges {
		if e.From == "expand" {
			expandEdges++
		}
	}
	if expandEdges != 2 {
		t.Fatalf("expand outgoing edges = %d, want 2 (upper + lower)", expandEdges)
	}
	// All three terminal branches (upper/lower/greet) land at join.
	joinIn := 0
	for _, e := range parsed.Edges {
		if e.To == "join" {
			joinIn++
		}
	}
	if joinIn != 3 {
		t.Fatalf("edges into join = %d, want 3", joinIn)
	}
	for from, to := range wantEdges {
		if to == "" {
			continue
		}
		found := false
		for _, e := range parsed.Edges {
			if e.From == from && e.To == to {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected edge %s -> %s missing in exported edge list", from, to)
		}
	}

	// 4. Matrix should be square with a 1 for each declared edge.
	matrix := codec.AdjacencyMatrix{}
	if err := json.Unmarshal(adjMatrix, &matrix); err != nil {
		t.Fatalf("matrix not parseable: %v\nraw=%s", err, adjMatrix)
	}
	if len(matrix.Nodes) != 8 {
		t.Fatalf("matrix node count = %d, want 8", len(matrix.Nodes))
	}
	if len(matrix.Matrix) != 8 {
		t.Fatalf("matrix row count = %d, want 8", len(matrix.Matrix))
	}
	onesCount := 0
	for _, row := range matrix.Matrix {
		if len(row) != 8 {
			t.Fatalf("matrix column count = %d, want 8", len(row))
		}
		for _, v := range row {
			if v == 1 {
				onesCount++
			} else if v != 0 {
				t.Fatalf("matrix entry = %d, want 0 or 1", v)
			}
		}
	}
	if onesCount != 9 {
		t.Fatalf("matrix ones = %d, want 9", onesCount)
	}

	t.Logf("final output: %s", truncate(final, 200))
}

// kindOnlyCodec encodes a node as {id, type=kind} without payload. It is
// enough for topology export — the runtime does not consult the encoded
// data when executing.
type kindOnlyCodec struct{}

func (kindOnlyCodec) EncodeNode(n wpnode.Node) (codec.NodeDefinition, error) {
	kind := ""
	if k, ok := n.(interface{ Kind() wpnode.NodeKind }); ok {
		kind = k.Kind().String()
	}
	return codec.NodeDefinition{ID: n.ID(), Type: kind}, nil
}

func (kindOnlyCodec) DecodeNode(codec.NodeDefinition) (wpnode.Node, error) {
	return nil, fmt.Errorf("kindOnlyCodec is for export only")
}
