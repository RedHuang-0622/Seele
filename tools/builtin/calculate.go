package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	roottools "github.com/RedHuang-0622/Seele/tools"
	seeletypes "github.com/RedHuang-0622/Seele/types"
)

const calculateName = "calculate"

type calculateArguments struct {
	Operation string   `json:"operation" desc:"Arithmetic operation" enum:"add,subtract,multiply,divide"`
	Left      *float64 `json:"left" desc:"Left operand"`
	Right     *float64 `json:"right" desc:"Right operand"`
}

type calculateResult struct {
	Operation string  `json:"operation"`
	Left      float64 `json:"left"`
	Right     float64 `json:"right"`
	Result    float64 `json:"result"`
}

func newCalculateEntry() roottools.ToolEntry {
	return roottools.ToolEntry{
		Definition: seeletypes.Tool{
			Type: "function",
			Function: seeletypes.ToolFunction{
				Name:        calculateName,
				Description: "Perform one basic arithmetic operation without evaluating code or expressions.",
				Parameters:  roottools.SchemaOf(calculateArguments{}),
			},
		},
		Handler: roottools.HandlerFunc(func(_ context.Context, argumentsJSON string) (string, error) {
			var arguments calculateArguments
			if err := decodeArguments(calculateName, argumentsJSON, &arguments); err != nil {
				return "", err
			}
			operation := strings.ToLower(strings.TrimSpace(arguments.Operation))
			if operation == "" {
				return "", invalidArgument(calculateName, "$.operation", "required field is missing")
			}
			if arguments.Left == nil {
				return "", invalidArgument(calculateName, "$.left", "required field is missing")
			}
			if arguments.Right == nil {
				return "", invalidArgument(calculateName, "$.right", "required field is missing")
			}
			result, err := calculate(operation, *arguments.Left, *arguments.Right)
			if err != nil {
				return "", err
			}
			encoded, err := json.Marshal(calculateResult{
				Operation: operation,
				Left:      *arguments.Left,
				Right:     *arguments.Right,
				Result:    result,
			})
			if err != nil {
				return "", fmt.Errorf("tools.builtin.%s: encode result: %w", calculateName, err)
			}
			return string(encoded), nil
		}),
		OutputSchema: roottools.SchemaOf(calculateResult{}),
		Metadata:     builtinMetadata(),
	}
}

func calculate(operation string, left, right float64) (float64, error) {
	if math.IsNaN(left) || math.IsInf(left, 0) {
		return 0, invalidArgument(calculateName, "$.left", "must be a finite number")
	}
	if math.IsNaN(right) || math.IsInf(right, 0) {
		return 0, invalidArgument(calculateName, "$.right", "must be a finite number")
	}
	var result float64
	switch operation {
	case "add":
		result = left + right
	case "subtract":
		result = left - right
	case "multiply":
		result = left * right
	case "divide":
		if right == 0 {
			return 0, invalidArgument(calculateName, "$.right", "division by zero is not allowed")
		}
		result = left / right
	default:
		return 0, invalidArgument(calculateName, "$.operation", fmt.Sprintf("unsupported value %q; expected add, subtract, multiply, or divide", operation))
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, invalidArgument(calculateName, "$", "operation result is not a finite number")
	}
	return result, nil
}
