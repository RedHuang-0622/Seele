package seeleerrors

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWrapIncludesStructuredContextAndJSONFields(t *testing.T) {
	root := errors.New("invalid node")
	err := Wrap(root, Context{
		Code:   "workplan.codec.node",
		Struct: "NodeSpec[string]", Function: "DecodeNode", Step: "nodes[0]",
		Path: "$.nodes[0].input", Raw: map[string]string{"input": ""},
	})
	structured := From(err)
	if structured == nil || structured.Cause != root {
		t.Fatalf("structured = %#v", structured)
	}
	for _, part := range []string{"workplan.codec.node", "NodeSpec[string]", "DecodeNode", "nodes[0]", "$.nodes[0].input"} {
		if !strings.Contains(err.Error(), part) {
			t.Fatalf("error %q does not contain %q", err, part)
		}
	}
	encoded, marshalErr := json.Marshal(structured)
	if marshalErr != nil || !strings.Contains(string(encoded), `"raw"`) {
		t.Fatalf("JSON error = %v, payload = %s", marshalErr, encoded)
	}
}

func TestWrapEnrichesExistingErrorWithoutReplacingMessage(t *testing.T) {
	existing := New("first", "bad input")
	enriched := Wrap(existing, Context{Step: "validate", Path: "$.entry"})
	if enriched != existing || existing.Message != "bad input" || existing.Step != "validate" {
		t.Fatalf("enriched = %#v", existing)
	}
}
