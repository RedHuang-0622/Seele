package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/RedHuang-0622/Seele/types"
)

// reasoning_effort 是跨厂商的思考强度旋钮（OpenAI/DeepSeek 用 reasoning_effort，
// Anthropic 用顶层 output_config.effort）。这里钉住两件事：
// 值非空时必须真的进请求体，值为空时必须整个字段省略（omitempty），
// 免得"配置写了却没上线"再次发生。
func TestOpenAIBuildRequestCarriesReasoningEffort(t *testing.T) {
	s := &OpenAIStrategy{}
	for _, stream := range []bool{false, true} {
		raw, err := s.BuildRequest("m", nil, nil, stream, RequestOptions{ReasoningEffort: "high"})
		if err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("stream=%v: %v", stream, err)
		}
		if body["reasoning_effort"] != "high" {
			t.Fatalf("stream=%v: reasoning_effort=%v want high (body=%s)", stream, body["reasoning_effort"], raw)
		}
	}

	raw, err := s.BuildRequest("m", nil, nil, false, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "reasoning_effort") {
		t.Fatalf("empty effort must stay off the wire, got %s", raw)
	}
}

func TestAnthropicBuildRequestCarriesEffort(t *testing.T) {
	s := &AnthropicStrategy{}
	raw, err := s.BuildRequest("m", nil, nil, false, RequestOptions{ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok || oc["effort"] != "high" {
		t.Fatalf("output_config.effort missing, got %s", raw)
	}

	raw, err = s.BuildRequest("m", nil, nil, false, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "output_config") {
		t.Fatalf("empty effort must stay off the wire, got %s", raw)
	}
}

func TestRequestOptsCarriesReasoningEffort(t *testing.T) {
	opts := requestOpts(types.LLMConfig{ReasoningEffort: "low"}, nil)
	if opts.ReasoningEffort != "low" {
		t.Fatalf("requestOpts dropped the effort: %q", opts.ReasoningEffort)
	}
}
