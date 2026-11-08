// Package api provides HTTP routing, request validation, and handlers
// for the Tilcayo control plane REST API.
package api

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/jobcache"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

const (
	maxSQLLength = 128000
	maxBodyBytes = 1 << 20 // 1 MiB limit on request body
)

// CreateJobRequest defines the JSON schema for POST /jobs.
type CreateJobRequest struct {
	Engine     string `json:"engine"`
	SQL        string `json:"sql"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

// CreateJobResponse is returned on successful submission (202 Accepted).
type CreateJobResponse struct {
	ID    string             `json:"id"`
	State orchestrator.State `json:"state"`
}

// ErrorResponse defines the standard error payload {"error": "..."}.
type ErrorResponse struct {
	Error string `json:"error"`
}

// JobResponse is returned for GET /jobs/{id}.
type JobResponse struct {
	ID         string             `json:"id"`
	Engine     string             `json:"engine"`
	Query      string             `json:"query"`
	WebhookURL string             `json:"webhook_url,omitempty"`
	State      orchestrator.State `json:"state"`
	ExitCode   *int               `json:"exit_code,omitempty"`
	Error      string             `json:"error,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	FinishedAt *time.Time         `json:"finished_at,omitempty"`
}

// Server holds dependencies for HTTP handlers.
type Server struct {
	cfg   *config.Config
	cache *jobcache.Cache
}

// NewServer creates a new API Server with the provided dependencies.
func NewServer(cfg *config.Config, cache *jobcache.Cache) *Server {
	return &Server{
		cfg:   cfg,
		cache: cache,
	}
}

// Routes builds and returns the Chi HTTP router with middleware and endpoints.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})

	r.Post("/jobs", s.handleCreateJob)
	r.Get("/jobs/{id}", s.handleGetJob)

	return r
}

// handleCreateJob handles POST /jobs.
// It validates the engine and query, assigns a new JobID, caches the pending job,
// and answers 202 Accepted.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	// Guard against unbounded request bodies (1 MiB max).
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req CreateJobRequest
	if err := dec.Decode(&req); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Engine == "" {
		writeError(w, http.StatusBadRequest, "engine is required")
		return
	}
	if _, err := s.cfg.ImageFor(req.Engine); errors.Is(err, config.ErrUnknownEngine) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown engine: %q", req.Engine))
		return
	}
	if strings.TrimSpace(req.SQL) == "" {
		writeError(w, http.StatusBadRequest, "sql query is required")
		return
	}
	if len(req.SQL) > maxSQLLength {
		writeError(w, http.StatusBadRequest, "sql query exceeds maximum length of 128,000 bytes")
		return
	}
	if strings.Contains(req.SQL, "\x00") {
		writeError(w, http.StatusBadRequest, "sql query contains null byte")
		return
	}

	jobID := newJobID()
	job := jobcache.Job{
		ID:         jobID,
		Engine:     req.Engine,
		Query:      req.SQL,
		WebhookURL: req.WebhookURL,
		State:      orchestrator.StatePending,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.cache.Put(job); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create job")
		return
	}
	writeJSON(w, http.StatusAccepted, CreateJobResponse{ID: jobID, State: job.State})
}

// handleGetJob handles GET /jobs/{id}.
// It retrieves the job from the cache and answers 200 OK, or 404 Not Found if missing.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	job, ok := s.cache.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	var finishedAt *time.Time
	var exitCode *int
	if !job.FinishedAt.IsZero() {
		finishedAt = &job.FinishedAt
		exitCode = &job.ExitCode
	}

	resp := JobResponse{
		ID:         job.ID,
		Engine:     job.Engine,
		Query:      job.Query,
		WebhookURL: job.WebhookURL,
		State:      job.State,
		ExitCode:   exitCode,
		Error:      job.Error,
		CreatedAt:  job.CreatedAt,
		FinishedAt: finishedAt,
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeJSON writes a JSON response with the given status code and sets Content-Type.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError sends an ErrorResponse with the given HTTP status code.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

// newJobID generates a unique random job identifier: "job-" + 8 random hex bytes.
func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("job-%x", b)
}
