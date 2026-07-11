package types

import "fmt"

// Status represents the execution state of a WorkPlan.
type Status int

const (
	StatusPending   Status = iota // Not started
	StatusRunning                 // Currently executing
	StatusCompleted               // Finished successfully
	StatusFailed                  // Finished with error
	StatusAborted                 // Cancelled by user or timeout
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusAborted:
		return "aborted"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}
