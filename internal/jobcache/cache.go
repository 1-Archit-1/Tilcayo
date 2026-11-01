// Package jobcache holds the control plane's in-memory view of jobs and their
// state. Orchestrator objects are the source of truth (issue #7); this cache
// is safe for concurrent use.
package jobcache

import (
	"errors"
	"sync"
	"time"

	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

var (
	// ErrEmptyJobID is returned when attempting to cache a job with an empty ID.
	ErrEmptyJobID = errors.New("job ID cannot be empty")

	// ErrJobExists is returned when attempting to insert a job whose ID already exists.
	ErrJobExists = errors.New("job already exists")
)

// Job represents a cached job record in Tilcayo.
type Job struct {
	ID         string
	Engine     string
	Query      string
	WebhookURL string
	State      orchestrator.State
	ExitCode   int
	Error      string
	CreatedAt  time.Time
	FinishedAt time.Time
}

// Cache provides thread-safe in-memory storage for jobs and their lifecycle states.
type Cache struct {
	mu   sync.RWMutex
	jobs map[string]Job
}

// New creates an initialized Cache ready for concurrent use.
func New() *Cache {
	return &Cache{
		jobs: make(map[string]Job),
	}
}

// Put stores a new job into the cache. It returns ErrEmptyJobID if job.ID
// is empty, or ErrJobExists if a job with the same ID already exists.
// If job.State is empty, it defaults to orchestrator.StatePending.
func (c *Cache) Put(job Job) error {
	if job.ID == "" {
		return ErrEmptyJobID
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.jobs[job.ID]; ok {
		return ErrJobExists
	}
	if job.State == "" {
		job.State = orchestrator.StatePending
	}
	c.jobs[job.ID] = job
	return nil
}

// Get retrieves a copy of the job with the given ID.
// It returns the job and true if found, or an empty Job and false if not found.
func (c *Cache) Get(id string) (Job, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	job, ok := c.jobs[id]
	return job, ok
}

// Update applies a JobEvent to an existing job in the cache. It updates the state,
// and if terminal, records ExitCode, Error, and FinishedAt.
//
// A terminal state is final: events for a job that is already terminal are
// ignored, because Events replays every job's state after a reconnect and a
// cancelled job's container still reports its exit afterwards (issue #7).
// Events with an empty State are ignored too.
//
// found is false if no job has event.JobID; job is then empty. changed reports
// whether this event changed the stored state, so the caller can act exactly
// once when a job becomes terminal (for example, fire its webhook).
func (c *Cache) Update(event orchestrator.JobEvent) (job Job, changed, found bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	job, found = c.jobs[event.JobID]
	if !found {
		return Job{}, false, false
	}
	if job.State.IsTerminal() || event.State == "" || event.State == job.State {
		return job, false, true
	}

	job.State = event.State
	if event.State.IsTerminal() {
		job.ExitCode = event.ExitCode
		job.Error = event.Error
		job.FinishedAt = event.FinishedAt
	}

	c.jobs[event.JobID] = job
	return job, true, true
}

// Delete removes a job from the cache by ID.
// It returns true if the job was found and removed, or false if it did not exist.
func (c *Cache) Delete(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.jobs[id]; !ok {
		return false
	}
	delete(c.jobs, id)
	return true
}

// List returns a slice snapshot of all jobs currently in the cache,
// in no particular order.
func (c *Cache) List() []Job {
	c.mu.RLock()
	defer c.mu.RUnlock()
	list := make([]Job, 0, len(c.jobs))
	for _, job := range c.jobs {
		list = append(list, job)
	}
	return list
}
