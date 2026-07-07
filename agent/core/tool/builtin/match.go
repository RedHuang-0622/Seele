package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/RedHuang-0622/Seele/agent/core/tool/interfaces"
	types "github.com/RedHuang-0622/Seele/types"
)

// MatchTool 提供 LLM-based 相关性匹配。
// 输入候选列表 + 查询 → LLM 判断哪些相关 → 返回置信度排序结果。
type MatchTool struct {
	llm types.ChatCompleter
}

// NewMatchTool 创建基于 LLM 的语义匹配工具。
func NewMatchTool(llm types.ChatCompleter) *MatchTool {
	return &MatchTool{llm: llm}
}

func (m *MatchTool) ProviderName() string { return "builtin" }

func (m *MatchTool) Tools() []interfaces.ToolEntry {
	return []interfaces.ToolEntry{{
		Definition: types.Tool{
			Type: "function",
			Function: types.ToolFunction{
				Name:        "semantic_match",
				Description: "基于语义理解判断候选内容与查询的相关性。返回置信度 0-1。",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{"type": "string", "description": "查询内容"},
						"candidates": map[string]interface{}{
							"type": "array",
							"items": map[string]interface{}{
								"type": "object",
								"properties": map[string]interface{}{
									"id":      map[string]interface{}{"type": "string"},
									"summary": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
					"required": []string{"query", "candidates"},
				},
			},
		},
		Handler: &matchHandler{llm: m.llm},
	}}
}

// ── matchHandler ────────────────────────────────────────────────

type matchHandler struct {
	llm types.ChatCompleter
}

// matchInput 是 semantic_match 的参数结构。
type matchInput struct {
	Query      string            `json:"query"`
	Candidates []matchCandidate `json:"candidates"`
}

type matchCandidate struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// matchResult 是 LLM 返回的每条判定结果。
type matchResult struct {
	ID          string  `json:"id"`
	Confidence  float64 `json:"confidence"`
	Reason      string  `json:"reason"`
}

func (h *matchHandler) Execute(ctx context.Context, argsJSON string) (string, error) {
	var input matchInput
	if err := json.Unmarshal([]byte(argsJSON), &input); err != nil {
		return "", fmt.Errorf("matchHandler: invalid args: %w", err)
	}

	if input.Query == "" || len(input.Candidates) == 0 {
		return "[]", nil
	}

	// 构造 prompt
	var sb strings.Builder
	sb.WriteString("You are a relevance judge. Given the query and candidates, determine which candidates are relevant.\n")
	fmt.Fprintf(&sb, "Query: %s\n\nCandidates:\n", input.Query)
	for _, c := range input.Candidates {
		fmt.Fprintf(&sb, "[%s] %s\n", c.ID, c.Summary)
	}
	sb.WriteString("\nReturn JSON array: [{id: string, confidence: number (0-1), reason: string}]\nOnly include confidence > 0.3.")

	messages := []types.Message{
		{Role: "user", Content: strPtr(sb.String())},
	}

	resp, err := h.llm.Complete(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("matchHandler: LLM call failed: %w", err)
	}

	if resp.Content == nil {
		return "[]", nil
	}

	// 清理可能的 markdown 围栏
	raw := strings.TrimSpace(*resp.Content)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)

	var results []matchResult
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return "", fmt.Errorf("matchHandler: failed to parse LLM response: %w\nraw: %s", err, raw)
	}

	out, err := json.Marshal(results)
	if err != nil {
		return "", fmt.Errorf("matchHandler: failed to marshal results: %w", err)
	}
	return string(out), nil
}

// strPtr 返回 *string。
func strPtr(s string) *string { return &s }
