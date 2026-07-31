package builtin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ArgumentError identifies malformed or semantically invalid tool arguments.
// Path uses JSONPath-like notation rooted at "$". Line and Column are set for
// JSON decoding failures when the decoder exposes a byte offset.
type ArgumentError struct {
	Tool   string
	Path   string
	Line   int
	Column int
	Reason string
	Err    error
}

func (e *ArgumentError) Error() string {
	prefix := "tools.builtin." + e.Tool + ": arguments"
	if e.Path != "" {
		prefix += " " + e.Path
	}
	if e.Line > 0 && e.Column > 0 {
		prefix += fmt.Sprintf(" at line %d, column %d", e.Line, e.Column)
	}
	return prefix + ": " + e.Reason
}

func (e *ArgumentError) Unwrap() error { return e.Err }

func decodeArguments(toolName, raw string, target interface{}) error {
	if strings.TrimSpace(raw) == "" {
		return invalidArgument(toolName, "$", "expected a JSON object")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return decodingArgumentError(toolName, raw, decoder.InputOffset(), err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidArgument(toolName, "$", "multiple JSON values are not allowed")
		}
		return decodingArgumentError(toolName, raw, decoder.InputOffset(), err)
	}
	return nil
}

func decodingArgumentError(toolName, raw string, decoderOffset int64, err error) error {
	argumentError := &ArgumentError{
		Tool:   toolName,
		Path:   "$",
		Reason: "invalid JSON: " + err.Error(),
		Err:    err,
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		argumentError.Line, argumentError.Column = lineColumn(raw, syntaxError.Offset)
		return argumentError
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		argumentError.Line, argumentError.Column = lineColumn(raw, int64(len(raw)+1))
		return argumentError
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		if typeError.Field != "" {
			argumentError.Path = "$." + typeError.Field
		}
		argumentError.Line, argumentError.Column = lineColumn(raw, typeError.Offset)
		argumentError.Reason = fmt.Sprintf("expected %s, received %s", typeError.Type, typeError.Value)
		return argumentError
	}
	const unknownPrefix = "json: unknown field "
	if strings.HasPrefix(err.Error(), unknownPrefix) {
		field := strings.Trim(strings.TrimPrefix(err.Error(), unknownPrefix), `"`)
		argumentError.Path = "$." + field
		argumentError.Reason = "unknown field"
	}
	if argumentError.Line == 0 && decoderOffset > 0 {
		argumentError.Line, argumentError.Column = lineColumn(raw, decoderOffset)
	}
	return argumentError
}

func invalidArgument(toolName, path, reason string) error {
	return &ArgumentError{Tool: toolName, Path: path, Reason: reason}
}

func lineColumn(raw string, offset int64) (line, column int) {
	if offset < 1 {
		offset = 1
	}
	limit := int(offset - 1)
	if limit > len(raw) {
		limit = len(raw)
	}
	prefix := []byte(raw[:limit])
	line = bytes.Count(prefix, []byte{'\n'}) + 1
	lastNewline := bytes.LastIndexByte(prefix, '\n')
	if lastNewline < 0 {
		return line, len(prefix) + 1
	}
	return line, len(prefix) - lastNewline
}
