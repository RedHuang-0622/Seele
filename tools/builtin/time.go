package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	roottools "github.com/RedHuang-0622/Seele/tools"
	seeletypes "github.com/RedHuang-0622/Seele/types"
)

const getTimeName = "get_time"

type getTimeArguments struct {
	Timezone string `json:"timezone,omitempty" desc:"IANA time zone such as UTC or Asia/Shanghai; defaults to UTC"`
}

type getTimeResult struct {
	RFC3339     string `json:"rfc3339"`
	UnixSeconds int64  `json:"unix_seconds"`
	Timezone    string `json:"timezone"`
}

func newGetTimeEntry(clock Clock) roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: seeletypes.Tool{
			Type: "function",
			Function: seeletypes.ToolFunction{
				Name:        getTimeName,
				Description: "Return the current time in a requested IANA time zone. Defaults to UTC.",
				Parameters:  roottools.SchemaOf(getTimeArguments{}),
			},
		},
		Handler: roottools.HandlerFunc(func(_ context.Context, argumentsJSON string) (string, error) {
			var arguments getTimeArguments
			if err := decodeArguments(getTimeName, argumentsJSON, &arguments); err != nil {
				return "", err
			}
			zoneName := strings.TrimSpace(arguments.Timezone)
			if zoneName == "" {
				zoneName = "UTC"
			}
			location, err := time.LoadLocation(zoneName)
			if err != nil {
				return "", invalidArgument(getTimeName, "$.timezone", fmt.Sprintf("unknown IANA time zone %q", zoneName))
			}
			now := clock.Now().In(location)
			encoded, err := json.Marshal(getTimeResult{
				RFC3339:     now.Format(time.RFC3339),
				UnixSeconds: now.Unix(),
				Timezone:    zoneName,
			})
			if err != nil {
				return "", fmt.Errorf("tools.builtin.%s: encode result: %w", getTimeName, err)
			}
			return string(encoded), nil
		}),
		OutputSchema: roottools.SchemaOf(getTimeResult{}),
		Metadata:     builtinMetadata(),
	}
}
