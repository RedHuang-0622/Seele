package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
)

func TestProviderExposesProductNeutralTools(t *testing.T) {
	provider := New()
	if provider.ProviderName() != ProviderName {
		t.Fatalf("ProviderName() = %q", provider.ProviderName())
	}
	entries := provider.Tools()
	if len(entries) != 3 {
		t.Fatalf("len(Tools()) = %d, want 3", len(entries))
	}
	want := map[string]bool{getTimeName: true, calculateName: true, textStatsName: true}
	for _, entry := range entries {
		name := entry.Definition.Function.Name
		if !want[name] {
			t.Fatalf("unexpected tool %q", name)
		}
		delete(want, name)
		if entry.Handler == nil || entry.Definition.Function.Parameters == nil {
			t.Fatalf("tool %q is incomplete", name)
		}
	}
}

func TestGetTimeUsesInjectedClock(t *testing.T) {
	fixed := time.Date(2026, time.July, 31, 12, 34, 56, 0, time.UTC)
	registry := registeredProvider(t, New(WithClock(ClockFunc(func() time.Time { return fixed }))))
	result := dispatch(t, registry, getTimeName, `{"timezone":"UTC"}`)
	var decoded getTimeResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.RFC3339 != "2026-07-31T12:34:56Z" || decoded.UnixSeconds != fixed.Unix() {
		t.Fatalf("result = %#v", decoded)
	}
}

func TestCalculateAndTextStats(t *testing.T) {
	registry := registeredProvider(t, New())
	operations := []struct {
		operation string
		left      int
		right     int
		want      string
	}{
		{operation: "add", left: 7, right: 35, want: `"result":42`},
		{operation: "subtract", left: 7, right: 2, want: `"result":5`},
		{operation: "multiply", left: 6, right: 7, want: `"result":42`},
		{operation: "divide", left: 21, right: 3, want: `"result":7`},
	}
	for _, operation := range operations {
		arguments := `{"operation":"` + operation.operation + `","left":` +
			jsonNumber(operation.left) + `,"right":` + jsonNumber(operation.right) + `}`
		calculated := dispatch(t, registry, calculateName, arguments)
		if !strings.Contains(calculated, operation.want) {
			t.Fatalf("calculate %s result = %s", operation.operation, calculated)
		}
	}
	stats := dispatch(t, registry, textStatsName, `{"text":"hello 世界\nnext"}`)
	var decoded textStatsResult
	if err := json.Unmarshal([]byte(stats), &decoded); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if decoded.Runes != 13 || decoded.Words != 3 || decoded.Lines != 2 {
		t.Fatalf("stats = %#v", decoded)
	}
}

func TestSemanticArgumentErrors(t *testing.T) {
	registry := registeredProvider(t, New())
	tests := []struct {
		name string
		tool string
		args string
		want string
	}{
		{name: "syntax position", tool: calculateName, args: "{\n  \"operation\":", want: "line 2, column"},
		{name: "empty payload", tool: calculateName, args: "", want: "expected a JSON object"},
		{name: "type position", tool: calculateName, args: `{"operation":"add","left":"one","right":2}`, want: "$.left at line 1"},
		{name: "multiple values", tool: calculateName, args: `{} {}`, want: "multiple JSON values are not allowed"},
		{name: "unknown field path", tool: textStatsName, args: `{"value":"x"}`, want: "$.value at line 1, column 13: unknown field"},
		{name: "missing operation", tool: calculateName, args: `{"left":1,"right":2}`, want: "$.operation: required field is missing"},
		{name: "missing field path", tool: calculateName, args: `{"operation":"add","right":1}`, want: "$.left: required field is missing"},
		{name: "invalid operation", tool: calculateName, args: `{"operation":"power","left":2,"right":3}`, want: "$.operation: unsupported value"},
		{name: "division by zero", tool: calculateName, args: `{"operation":"divide","left":1,"right":0}`, want: "$.right: division by zero"},
		{name: "invalid timezone", tool: getTimeName, args: `{"timezone":"Mars/Olympus"}`, want: "$.timezone: unknown IANA time zone"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := registry.Dispatch(context.Background(), roottools.ToolCall{Name: test.tool, ArgumentsJSON: test.args})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			var argumentError *ArgumentError
			if !errors.As(err, &argumentError) {
				t.Fatalf("error type = %T, want *ArgumentError", err)
			}
		})
	}
}

func TestGetTimeWithSystemClock(t *testing.T) {
	registry := registeredProvider(t, New())
	result := dispatch(t, registry, getTimeName, `{}`)
	if !strings.Contains(result, `"timezone":"UTC"`) {
		t.Fatalf("get_time result = %s", result)
	}
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func registeredProvider(t *testing.T, provider *Provider) *roottools.Registry {
	t.Helper()
	registry := roottools.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	return registry
}

func dispatch(t *testing.T, registry *roottools.Registry, name, arguments string) string {
	t.Helper()
	result, err := registry.Dispatch(context.Background(), roottools.ToolCall{Name: name, ArgumentsJSON: arguments})
	if err != nil {
		t.Fatalf("Dispatch(%q) error = %v", name, err)
	}
	return result
}
