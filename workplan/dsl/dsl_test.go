package dsl

import (
	"errors"
	"strings"
	"testing"
)

const validPlan = `{
  "version": 1,
  "entry": "inspect",
  "nodes": [
    {"id": "inspect", "input": "inspect scope", "kind": "auto"},
    {"id": "integrate", "input": "integrate results", "kind": "auto"}
  ],
  "edges": [{"from": "inspect", "to": "integrate"}]
}`

func TestParseValidPlan(t *testing.T) {
	p, err := Parse(validPlan)
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	if p.Version != Version || p.Entry != "inspect" || len(p.Nodes) != 2 || len(p.Edges) != 1 {
		t.Fatalf("plan = %#v", p)
	}
}

func TestParseReportsSyntaxLineAndColumn(t *testing.T) {
	_, err := Parse("{\n  \"version\": 1,\n")
	if err == nil || !strings.Contains(err.Error(), "line 2, column 16") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseReportsPreciseSemanticPaths(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{
			name: "missing task input",
			data: `{"version":1,"entry":"inspect","nodes":[{"id":"inspect","kind":"auto"}],"edges":[]}`,
			want: "$.nodes[0].input: is required",
		},
		{
			name: "unknown field",
			data: `{"version":1,"entry":"inspect","nodes":[{"id":"inspect","input":"run","kind":"auto","role":"reviewer"}],"edges":[]}`,
			want: "$.nodes[0].role: is not a supported field",
		},
		{
			name: "unreachable node",
			data: `{"version":1,"entry":"inspect","nodes":[{"id":"inspect","input":"run","kind":"auto"},{"id":"orphan","input":"run","kind":"auto"}],"edges":[]}`,
			want: "$.nodes[1].id: node \"orphan\" is unreachable from entry \"inspect\"",
		},
		{
			name: "cycle edge",
			data: `{"version":1,"entry":"a","nodes":[{"id":"a","input":"run","kind":"auto"},{"id":"b","input":"run","kind":"auto"}],"edges":[{"from":"a","to":"b"},{"from":"b","to":"a"}]}`,
			want: "$.edges[1]: creates a cycle through node \"a\"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsMalformedShapes(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"root not an object", `[]`, "$: must be an object, got array"},
		{"unknown root field", `{"version":1,"entry":"a","nodes":[],"edges":[],"name":"x"}`, "$.name: is not a supported field"},
		{"missing version", `{"entry":"a","nodes":[],"edges":[]}`, "$.version: is required"},
		{"wrong version", `{"version":2,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[]}`, "$.version: must be 1, got 2"},
		{"non-integer version", `{"version":1.5,"entry":"a","nodes":[],"edges":[]}`, "$.version: must be an integer"},
		{"version wrong type", `{"version":"1","entry":"a","nodes":[],"edges":[]}`, "$.version: must be an integer, got string"},
		{"entry wrong type", `{"version":1,"entry":7,"nodes":[],"edges":[]}`, "$.entry: must be a string, got number"},
		{"nodes not an array", `{"version":1,"entry":"a","nodes":{"a":{}},"edges":[]}`, "$.nodes: must be an array, got object"},
		{"nodes null", `{"version":1,"entry":"a","nodes":null,"edges":[]}`, "$.nodes: must be an array, got null"},
		{"edges missing", `{"version":1,"entry":"a","nodes":[]}`, "$.edges: is required"},
		{"node not an object", `{"version":1,"entry":"a","nodes":["a"],"edges":[]}`, "$.nodes[0]: must be an object, got string"},
		{"empty node list", `{"version":1,"entry":"a","nodes":[],"edges":[]}`, "$.nodes: must contain at least one node"},
		{"blank entry", `{"version":1,"entry":"  ","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[]}`, "$.entry: must be a non-empty node ID"},
		{"entry not declared", `{"version":1,"entry":"z","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[]}`, `$.entry: references node "z"`},
		{"duplicate node id", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"},{"id":"a","input":"r","kind":"auto"}],"edges":[]}`, "$.nodes[1].id: duplicates node ID"},
		{"blank node input", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"   ","kind":"auto"}],"edges":[]}`, "$.nodes[0].input: must be a non-empty task prompt"},
		{"unsupported kind", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"manual"}],"edges":[]}`, `$.nodes[0].kind: must be "auto"`},
		{"edge to undeclared node", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[{"from":"a","to":"z"}]}`, `$.edges[0].to: references undeclared node "z"`},
		{"self edge", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[{"from":"a","to":"a"}]}`, "$.edges[0]: self-edge"},
		{"duplicate edge", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"},{"id":"b","input":"r","kind":"auto"}],"edges":[{"from":"a","to":"b"},{"from":"a","to":"b"}]}`, "$.edges[1]: duplicates edge"},
		{"blank edge endpoint", `{"version":1,"entry":"a","nodes":[{"id":"a","input":"r","kind":"auto"}],"edges":[{"from":"","to":"a"}]}`, "$.edges[0].from: must be a non-empty node ID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsTrailingContentAfterDocument(t *testing.T) {
	_, err := Parse(validPlan + " trailing")
	if err == nil {
		t.Fatal("trailing content must be rejected")
	}
	var syntaxErr *SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("error = %T (%v), want *SyntaxError", err, err)
	}
}

func TestValidationErrorIsTypedAndDescribesPath(t *testing.T) {
	_, err := Parse(`{"version":1,"entry":"a","nodes":[{"id":"a","kind":"auto"}],"edges":[]}`)
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %T (%v), want *ValidationError", err, err)
	}
	if validationErr.Path != "$.nodes[0].input" || validationErr.Reason == "" {
		t.Fatalf("validation error = %#v", validationErr)
	}
}

func TestToJSONRoundTripsThroughParse(t *testing.T) {
	p, err := Parse(validPlan)
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	encoded, err := p.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error = %v", err)
	}
	restored, err := Parse(encoded)
	if err != nil {
		t.Fatalf("re-parse of ToJSON output failed: %v", err)
	}
	if restored.Entry != p.Entry || len(restored.Nodes) != len(p.Nodes) || len(restored.Edges) != len(p.Edges) {
		t.Fatalf("round-trip changed the plan: %#v", restored)
	}
	for i, n := range restored.Nodes {
		if n != p.Nodes[i] {
			t.Fatalf("node %d = %#v, want %#v", i, n, p.Nodes[i])
		}
	}
}

func TestToJSONRejectsInvalidPlan(t *testing.T) {
	// A programmatically-built plan must not serialize past validation.
	p := &Plan{Version: Version, Entry: "a", Nodes: []Node{{ID: "a", Input: "r", Kind: "auto"}, {ID: "b", Input: "r", Kind: "auto"}}}
	if _, err := p.ToJSON(); err == nil {
		t.Fatal("ToJSON must reject a plan with an unreachable node")
	}
}

func TestValidateRejectsNilPlan(t *testing.T) {
	var p *Plan
	if err := p.Validate(); err == nil {
		t.Fatal("a nil plan must not validate")
	}
}
