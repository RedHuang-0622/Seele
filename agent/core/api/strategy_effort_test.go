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

// TestChatClientReasoningEffortIsOverridable 钉住"运行期改强度真的生效"：
// 构造期值只是初值，SetReasoningEffort 之后 requestOpts 必须用新值——这是
// `session` 档位（跟随会话 effort）的唯一落点，写歪了主角色就永远是空值不下发。
func TestChatClientReasoningEffortIsOverridable(t *testing.T) {
	client := NewChatClient(types.LLMConfig{ReasoningEffort: "low"})
	if got := client.ReasoningEffort(); got != "low" {
		t.Fatalf("constructor value must be the initial effort, got %q", got)
	}
	if got := client.requestOpts(nil).ReasoningEffort; got != "low" {
		t.Fatalf("requestOpts must see the constructor value, got %q", got)
	}

	client.SetReasoningEffort("high")
	if got := client.requestOpts(nil).ReasoningEffort; got != "high" {
		t.Fatalf("requestOpts must see the overridden effort, got %q", got)
	}

	// 空串 = 不下发（provider 走自己的默认），必须能把一个已设的值清掉。
	client.SetReasoningEffort("")
	if got := client.requestOpts(nil).ReasoningEffort; got != "" {
		t.Fatalf("empty effort must clear the wire value, got %q", got)
	}
}
