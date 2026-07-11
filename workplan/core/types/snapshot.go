package types

import "time"

// Snapshot captures the execution state at a point in time for checkpoint/resume.
type Snapshot struct {
	NodeID    string           // The node being executed when snapshot was taken
	Context   *WorkflowContext // Current workflow context (vars, prev output, result)
	Timestamp time.Time        // When the snapshot was taken
	Status    Status           // Execution status at snapshot time
}

// ConditionRegistry maps condition labels to EdgeCondition functions.
// Used by serialization to reconstruct non-serializable condition functions.
type ConditionRegistry struct {
	conditions map[string]EdgeCondition
}

// EdgeCondition is a predicate that determines whether an edge should be traversed.
type EdgeCondition func(wc *WorkflowContext) bool

// NewConditionRegistry creates an empty condition registry.
func NewConditionRegistry() *ConditionRegistry {
	return &ConditionRegistry{conditions: make(map[string]EdgeCondition)}
}

// Register binds a label to a condition function.
func (r *ConditionRegistry) Register(name string, cond EdgeCondition) {
	r.conditions[name] = cond
}

// Resolve looks up a condition function by label.
func (r *ConditionRegistry) Resolve(name string) (EdgeCondition, bool) {
	c, ok := r.conditions[name]
	return c, ok
}
