package orchestrator

// State represents the lifecycle state of a job in Tilcayo.
type State string

// Exported constants for job states.
const (
	StatePending    State = "pending"
	StateRunning    State = "running"
	StateSucceeded  State = "succeeded"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

// IsTerminal returns true if the job has finished and reached an end state.
func (s State) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}
