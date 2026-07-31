// Package seeleerrors defines the structured error envelope shared by Seele
// modules. It deliberately depends only on the standard library so lower
// layers can report context without importing Agent, WorkPlan, or products.
package seeleerrors

import (
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"strings"
)

// Error is the serializable error envelope returned by framework boundaries.
// Raw is intentionally opaque: callers may store the rejected DTO, provider
// response metadata, or another JSON-friendly diagnostic value.
type Error struct {
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
	Struct   string `json:"struct,omitempty"`
	Function string `json:"function,omitempty"`
	Step     string `json:"step,omitempty"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Raw      any    `json:"raw,omitempty"`

	// Reason is retained for source compatibility with the original codec
	// error. New code should populate Message.
	Reason string `json:"-"`
	Cause  error  `json:"-"`
}

// Context describes where an error occurred in a framework operation.
type Context struct {
	Code     string
	Struct   string
	Function string
	Step     string
	Path     string
	Line     int
	Column   int
	Raw      any
}

// New creates a structured error without a wrapped cause.
func New(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Wrap turns an arbitrary error into the common envelope and attaches the
// supplied execution context. Existing envelopes are enriched in place so
// callers can add step/path information while preserving the root cause.
func Wrap(err error, context Context) error {
	if err == nil {
		return nil
	}
	var structured *Error
	if stdErrors.As(err, &structured) {
		applyContext(structured, context)
		return structured
	}
	return &Error{
		Code:     context.Code,
		Message:  err.Error(),
		Struct:   context.Struct,
		Function: context.Function,
		Step:     context.Step,
		Path:     context.Path,
		Line:     context.Line,
		Column:   context.Column,
		Raw:      context.Raw,
		Cause:    err,
	}
}

// With enriches an error with fields that are not already present.
func With(err error, context Context) error {
	return Wrap(err, context)
}

// From returns the first structured error in an error chain.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var structured *Error
	if stdErrors.As(err, &structured) {
		return structured
	}
	return nil
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	message := e.Message
	if message == "" {
		message = e.Reason
	}
	parts := make([]string, 0, 5)
	if e.Struct != "" {
		parts = append(parts, "struct="+e.Struct)
	}
	if e.Function != "" {
		parts = append(parts, "function="+e.Function)
	}
	if e.Step != "" {
		parts = append(parts, "step="+e.Step)
	}
	if e.Path != "" {
		parts = append(parts, "path="+e.Path)
	}
	if e.Line > 0 {
		parts = append(parts, fmt.Sprintf("line=%d,column=%d", e.Line, e.Column))
	}
	prefix := e.Code
	if prefix == "" {
		prefix = "seele.error"
	}
	if len(parts) == 0 {
		return prefix + ": " + message
	}
	return prefix + ": " + message + " (" + strings.Join(parts, ", ") + ")"
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// MarshalJSON keeps the compatibility Reason field visible through the
// stable structured error envelope. New callers should use Message.
func (e *Error) MarshalJSON() ([]byte, error) {
	if e == nil {
		return []byte("null"), nil
	}
	type payload struct {
		Code     string `json:"code,omitempty"`
		Message  string `json:"message"`
		Struct   string `json:"struct,omitempty"`
		Function string `json:"function,omitempty"`
		Step     string `json:"step,omitempty"`
		Path     string `json:"path,omitempty"`
		Line     int    `json:"line,omitempty"`
		Column   int    `json:"column,omitempty"`
		Raw      any    `json:"raw,omitempty"`
	}
	return json.Marshal(payload{
		Code: e.Code, Message: e.message(), Struct: e.Struct,
		Function: e.Function, Step: e.Step, Path: e.Path,
		Line: e.Line, Column: e.Column, Raw: e.Raw,
	})
}

func (e *Error) message() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

func applyContext(target *Error, context Context) {
	if target.Code == "" {
		target.Code = context.Code
	}
	if target.Struct == "" {
		target.Struct = context.Struct
	}
	if target.Function == "" {
		target.Function = context.Function
	}
	if target.Step == "" {
		target.Step = context.Step
	}
	if target.Path == "" {
		target.Path = context.Path
	}
	if target.Line == 0 {
		target.Line = context.Line
	}
	if target.Column == 0 {
		target.Column = context.Column
	}
	if target.Raw == nil {
		target.Raw = context.Raw
	}
}
