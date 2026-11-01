package jobcache_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/1-Archit-1/Tilcayo/internal/jobcache"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

func TestPutAndGet(t *testing.T) {
	c := jobcache.New()

	// 1. Empty ID should fail
	err := c.Put(jobcache.Job{Engine: "general", Query: "SELECT 1"})
	if !errors.Is(err, jobcache.ErrEmptyJobID) {
		t.Fatalf("Put with empty ID got error %v, want %v", err, jobcache.ErrEmptyJobID)
	}

	// 2. Put valid job
	now := time.Now()
	j1 := jobcache.Job{
		ID:        "job-1",
		Engine:    "general",
		Query:     "SELECT 1",
		CreatedAt: now,
	}
	if err := c.Put(j1); err != nil {
		t.Fatalf("Put valid job failed: %v", err)
	}

	// 3. Duplicate ID should fail
	err = c.Put(jobcache.Job{ID: "job-1", Engine: "spatial"})
	if !errors.Is(err, jobcache.ErrJobExists) {
		t.Fatalf("Put duplicate ID got error %v, want %v", err, jobcache.ErrJobExists)
	}

	// 4. Get existing job
	got, ok := c.Get("job-1")
	if !ok {
		t.Fatalf("Get('job-1') returned ok=false, want true")
	}
	if got.ID != "job-1" || got.Engine != "general" || got.Query != "SELECT 1" {
		t.Fatalf("Get('job-1') returned unexpected job: %+v", got)
	}
	// Initial state defaults to StatePending if empty
	if got.State != orchestrator.StatePending {
		t.Fatalf("Get('job-1') State = %q, want %q", got.State, orchestrator.StatePending)
	}

	// 5. Get non-existent job
	_, ok = c.Get("non-existent")
	if ok {
		t.Fatalf("Get('non-existent') returned ok=true, want false")
	}
}

func TestUpdate(t *testing.T) {
	c := jobcache.New()

	// Update non-existent job
	event := orchestrator.JobEvent{
		JobID: "job-ghost",
		State: orchestrator.StateRunning,
	}
	if _, ok := c.Update(event); ok {
		t.Fatalf("Update non-existent job returned ok=true, want false")
	}

	// Put job
	_ = c.Put(jobcache.Job{ID: "job-2", Engine: "spatial", Query: "SELECT 2"})

	// Transition to running
	updated, ok := c.Update(orchestrator.JobEvent{
		JobID: "job-2",
		State: orchestrator.StateRunning,
	})
	if !ok || updated.State != orchestrator.StateRunning {
		t.Fatalf("Update to running failed: ok=%v, state=%q", ok, updated.State)
	}

	// Transition to succeeded (terminal)
	finishTime := time.Now()
	updated, ok = c.Update(orchestrator.JobEvent{
		JobID:      "job-2",
		State:      orchestrator.StateSucceeded,
		ExitCode:   0,
		FinishedAt: finishTime,
	})
	if !ok {
		t.Fatalf("Update to succeeded returned ok=false")
	}
	if updated.State != orchestrator.StateSucceeded {
		t.Fatalf("State = %q, want %q", updated.State, orchestrator.StateSucceeded)
	}
	if updated.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", updated.ExitCode)
	}
	if !updated.FinishedAt.Equal(finishTime) {
		t.Fatalf("FinishedAt = %v, want %v", updated.FinishedAt, finishTime)
	}

	// Verify Get sees the update
	stored, ok := c.Get("job-2")
	if !ok || stored.State != orchestrator.StateSucceeded {
		t.Fatalf("Stored job state after terminal update = %q", stored.State)
	}
}

func TestDeleteAndList(t *testing.T) {
	c := jobcache.New()

	// Empty list
	if list := c.List(); len(list) != 0 {
		t.Fatalf("List() on empty cache returned %d items, want 0", len(list))
	}

	// Insert 3 jobs
	_ = c.Put(jobcache.Job{ID: "job-a", Engine: "general"})
	_ = c.Put(jobcache.Job{ID: "job-b", Engine: "spatial"})
	_ = c.Put(jobcache.Job{ID: "job-c", Engine: "ml"})

	if list := c.List(); len(list) != 3 {
		t.Fatalf("List() returned %d items, want 3", len(list))
	}

	// Delete job-b
	if ok := c.Delete("job-b"); !ok {
		t.Fatalf("Delete('job-b') returned false, want true")
	}

	// Delete again should return false
	if ok := c.Delete("job-b"); ok {
		t.Fatalf("Second Delete('job-b') returned true, want false")
	}

	// Get deleted job
	if _, ok := c.Get("job-b"); ok {
		t.Fatalf("Get('job-b') after delete returned ok=true, want false")
	}

	// List after delete
	if list := c.List(); len(list) != 2 {
		t.Fatalf("List() after delete returned %d items, want 2", len(list))
	}
}

func TestDefensiveCopy(t *testing.T) {
	c := jobcache.New()
	_ = c.Put(jobcache.Job{ID: "job-defensive", State: orchestrator.StatePending})

	got, ok := c.Get("job-defensive")
	if !ok {
		t.Fatalf("Get returned ok=false")
	}

	// Mutating the returned struct value should NOT mutate the cached struct
	got.State = orchestrator.StateCancelled

	stored, _ := c.Get("job-defensive")
	if stored.State != orchestrator.StatePending {
		t.Fatalf("Mutating caller copy corrupted cache! stored.State = %q, want %q", stored.State, orchestrator.StatePending)
	}
}

func TestConcurrentAccess(t *testing.T) {
	c := jobcache.New()
	var wg sync.WaitGroup

	numGoroutines := 20
	jobsPerRoutine := 50

	// Concurrent Put
	for i := range numGoroutines {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := range jobsPerRoutine {
				id := fmt.Sprintf("job-%d-%d", routineID, j)
				_ = c.Put(jobcache.Job{
					ID:     id,
					Engine: "general",
					Query:  "SELECT 1",
				})
			}
		}(i)
	}

	// Concurrent Get and List
	for i := range numGoroutines {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := range jobsPerRoutine {
				id := fmt.Sprintf("job-%d-%d", routineID, j)
				_, _ = c.Get(id)
				_ = c.List()
			}
		}(i)
	}

	// Concurrent Update
	for i := range numGoroutines {
		wg.Add(1)
		go func(routineID int) {
			defer wg.Done()
			for j := range jobsPerRoutine {
				id := fmt.Sprintf("job-%d-%d", routineID, j)
				_, _ = c.Update(orchestrator.JobEvent{
					JobID: id,
					State: orchestrator.StateRunning,
				})
			}
		}(i)
	}

	wg.Wait()

	// Verify all jobs are present
	all := c.List()
	expectedTotal := numGoroutines * jobsPerRoutine
	if len(all) != expectedTotal {
		t.Fatalf("List() count = %d, want %d", len(all), expectedTotal)
	}
}
