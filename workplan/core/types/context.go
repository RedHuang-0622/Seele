// Package types provides pure data types shared by all workplan layers.
// This package has zero dependencies on other workplan packages.
package types

import (
	"encoding/json"
	"reflect"
	"time"
)

// WorkflowContext carries shared state during graph execution.
// Named WorkflowContext to avoid collision with stdlib context.Context.
type WorkflowContext struct {
	PrevOutput  string            // JSON output from the immediately previous node
	PrevResults map[string]string // nodeID → output for all executed nodes (multi-upstream reference)
	Vars        map[string]string // Named variables written by Emit nodes
	Result      *WorkPlanResult   // Accumulated execution result
	Metadata    map[string]any    // Extension fields
}

// NewWorkflowContext creates an empty workflow context.
func NewWorkflowContext() *WorkflowContext {
	return &WorkflowContext{
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
		PrevOutput:  wc.PrevOutput,
		PrevResults: cloneStringMap(wc.PrevResults),
		Vars:        cloneStringMap(wc.Vars),
		Result:      cloneWorkPlanResult(wc.Result),
		Metadata:    cloneMetadata(wc.Metadata),
	}
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
	Err error `json:"-"`
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
	result = replaceAll(result, "{{.PrevResult}}", FromJSON(ec.PrevOutput))
	for nodeID, output := range ec.PrevResults {
		result = replaceAll(result, "{{.PrevResults."+nodeID+"}}", FromJSON(output))
	}
	for key, jsonVal := range ec.Vars {
		result = replaceAll(result, "{{.Vars."+key+"}}", FromJSON(jsonVal))
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
