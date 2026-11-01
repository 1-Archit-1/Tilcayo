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

func TestUpdateUnknownJob(t *testing.T) {
	c := jobcache.New()
	job, changed, found := c.Update(orchestrator.JobEvent{
		JobID: "job-ghost",
		State: orchestrator.StateRunning,
	})
	if found || changed || job != (jobcache.Job{}) {
		t.Fatalf("Update unknown job = (%+v, changed=%v, found=%v), want empty, false, false", job, changed, found)
	}
}

func TestUpdate(t *testing.T) {
	finished := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	running := orchestrator.JobEvent{State: orchestrator.StateRunning}
	succeeded := orchestrator.JobEvent{State: orchestrator.StateSucceeded, FinishedAt: finished}
	failed := orchestrator.JobEvent{State: orchestrator.StateFailed, ExitCode: 1, Error: "bad SQL", FinishedAt: finished}
	cancelled := orchestrator.JobEvent{State: orchestrator.StateCancelled, ExitCode: 137, FinishedAt: finished}
	oomAfterCancel := orchestrator.JobEvent{State: orchestrator.StateFailed, ExitCode: 137, Error: "out of memory", FinishedAt: finished.Add(time.Second)}

	tests := []struct {
		name        string
		events      []orchestrator.JobEvent
		wantChanged []bool // one per event
		want        jobcache.Job
	}{
		{
			name:        "pending to running to succeeded",
			events:      []orchestrator.JobEvent{running, succeeded},
			wantChanged: []bool{true, true},
			want:        jobcache.Job{State: orchestrator.StateSucceeded, FinishedAt: finished},
		},
		{
			name:        "failed records exit code and error",
			events:      []orchestrator.JobEvent{running, failed},
			wantChanged: []bool{true, true},
			want:        jobcache.Job{State: orchestrator.StateFailed, ExitCode: 1, Error: "bad SQL", FinishedAt: finished},
		},
		{
			name:        "repeated state is not a change",
			events:      []orchestrator.JobEvent{running, running},
			wantChanged: []bool{true, false},
			want:        jobcache.Job{State: orchestrator.StateRunning},
		},
		{
			name:        "replayed terminal event after reconnect is not a change",
			events:      []orchestrator.JobEvent{succeeded, succeeded},
			wantChanged: []bool{true, false},
			want:        jobcache.Job{State: orchestrator.StateSucceeded, FinishedAt: finished},
		},
		{
			name:        "stale running after succeeded is ignored",
			events:      []orchestrator.JobEvent{running, succeeded, running},
			wantChanged: []bool{true, true, false},
			want:        jobcache.Job{State: orchestrator.StateSucceeded, FinishedAt: finished},
		},
		{
			name:        "container exit after cancel keeps cancelled",
			events:      []orchestrator.JobEvent{running, cancelled, oomAfterCancel},
			wantChanged: []bool{true, true, false},
			want:        jobcache.Job{State: orchestrator.StateCancelled, ExitCode: 137, FinishedAt: finished},
		},
		{
			name:        "empty state is ignored",
			events:      []orchestrator.JobEvent{running, {}},
			wantChanged: []bool{true, false},
			want:        jobcache.Job{State: orchestrator.StateRunning},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := jobcache.New()
			if err := c.Put(jobcache.Job{ID: "job-1"}); err != nil {
				t.Fatal(err)
			}
			tt.want.ID = "job-1"

			for i, ev := range tt.events {
				ev.JobID = "job-1"
				_, changed, found := c.Update(ev)
				if !found {
					t.Fatalf("event %d (%q): found = false, want true", i, ev.State)
				}
				if changed != tt.wantChanged[i] {
					t.Errorf("event %d (%q): changed = %v, want %v", i, ev.State, changed, tt.wantChanged[i])
				}
			}

			got, _ := c.Get("job-1")
			if got != tt.want {
				t.Errorf("stored job = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDeleteAndList(t *testing.T) {
	c := jobcache.New()

	// Empty list
	if list := c.List(); len(list) != 0 {
		t.Fatalf("List() on empty cache returned %d items, want 0", len(list))
	}

	// Insert 3 jobs
	for _, j := range []jobcache.Job{
		{ID: "job-a", Engine: "general"},
		{ID: "job-b", Engine: "spatial"},
		{ID: "job-c", Engine: "ml"},
	} {
		if err := c.Put(j); err != nil {
			t.Fatal(err)
		}
	}

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
	if err := c.Put(jobcache.Job{ID: "job-defensive", State: orchestrator.StatePending}); err != nil {
		t.Fatal(err)
	}

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
				// t.Error, not t.Fatal: Fatal must only be called from the test goroutine.
				if err := c.Put(jobcache.Job{ID: id, Engine: "general", Query: "SELECT 1"}); err != nil {
					t.Error(err)
				}
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
				_, _, _ = c.Update(orchestrator.JobEvent{
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
