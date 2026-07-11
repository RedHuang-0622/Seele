// Package types provides pure data types shared by all workplan layers.
// This package has zero dependencies on other workplan packages.
package types

import (
	"encoding/json"
	"time"
)

// WorkflowContext carries shared state during graph execution.
// Named WorkflowContext to avoid collision with stdlib context.Context.
type WorkflowContext struct {
	PrevOutput string            // JSON output from the previous node
	Vars       map[string]string // Named variables written by Emit nodes
	Result     *WorkPlanResult   // Accumulated execution result
	Metadata   map[string]any    // Extension fields
}

// NewWorkflowContext creates an empty workflow context.
func NewWorkflowContext() *WorkflowContext {
	return &WorkflowContext{
		Vars:     make(map[string]string),
		Result:   &WorkPlanResult{Checkpoints: make(map[string]string)},
		Metadata: make(map[string]any),
	}
}

// NodeResult records the execution result of a single node.
type NodeResult struct {
	NodeID    string
	Kind      string
	Output    string
	Skipped   bool
	Aborted   bool
	Err       error
	StartedAt time.Time
	EndedAt   time.Time
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
// Supports {{.PrevResult}} and {{.Vars.key}}.
func RenderTemplate(tmpl string, ec *WorkflowContext) string {
	if ec == nil {
		return tmpl
	}
	result := tmpl
	result = replaceAll(result, "{{.PrevResult}}", FromJSON(ec.PrevOutput))
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
