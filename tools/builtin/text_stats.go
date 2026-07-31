package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	roottools "github.com/RedHuang-0622/Seele/tools"
	seeletypes "github.com/RedHuang-0622/Seele/types"
)

const textStatsName = "text_stats"

type textStatsArguments struct {
	Text *string `json:"text" desc:"Text to measure"`
}

type textStatsResult struct {
	Bytes int `json:"bytes"`
	Runes int `json:"runes"`
	Words int `json:"words"`
	Lines int `json:"lines"`
}

func newTextStatsEntry() roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: seeletypes.Tool{
			Type: "function",
			Function: seeletypes.ToolFunction{
				Name:        textStatsName,
				Description: "Count bytes, Unicode code points, whitespace-delimited words, and lines in text.",
				Parameters:  roottools.SchemaOf(textStatsArguments{}),
			},
		},
		Handler: roottools.HandlerFunc(func(_ context.Context, argumentsJSON string) (string, error) {
			var arguments textStatsArguments
			if err := decodeArguments(textStatsName, argumentsJSON, &arguments); err != nil {
				return "", err
			}
			if arguments.Text == nil {
				return "", invalidArgument(textStatsName, "$.text", "required field is missing")
			}
			text := *arguments.Text
			lines := 0
			if text != "" {
				lines = strings.Count(text, "\n") + 1
			}
			encoded, err := json.Marshal(textStatsResult{
				Bytes: len(text),
				Runes: utf8.RuneCountInString(text),
				Words: len(strings.Fields(text)),
				Lines: lines,
			})
			if err != nil {
				return "", fmt.Errorf("tools.builtin.%s: encode result: %w", textStatsName, err)
			}
			return string(encoded), nil
		}),
		OutputSchema: roottools.SchemaOf(textStatsResult{}),
		Metadata:     builtinMetadata(),
	}
}

func builtinMetadata() map[string]string {
	return map[string]string{
		"seele.scope":  "builtin",
		"seele.effect": "read_only",
	}
}
