package event

import (
	"errors"

	seeleerrors "github.com/RedHuang-0622/Seele/errors"
)

// Failure is the safe, serializable error projection carried by an Event.
// It deliberately omits Error.Raw and Error.Cause, which may contain
// sensitive or non-serializable implementation details.
type Failure struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message"`
	Struct    string `json:"struct,omitempty"`
	Function  string `json:"function,omitempty"`
	Step      string `json:"step,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// FailureFrom converts a framework error into a log-safe event payload.
// The returned value never exposes the structured error's Raw or Cause data.
func FailureFrom(err error) *Failure {
	if err == nil {
		return nil
	}
	var structured *seeleerrors.Error
	if errors.As(err, &structured) {
		return &Failure{
			Code: structured.Code, Message: structured.Message,
			Struct: structured.Struct, Function: structured.Function,
			Step: structured.Step, Path: structured.Path,
			Line: structured.Line, Column: structured.Column,
		}
	}
	return &Failure{Code: "seele.error", Message: err.Error()}
}
