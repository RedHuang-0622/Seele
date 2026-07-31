package types

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// --- NewWorkflowContext ---

func TestNewWorkflowContext(t *testing.T) {
	wc := NewWorkflowContext()
	if wc == nil {
		t.Fatal("NewWorkflowContext() returned nil")
	}
	if wc.Vars == nil {
		t.Error("Vars map should be initialized (non-nil)")
	}
	if wc.Result == nil {
		t.Error("Result should be initialized (non-nil)")
	}
	if wc.Result.Checkpoints == nil {
		t.Error("Result.Checkpoints map should be initialized (non-nil)")
	}
	if wc.Metadata == nil {
		t.Error("Metadata map should be initialized (non-nil)")
	}
	if wc.PrevOutput != "" {
		t.Errorf("PrevOutput should be empty string, got %q", wc.PrevOutput)
	}
	if wc.Result.Aborted {
		t.Error("Result.Aborted should default to false")
	}
}

func TestWorkflowContextCloneIsIndependent(t *testing.T) {
	source := NewWorkflowContext()
	source.PrevOutput = `"parent"`
	source.PrevResults["parent"] = `"result"`
	source.Vars["scope"] = `"parent"`
	source.Result.NodeResults = []*NodeResult{{NodeBase: NodeBase{NodeID: "parent", Output: `"result"`}}}
	source.Result.Vars = map[string]string{"scope": `"parent"`}
	source.Result.Checkpoints["parent"] = `"checkpoint"`
	source.Metadata["nested"] = map[string]any{
		"items": []any{map[string]string{"key": "parent"}},
	}

	clone := source.Clone()
	if clone == source {
		t.Fatal("Clone() returned the source context")
	}

	clone.PrevOutput = `"branch"`
	clone.PrevResults["parent"] = `"branch-result"`
	clone.Vars["scope"] = `"branch"`
	clone.Result.NodeResults[0].Output = `"branch-result"`
	clone.Result.Vars["scope"] = `"branch"`
	clone.Result.Checkpoints["parent"] = `"branch-checkpoint"`
	nested := clone.Metadata["nested"].(map[string]any)
	nested["items"].([]any)[0].(map[string]string)["key"] = "branch"

	if source.PrevOutput != `"parent"` {
		t.Errorf("source PrevOutput = %q, want parent value", source.PrevOutput)
	}
	if source.PrevResults["parent"] != `"result"` {
		t.Errorf("source PrevResults[parent] = %q, want parent result", source.PrevResults["parent"])
	}
	if source.Vars["scope"] != `"parent"` {
		t.Errorf("source Vars[scope] = %q, want parent value", source.Vars["scope"])
	}
	if source.Result.NodeResults[0].Output != `"result"` {
		t.Errorf("source node output = %q, want parent result", source.Result.NodeResults[0].Output)
	}
	if source.Result.Vars["scope"] != `"parent"` {
		t.Errorf("source result vars = %q, want parent value", source.Result.Vars["scope"])
	}
	if source.Result.Checkpoints["parent"] != `"checkpoint"` {
		t.Errorf("source checkpoint = %q, want parent value", source.Result.Checkpoints["parent"])
	}
	originalNested := source.Metadata["nested"].(map[string]any)
	if originalNested["items"].([]any)[0].(map[string]string)["key"] != "parent" {
		t.Error("source nested metadata was mutated through clone")
	}
}

func TestWorkflowContextCloneCopiesTypedMetadataValues(t *testing.T) {
	type metadataPayload struct {
		Values []int
		Labels map[string]string
	}

	source := NewWorkflowContext()
	source.Metadata["typed"] = []map[string]any{{"values": []int{1, 2}}}
	source.Metadata["payload"] = &metadataPayload{
		Values: []int{3, 4},
		Labels: map[string]string{"scope": "parent"},
	}

	clone := source.Clone()
	clone.Metadata["typed"].([]map[string]any)[0]["values"].([]int)[0] = 99
	payload := clone.Metadata["payload"].(*metadataPayload)
	payload.Values[0] = 88
	payload.Labels["scope"] = "branch"

	typed := source.Metadata["typed"].([]map[string]any)
	if typed[0]["values"].([]int)[0] != 1 {
		t.Error("source typed metadata was mutated through clone")
	}
	originalPayload := source.Metadata["payload"].(*metadataPayload)
	if originalPayload.Values[0] != 3 || originalPayload.Labels["scope"] != "parent" {
		t.Error("source pointer metadata was mutated through clone")
	}
}

// --- WorkPlanResult FinalOutput / FinalOutputString ---

func TestFinalOutput(t *testing.T) {
	tests := []struct {
		name   string
		result *WorkPlanResult
		want   string
	}{
		{
			name: "last non-skipped output",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "first", Skipped: false}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "second", Skipped: false}, Err: nil},
			}},
			want: "second",
		},
		{
			name: "skip aborted result",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "should not see", Skipped: false, Aborted: true}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "valid", Skipped: false, Aborted: false}, Err: nil},
			}},
			want: "valid",
		},
		{
			name: "skip errored result",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "good", Skipped: false}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "bad", Skipped: false}, Err: errors.New("exec error")},
			}},
			want: "good",
		},
		{
			name: "skip empty output",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "", Skipped: false}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "filled", Skipped: false}, Err: nil},
			}},
			want: "filled",
		},
		{
			name: "skip skipped result",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "skipped", Skipped: true}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "real", Skipped: false}, Err: nil},
			}},
			want: "real",
		},
		{
			name:   "no results returns empty JSON string",
			result: &WorkPlanResult{},
			want:   `""`,
		},
		{
			name: "all skipped returns empty JSON string",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: "a", Skipped: true}, Err: nil},
				{NodeBase: NodeBase{NodeID: "n2", Output: "b", Skipped: true}, Err: nil},
			}},
			want: `""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.FinalOutput()
			if got != tt.want {
				t.Errorf("FinalOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFinalOutputString(t *testing.T) {
	tests := []struct {
		name   string
		result *WorkPlanResult
		want   string
	}{
		{
			name: "unwraps JSON string",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: `"hello world"`, Skipped: false}, Err: nil},
			}},
			want: "hello world",
		},
		{
			name: "plain text returned as-is",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: `plaintext`, Skipped: false}, Err: nil},
			}},
			want: "plaintext",
		},
		{
			name: "JSON object returned as-is",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: `{"a":1}`, Skipped: false}, Err: nil},
			}},
			want: `{"a":1}`,
		},
		{
			name: "empty output",
			result: &WorkPlanResult{NodeResults: []*NodeResult{
				{NodeBase: NodeBase{NodeID: "n1", Output: `""`, Skipped: false}, Err: nil},
			}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.FinalOutputString()
			if got != tt.want {
				t.Errorf("FinalOutputString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- ToJSON / FromJSON ---

func TestToJSON(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"key":"value"}`, `{"key":"value"}`}, // already valid JSON
		{`"hello"`, `"hello"`},                 // valid JSON string
		{`42`, `42`},                           // valid JSON number
		{`true`, `true`},                       // valid JSON bool
		{`hello`, `"hello"`},                   // plain string → JSON quoted
		{`plain text`, `"plain text"`},         // plain with space → JSON quoted
		{`123`, `123`},                         // valid JSON number → pass through
		{``, `""`},                             // empty → JSON empty string
		{`{"nested": {"a": [1,2]}}`, `{"nested": {"a": [1,2]}}`}, // nested JSON
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ToJSON(tt.input)
			if got != tt.want {
				t.Errorf("ToJSON(%q) = %s, want %s", tt.input, got, tt.want)
			}
			if !json.Valid([]byte(got)) {
				t.Errorf("ToJSON(%q) produced invalid JSON: %s", tt.input, got)
			}
		})
	}
}

func TestFromJSON(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"hello"`, `hello`},             // JSON string → unwrapped
		{`"hello world"`, `hello world`}, // JSON string with space → unwrapped
		{`plaintext`, `plaintext`},       // not JSON → as-is
		{`{"a":1}`, `{"a":1}`},           // valid JSON but not a string → as-is
		{``, ``},                         // empty → as-is
		{`42`, `42`},                     // JSON number → as-is (not a string)
		{`true`, `true`},                 // JSON bool → as-is
		{`"\"escaped\""`, `"escaped"`},   // JSON with escaped quotes
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FromJSON(tt.input)
			if got != tt.want {
				t.Errorf("FromJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestToJSONFromJSONRoundTrip(t *testing.T) {
	inputs := []string{
		"hello world",
		"plain text with spaces",
		`{"a": 1, "b": "two"}`,
		"",
		"123",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			jsonStr := ToJSON(input)
			back := FromJSON(jsonStr)
			if input == "" {
				// ToJSON("") → `""`, FromJSON(`""`) → ""
				if back != "" {
					t.Errorf("round-trip of empty: got %q, want %q", back, "")
				}
			} else if json.Valid([]byte(input)) {
				// Already valid JSON → ToJSON is identity, FromJSON may unwrap if it's a string
				if back != input {
					t.Errorf("round-trip of valid JSON: got %q, want %q", back, input)
				}
			} else {
				// Plain text: ToJSON wraps in quotes, FromJSON unwraps back
				if back != input {
					t.Errorf("round-trip: got %q, want %q", back, input)
				}
			}
		})
	}
}

// --- RenderTemplate ---

func TestRenderTemplate(t *testing.T) {
	t.Run("replaces {{.PrevResult}}", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `"hello world"`
		result := RenderTemplate("prev: {{.PrevResult}}", wc)
		if result != "prev: hello world" {
			t.Errorf("got %q, want %q", result, "prev: hello world")
		}
	})

	t.Run("replaces {{.Vars.key}}", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.Vars["name"] = `"Alice"`
		wc.Vars["count"] = `"42"`
		result := RenderTemplate("{{.Vars.name}} has {{.Vars.count}} items", wc)
		if result != "Alice has 42 items" {
			t.Errorf("got %q, want %q", result, "Alice has 42 items")
		}
	})

	t.Run("replaces multiple occurrences", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `"echo"`
		result := RenderTemplate("{{.PrevResult}} {{.PrevResult}} {{.PrevResult}}", wc)
		if result != "echo echo echo" {
			t.Errorf("got %q, want %q", result, "echo echo echo")
		}
	})

	t.Run("combined replacements", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `"result_123"`
		wc.Vars["user"] = `"Bob"`
		result := RenderTemplate("{{.Vars.user}} got {{.PrevResult}}", wc)
		if result != "Bob got result_123" {
			t.Errorf("got %q, want %q", result, "Bob got result_123")
		}
	})

	t.Run("nil context returns template as-is", func(t *testing.T) {
		result := RenderTemplate("hello {{.PrevResult}}", nil)
		if result != "hello {{.PrevResult}}" {
			t.Errorf("got %q, want %q", result, "hello {{.PrevResult}}")
		}
	})

	t.Run("no template variables", func(t *testing.T) {
		wc := NewWorkflowContext()
		result := RenderTemplate("just plain text", wc)
		if result != "just plain text" {
			t.Errorf("got %q, want %q", result, "just plain text")
		}
	})

	t.Run("missing variable replaced with empty string", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `""`
		result := RenderTemplate("{{.PrevResult}}", wc)
		if result != "" {
			t.Errorf("got %q, want %q", result, "")
		}
	})

	t.Run("template with {{.PrevResult}} and plain text from JSON", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `"{\"key\": \"value\"}"`
		result := RenderTemplate("data: {{.PrevResult}}", wc)
		if result != `data: {"key": "value"}` {
			t.Errorf("got %q", result)
		}
	})
	t.Run("replaces {{.PrevResults.nodeID}}", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevResults["b"] = `"布偶猫"`
		wc.PrevResults["c"] = `"温顺"`
		result := RenderTemplate("外形{{.PrevResults.b}}，习性{{.PrevResults.c}}", wc)
		if result != "外形布偶猫，习性温顺" {
			t.Errorf("got %q, want %q", result, "外形布偶猫，习性温顺")
		}
	})

	t.Run("{{.PrevResults}} missing key leaves placeholder", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevResults["b"] = `"存在"`
		result := RenderTemplate("{{.PrevResults.b}} {{.PrevResults.nonexistent}}", wc)
		if result != "存在 {{.PrevResults.nonexistent}}" {
			t.Errorf("got %q", result)
		}
	})

	t.Run("combined PrevResult and PrevResults", func(t *testing.T) {
		wc := NewWorkflowContext()
		wc.PrevOutput = `"最后"`
		wc.PrevResults["a"] = `"第一"`
		wc.PrevResults["b"] = `"第二"`
		result := RenderTemplate("{{.PrevResult}} after {{.PrevResults.a}} and {{.PrevResults.b}}", wc)
		if result != "最后 after 第一 and 第二" {
			t.Errorf("got %q", result)
		}
	})
}

// --- Status String ---

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusPending, "pending"},
		{StatusRunning, "running"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
		{StatusAborted, "aborted"},
		{Status(99), "unknown(99)"},
		{Status(-1), "unknown(-1)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.status.String()
			if got != tt.want {
				t.Errorf("Status(%d).String() = %q, want %q", int(tt.status), got, tt.want)
			}
		})
	}
}

// --- ConditionRegistry ---

func TestConditionRegistry(t *testing.T) {
	t.Run("new registry is empty", func(t *testing.T) {
		r := NewConditionRegistry()
		if r == nil {
			t.Fatal("NewConditionRegistry() returned nil")
		}
		_, ok := r.Resolve("anything")
		if ok {
			t.Error("expected Resolve to return false for empty registry")
		}
	})

	t.Run("register and resolve", func(t *testing.T) {
		r := NewConditionRegistry()
		called := false
		cond := EdgeCondition(func(_ *WorkflowContext) bool {
			called = true
			return true
		})
		r.Register("my_cond", cond)

		got, ok := r.Resolve("my_cond")
		if !ok {
			t.Fatal("expected Resolve('my_cond') to be ok")
		}
		if got == nil {
			t.Fatal("condition should not be nil")
		}

		result := got(nil)
		if !result {
			t.Error("expected condition to return true")
		}
		if !called {
			t.Error("condition function was not called")
		}
	})

	t.Run("unknown key returns false", func(t *testing.T) {
		r := NewConditionRegistry()
		r.Register("exists", func(_ *WorkflowContext) bool { return false })

		_, ok := r.Resolve("nonexistent")
		if ok {
			t.Error("expected Resolve('nonexistent') to be false")
		}
	})

	t.Run("multiple registrations", func(t *testing.T) {
		r := NewConditionRegistry()
		r.Register("a", func(_ *WorkflowContext) bool { return true })
		r.Register("b", func(_ *WorkflowContext) bool { return false })

		condA, okA := r.Resolve("a")
		condB, okB := r.Resolve("b")

		if !okA || condA == nil || !condA(nil) {
			t.Error("condition 'a' should resolve to a true-returning function")
		}
		if !okB || condB == nil || condB(nil) {
			t.Error("condition 'b' should resolve to a false-returning function")
		}
	})

	t.Run("overwrite existing key", func(t *testing.T) {
		r := NewConditionRegistry()
		r.Register("key", func(_ *WorkflowContext) bool { return false })
		r.Register("key", func(_ *WorkflowContext) bool { return true })

		cond, ok := r.Resolve("key")
		if !ok {
			t.Fatal("expected Resolve('key') to be ok")
		}
		if !cond(nil) {
			t.Error("condition should return true after overwrite")
		}
	})
}

// --- Snapshot ---

func TestSnapshot(t *testing.T) {
	wc := NewWorkflowContext()
	wc.PrevOutput = `"data"`
	wc.Vars["key"] = `"value"`

	now := time.Now()
	snap := Snapshot{
		NodeID:    "node_42",
		Context:   wc,
		Timestamp: now,
		Status:    StatusRunning,
	}

	if snap.NodeID != "node_42" {
		t.Errorf("NodeID = %q, want %q", snap.NodeID, "node_42")
	}
	if snap.Context != wc {
		t.Error("Context should be the same pointer")
	}
	if snap.Context.PrevOutput != `"data"` {
		t.Errorf("Context.PrevOutput = %q, want %q", snap.Context.PrevOutput, `"data"`)
	}
	if snap.Context.Vars["key"] != `"value"` {
		t.Errorf("Context.Vars[key] = %q, want %q", snap.Context.Vars["key"], `"value"`)
	}
	if snap.Status != StatusRunning {
		t.Errorf("Status = %v, want StatusRunning", snap.Status)
	}
	if snap.Timestamp != now {
		t.Errorf("Timestamp mismatch")
	}
	if snap.Timestamp.IsZero() {
		t.Error("Timestamp should not be zero")
	}
}

// --- WorkPlanResult helper fields ---

func TestWorkPlanResultDefaults(t *testing.T) {
	r := &WorkPlanResult{}
	if r.TotalElapsed != 0 {
		t.Errorf("TotalElapsed should be 0, got %v", r.TotalElapsed)
	}
	if r.Aborted {
		t.Error("Aborted should default to false")
	}
	if r.AbortReason != "" {
		t.Errorf("AbortReason should be empty, got %q", r.AbortReason)
	}
	if r.Vars != nil {
		t.Error("Vars should be nil by default")
	}
	if r.NodeResults != nil {
		t.Error("NodeResults should be nil by default")
	}
}

func TestNodeResultDefaults(t *testing.T) {
	nr := &NodeResult{}
	if nr.NodeID != "" {
		t.Errorf("NodeID should be empty, got %q", nr.NodeID)
	}
	if nr.Kind != "" {
		t.Errorf("Kind should be empty, got %q", nr.Kind)
	}
	if nr.Skipped {
		t.Error("Skipped should default to false")
	}
	if nr.Aborted {
		t.Error("Aborted should default to false")
	}
	if nr.Err != nil {
		t.Errorf("Err should be nil, got %v", nr.Err)
	}
	if !nr.StartedAt.IsZero() {
		t.Error("StartedAt should be zero by default")
	}
	if !nr.EndedAt.IsZero() {
		t.Error("EndedAt should be zero by default")
	}
}
