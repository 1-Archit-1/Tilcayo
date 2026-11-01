package orchestrator

import (
	"context"
	"time"
)

// JobSpec specifies the parameters needed to execute an ephemeral worker.
type JobSpec struct {
	Engine string
	Query  string
}

// JobEvent reports orchestrator state transitions and execution results.
// ExitCode, Error, and FinishedAt are only meaningful once State.IsTerminal() is true.
type JobEvent struct {
	JobID      string
	State      State
	ExitCode   int
	Error      string
	FinishedAt time.Time
}

// Orchestrator defines the contract for managing worker lifecycles across backends (Docker, Kubernetes).
type Orchestrator interface {
	// Run starts a worker. The control plane mints jobID first; the output path is derived from it.
	Run(ctx context.Context, jobID string, spec JobSpec) error

	// Events returns one stream for all jobs: first the current state of every existing job,
	// then live changes. Reconnect and re-check are internal.
	Events(ctx context.Context) (<-chan JobEvent, error)

	// Cancel stops a running job. The record is retained and marked cancelled.
	Cancel(ctx context.Context, jobID string) error
}
