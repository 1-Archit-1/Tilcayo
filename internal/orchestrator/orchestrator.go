package orchestrator

import (
	"context"
	"time"
)

type JobSpec struct {
	Engine string
	Query string
}

type JobEvent struct {
	JobID string
	State State
	ExitCode   int
  	Error      string
   	FinishedAt time.Time
}

type Orchestrator interface {
	Run(ctx context.Context, jobID string, spec JobSpec) error
	Events(ctx context.Context) (<-chan JobEvent, error) 
	Cancel(ctx context.Context, jobID string) error
}
