package types

import "testing"

func TestValueRoundTripAndTextRendering(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	original := payload{Name: "demo", Count: 3}
	value, err := NewValue(original)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeValue[payload](value)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != original || value.Text() != `{"name":"demo","count":3}` {
		t.Fatalf("value = %s, decoded = %#v", value.RawString(), decoded)
	}
}

func TestWorkflowContextStructuredAndLegacyMirrors(t *testing.T) {
	wc := NewWorkflowContext()
	wc.SetPrevRaw(`{"ok":true}`)
	wc.SetResultRaw("node", `{"value":7}`)
	wc.SetVariableRaw("name", "Alice")

	if wc.PrevRaw() != `{"ok":true}` || wc.PrevText() != `{"ok":true}` {
		t.Fatalf("previous value mismatch: raw=%q text=%q", wc.PrevRaw(), wc.PrevText())
	}
	if wc.PrevResults["node"] != `{"value":7}` || wc.ResultText("node") != `{"value":7}` {
		t.Fatalf("result mirror mismatch: %#v", wc.PrevResults)
	}
	if wc.Vars["name"] != `"Alice"` || wc.VariableText("name") != "Alice" {
		t.Fatalf("variable mirror mismatch: %#v", wc.Vars)
	}
}
