// Package types provides pure data types shared by all workplan layers.
// This package has zero dependencies on other workplan packages.
package types

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Value is the JSON-backed transport unit used by WorkPlan internals. It
// avoids forcing every node boundary through an untyped string while keeping
// snapshots and product adapters language-neutral.
type Value struct {
	Raw json.RawMessage `json:"raw,omitempty"`
}

// NewValue encodes a typed value into the transport representation.
func NewValue[T any](value T) (Value, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return Value{}, fmt.Errorf("encode workflow value: %w", err)
	}
	return Value{Raw: raw}, nil
}

// RawValue creates a Value from an existing JSON payload or a plain string.
func RawValue(value string) Value {
	if json.Valid([]byte(value)) {
		return Value{Raw: json.RawMessage(value)}
	}
	raw, _ := json.Marshal(value)
	return Value{Raw: raw}
}

// DecodeValue decodes a transport value into a caller-selected Go type.
func DecodeValue[T any](value Value) (T, error) {
	var decoded T
	if len(value.Raw) == 0 {
		return decoded, nil
	}
	if err := json.Unmarshal(value.Raw, &decoded); err != nil {
		return decoded, fmt.Errorf("decode workflow value: %w", err)
	}
	return decoded, nil
}

// RawString returns the JSON representation used for persistence and legacy
// string fields.
func (v Value) RawString() string { return string(v.Raw) }

// Text returns a readable value for prompt/template rendering. JSON strings
// are unquoted; objects and arrays remain JSON.
func (v Value) Text() string {
	if len(v.Raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(v.Raw, &text) == nil {
		return text
	}
	return string(v.Raw)
}

// WorkflowContext carries shared state during graph execution.
// Named WorkflowContext to avoid collision with stdlib context.Context.
type WorkflowContext struct {
	Prev        Value             `json:"prev,omitempty"`
	Results     map[string]Value  `json:"results,omitempty"`
	Variables   map[string]Value  `json:"variables,omitempty"`
	PrevOutput  string            // JSON output from the immediately previous node
	PrevResults map[string]string // nodeID → output for all executed nodes (multi-upstream reference)
	Vars        map[string]string // Named variables written by Emit nodes
	Result      *WorkPlanResult   // Accumulated execution result
	Metadata    map[string]any    // Extension fields
}

// NewWorkflowContext creates an empty workflow context.
func NewWorkflowContext() *WorkflowContext {
	return &WorkflowContext{
		Results:     make(map[string]Value),
		Variables:   make(map[string]Value),
		PrevResults: make(map[string]string),
		Vars:        make(map[string]string),
		Result:      &WorkPlanResult{Checkpoints: make(map[string]string)},
		Metadata:    make(map[string]any),
	}
}

// Clone returns an independent copy of the workflow context. It is used at
// fork boundaries so branch-local writes cannot mutate the parent context or
// sibling branches.
func (wc *WorkflowContext) Clone() *WorkflowContext {
	if wc == nil {
		return nil
	}

	return &WorkflowContext{
		Prev:        cloneValue(wc.Prev),
		Results:     cloneValues(wc.Results),
		Variables:   cloneValues(wc.Variables),
		PrevOutput:  wc.PrevOutput,
		PrevResults: cloneStringMap(wc.PrevResults),
		Vars:        cloneStringMap(wc.Vars),
		Result:      cloneWorkPlanResult(wc.Result),
		Metadata:    cloneMetadata(wc.Metadata),
	}
}

// SetPrevValue updates the structured previous output and its legacy mirror.
func (wc *WorkflowContext) SetPrevValue(value Value) {
	if wc == nil {
		return
	}
	wc.Prev = cloneValue(value)
	wc.PrevOutput = value.RawString()
}

// SetPrevRaw stores a JSON or plain-text node output.
func (wc *WorkflowContext) SetPrevRaw(output string) { wc.SetPrevValue(RawValue(output)) }

// PrevRaw returns the transport representation of the previous output. It is
// intended for the legacy Node.Run contract; prompt-facing code should use
// PrevText instead.
func (wc *WorkflowContext) PrevRaw() string {
	if wc == nil {
		return ""
	}
	if len(wc.Prev.Raw) > 0 {
		return wc.Prev.RawString()
	}
	return wc.PrevOutput
}

// SetResultRaw records a node result in both structured and compatibility maps.
func (wc *WorkflowContext) SetResultRaw(nodeID, output string) {
	if wc == nil {
		return
	}
	if wc.Results == nil {
		wc.Results = make(map[string]Value)
	}
	value := RawValue(output)
	wc.Results[nodeID] = value
	if wc.PrevResults == nil {
		wc.PrevResults = make(map[string]string)
	}
	wc.PrevResults[nodeID] = value.RawString()
}

// SetVariableRaw records a named variable in both structured and compatibility maps.
func (wc *WorkflowContext) SetVariableRaw(key, value string) {
	if wc == nil {
		return
	}
	if wc.Variables == nil {
		wc.Variables = make(map[string]Value)
	}
	transport := RawValue(value)
	wc.Variables[key] = transport
	if wc.Vars == nil {
		wc.Vars = make(map[string]string)
	}
	wc.Vars[key] = transport.RawString()
}

// PrevText returns the structured previous output and falls back to the
// legacy field for contexts created by older callers.
func (wc *WorkflowContext) PrevText() string {
	if wc == nil {
		return ""
	}
	if len(wc.Prev.Raw) > 0 {
		return wc.Prev.Text()
	}
	return FromJSON(wc.PrevOutput)
}

// ResultText returns a node result in prompt-friendly form.
func (wc *WorkflowContext) ResultText(nodeID string) string {
	if wc == nil {
		return ""
	}
	if value, ok := wc.Results[nodeID]; ok {
		return value.Text()
	}
	return FromJSON(wc.PrevResults[nodeID])
}

// VariableText returns a named variable in prompt-friendly form.
func (wc *WorkflowContext) VariableText(key string) string {
	if wc == nil {
		return ""
	}
	if value, ok := wc.Variables[key]; ok {
		return value.Text()
	}
	return FromJSON(wc.Vars[key])
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}

	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneValue(source Value) Value {
	if source.Raw == nil {
		return Value{}
	}
	return Value{Raw: append(json.RawMessage(nil), source.Raw...)}
}

func cloneValues(source map[string]Value) map[string]Value {
	if source == nil {
		return nil
	}
	clone := make(map[string]Value, len(source))
	for key, value := range source {
		clone[key] = cloneValue(value)
	}
	return clone
}

func cloneWorkPlanResult(source *WorkPlanResult) *WorkPlanResult {
	if source == nil {
		return nil
	}

	clone := &WorkPlanResult{
		Vars:         cloneStringMap(source.Vars),
		Checkpoints:  cloneStringMap(source.Checkpoints),
		Aborted:      source.Aborted,
		AbortReason:  source.AbortReason,
		TotalElapsed: source.TotalElapsed,
	}
	if source.NodeResults == nil {
		return clone
	}

	clone.NodeResults = make([]*NodeResult, len(source.NodeResults))
	for index, result := range source.NodeResults {
		if result == nil {
			continue
		}
		resultClone := *result
		if result.Value != nil {
			value := cloneValue(*result.Value)
			resultClone.Value = &value
		}
		clone.NodeResults[index] = &resultClone
	}
	return clone
}

func cloneMetadata(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneMetadataValue(value)
	}
	return clone
}

func cloneMetadataValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneMetadataReflect(reflect.ValueOf(value)).Interface()
}

func cloneMetadataReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type()).Elem()
		clone.Set(cloneMetadataReflect(value.Elem()))
		return clone
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.New(value.Type().Elem())
		clone.Elem().Set(cloneMetadataReflect(value.Elem()))
		return clone
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			clone.SetMapIndex(iterator.Key(), cloneMetadataReflect(iterator.Value()))
		}
		return clone
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			clone.Index(index).Set(cloneMetadataReflect(value.Index(index)))
		}
		return clone
	case reflect.Array:
		clone := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			clone.Index(index).Set(cloneMetadataReflect(value.Index(index)))
		}
		return clone
	case reflect.Struct:
		clone := reflect.New(value.Type()).Elem()
		clone.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).PkgPath == "" {
				clone.Field(index).Set(cloneMetadataReflect(value.Field(index)))
			}
		}
		return clone
	default:
		return value
	}
}

// NodeBase contains fields common to both NodeStatus (callback payload)
// and NodeResult (full execution record). All fields have JSON tags so
// they can be serialized directly without manual map construction.
type NodeBase struct {
	NodeID    string    `json:"node_id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"` // completed | failed | skipped | aborted
	Output    string    `json:"output,omitempty"`
	Skipped   bool      `json:"skipped"`
	Aborted   bool      `json:"aborted"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
}

// Elapsed returns the wall-clock duration of this node's execution.
func (b *NodeBase) Elapsed() time.Duration { return b.EndedAt.Sub(b.StartedAt) }

// NodeStatus is the lightweight, JSON-serializable payload sent through
// the per-node progress callback. It contains no Go-only fields.
type NodeStatus struct {
	NodeBase
}

// NodeResult is the full execution record including the Go error.
// It embeds NodeBase so all JSON-tagged fields are promoted.
type NodeResult struct {
	NodeBase
	Value *Value `json:"value,omitempty"`
	Err   error  `json:"-"`
}

// WorkPlanResult is the execution summary of the entire WorkPlan.
type WorkPlanResult struct {
	NodeResults  []*NodeResult
	Vars         map[string]string
	Checkpoints  map[string]string
	Aborted      bool
	AbortReason  string
	TotalElapsed time.Duration
}

// FinalOutput returns the last non-empty, non-skipped output.
func (r *WorkPlanResult) FinalOutput() string {
	for i := len(r.NodeResults) - 1; i >= 0; i-- {
		nr := r.NodeResults[i]
		if !nr.Skipped && !nr.Aborted && nr.Err == nil && nr.Output != "" {
			return nr.Output
		}
	}
	return `""`
}

// FinalOutputString returns FinalOutput as a plain string (unwrapping JSON if needed).
func (r *WorkPlanResult) FinalOutputString() string {
	raw := r.FinalOutput()
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s
	}
	return raw
}

// ToJSON normalizes a string to valid JSON.
func ToJSON(s string) string {
	if json.Valid([]byte(s)) {
		return s
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// FromJSON attempts to unwrap a JSON string to plain text.
func FromJSON(s string) string {
	var str string
	if json.Unmarshal([]byte(s), &str) == nil {
		return str
	}
	return s
}

// RenderTemplate renders template variables in a string.
// Supports {{.PrevResult}}, {{.PrevResults.nodeID}}, and {{.Vars.key}}.
func RenderTemplate(tmpl string, ec *WorkflowContext) string {
	if ec == nil {
		return tmpl
	}
	result := tmpl
	result = replaceAll(result, "{{.PrevResult}}", ec.PrevText())
	nodeIDs := make(map[string]struct{}, len(ec.PrevResults)+len(ec.Results))
	for nodeID := range ec.PrevResults {
		nodeIDs[nodeID] = struct{}{}
	}
	for nodeID := range ec.Results {
		nodeIDs[nodeID] = struct{}{}
	}
	for nodeID := range nodeIDs {
		result = replaceAll(result, "{{.PrevResults."+nodeID+"}}", ec.ResultText(nodeID))
	}
	variableKeys := make(map[string]struct{}, len(ec.Vars)+len(ec.Variables))
	for key := range ec.Vars {
		variableKeys[key] = struct{}{}
	}
	for key := range ec.Variables {
		variableKeys[key] = struct{}{}
	}
	for key := range variableKeys {
		result = replaceAll(result, "{{.Vars."+key+"}}", ec.VariableText(key))
	}
	return result
}

func replaceAll(s, old, new string) string {
	for i := 0; i < len(s)-len(old)+1; i++ {
		if s[i:i+len(old)] == old {
			s = s[:i] + new + s[i+len(old):]
			i += len(new) - 1
		}
	}
	return s
}
