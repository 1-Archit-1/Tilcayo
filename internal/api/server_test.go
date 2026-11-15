package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/1-Archit-1/Tilcayo/internal/api"
	"github.com/1-Archit-1/Tilcayo/internal/config"
	"github.com/1-Archit-1/Tilcayo/internal/jobcache"
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
)

type mockOrchestrator struct {
	lastJobID   string
	lastSpec    orchestrator.JobSpec
	failRun     error
	runCalled   bool
	eventsChan  chan orchestrator.JobEvent
	failEvents  error
	failCancel  error
	cancelJobID string
}

func (m *mockOrchestrator) Run(ctx context.Context, jobID string, spec orchestrator.JobSpec) error {
	m.runCalled = true
	m.lastJobID = jobID
	m.lastSpec = spec
	return m.failRun
}

func (m *mockOrchestrator) Events(ctx context.Context) (<-chan orchestrator.JobEvent, error) {
	if m.failEvents != nil {
		return nil, m.failEvents
	}
	return m.eventsChan, nil
}

func (m *mockOrchestrator) Cancel(ctx context.Context, jobID string) error {
	m.cancelJobID = jobID
	return m.failCancel
}

func setupTestServer(t *testing.T) (*api.Server, *jobcache.Cache) {
	t.Helper()
	srv, cache, _ := setupTestServerWithOrch(t, &mockOrchestrator{})
	return srv, cache
}

func setupTestServerWithOrch(t *testing.T, orch orchestrator.Orchestrator) (*api.Server, *jobcache.Cache, *mockOrchestrator) {
	t.Helper()
	cfg := &config.Config{
		ListenAddr:  "127.0.0.1:8080",
		ResultsMode: config.ResultsS3,
		Images:      make(map[string]string),
	}
	cache := jobcache.New()
	mock, _ := orch.(*mockOrchestrator)
	srv := api.NewServer(cfg, cache, orch)
	return srv, cache, mock
}

func TestMiddlewareRequestID(t *testing.T) {
	srv, _ := setupTestServer(t)
	router := srv.Routes().(chi.Router)

	var capturedID string
	router.Get("/test-id", func(w http.ResponseWriter, r *http.Request) {
		capturedID = middleware.GetReqID(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test-id", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if capturedID == "" {
		t.Errorf("expected request ID injected into request context by chi middleware, got empty")
	}
}

func TestCreateJob_Success(t *testing.T) {
	mockOrch := &mockOrchestrator{}
	srv, cache, _ := setupTestServerWithOrch(t, mockOrch)
	handler := srv.Routes()

	body := `{"engine":"general","sql":"SELECT 42","webhook_url":"https://example.com/hook"}`
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}

	var resp api.CreateJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response JSON: %v", err)
	}

	if resp.ID == "" {
		t.Errorf("expected non-empty job ID in response")
	}
	if resp.State != orchestrator.StatePending {
		t.Errorf("job state = %q, want %q", resp.State, orchestrator.StatePending)
	}

	// Verify orchestrator.Run was called with correct parameters
	if !mockOrch.runCalled {
		t.Errorf("expected orchestrator.Run to be called")
	}
	if mockOrch.lastJobID != resp.ID {
		t.Errorf("orchestrator jobID = %q, want %q", mockOrch.lastJobID, resp.ID)
	}
	if mockOrch.lastSpec.Engine != "general" {
		t.Errorf("orchestrator engine = %q, want general", mockOrch.lastSpec.Engine)
	}
	if mockOrch.lastSpec.Query != "SELECT 42" {
		t.Errorf("orchestrator query = %q, want SELECT 42", mockOrch.lastSpec.Query)
	}

	// Verify job was stored in the cache
	cached, ok := cache.Get(resp.ID)
	if !ok {
		t.Fatalf("job %q was not saved to cache", resp.ID)
	}
	if cached.Engine != "general" {
		t.Errorf("cached engine = %q, want general", cached.Engine)
	}
	if cached.Query != "SELECT 42" {
		t.Errorf("cached query = %q, want SELECT 42", cached.Query)
	}
	if cached.WebhookURL != "https://example.com/hook" {
		t.Errorf("cached webhook = %q, want https://example.com/hook", cached.WebhookURL)
	}
	if cached.State != orchestrator.StatePending {
		t.Errorf("cached state = %q, want pending", cached.State)
	}
}

func TestCreateJob_OrchestratorRunFailure(t *testing.T) {
	mockOrch := &mockOrchestrator{
		failRun: errors.New("container launch failure"),
	}
	srv, cache, _ := setupTestServerWithOrch(t, mockOrch)
	handler := srv.Routes()

	body := `{"engine":"general","sql":"SELECT 42"}`
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	var errResp api.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error JSON: %v", err)
	}
	if !strings.Contains(errResp.Error, "failed to dispatch job") {
		t.Errorf("error = %q, want 'failed to dispatch job'", errResp.Error)
	}

	// Verify the job was deleted from cache on dispatch failure
	jobs := cache.List()
	if len(jobs) != 0 {
		t.Fatalf("expected 0 jobs in cache, got %d: %+v", len(jobs), jobs)
	}
}

func TestCreateJob_ValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		errSubstr  string
	}{
		{
			name:       "invalid JSON",
			body:       `{not valid json}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "invalid",
		},
		{
			name:       "unknown engine",
			body:       `{"engine":"quantum","sql":"SELECT 1"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "unknown engine",
		},
		{
			name:       "missing engine",
			body:       `{"sql":"SELECT 1"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "engine",
		},
		{
			name:       "empty sql",
			body:       `{"engine":"general","sql":""}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "sql",
		},
		{
			name:       "whitespace-only sql",
			body:       `{"engine":"general","sql":"   \t\n  "}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "sql",
		},
		{
			name:       "unknown fields rejected",
			body:       `{"engine":"general","sql":"SELECT 1","foo":"bar"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "invalid",
		},
		{
			name:       "trailing data after JSON",
			body:       `{"engine":"general","sql":"SELECT 1"} trailing`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "invalid",
		},
		{
			name:       "sql with NUL byte",
			body:       `{"engine":"general","sql":"SELECT 'hello\u0000world'"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "null",
		},
		{
			name:       "sql exceeds 128000 bytes",
			body:       fmt.Sprintf(`{"engine":"general","sql":"%s"}`, strings.Repeat("A", 128001)),
			wantStatus: http.StatusBadRequest,
			errSubstr:  "128,000",
		},
		{
			name:       "webhook relative url",
			body:       `{"engine":"general","sql":"SELECT 1","webhook_url":"/path/only"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "webhook_url",
		},
		{
			name:       "webhook missing scheme",
			body:       `{"engine":"general","sql":"SELECT 1","webhook_url":"example.com/hook"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "webhook_url",
		},
		{
			name:       "webhook unsupported scheme",
			body:       `{"engine":"general","sql":"SELECT 1","webhook_url":"ftp://example.com/hook"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "webhook_url",
		},
		{
			name:       "webhook missing host",
			body:       `{"engine":"general","sql":"SELECT 1","webhook_url":"http://"}`,
			wantStatus: http.StatusBadRequest,
			errSubstr:  "webhook_url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := setupTestServer(t)
			handler := srv.Routes()

			req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}

			var errResp api.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
				t.Fatalf("failed to decode error JSON: %v", err)
			}
			if !strings.Contains(strings.ToLower(errResp.Error), strings.ToLower(tt.errSubstr)) {
				t.Errorf("error message %q does not contain %q", errResp.Error, tt.errSubstr)
			}
		})
	}
}

func TestGetJob_Found(t *testing.T) {
	srv, cache := setupTestServer(t)
	handler := srv.Routes()

	finished := time.Now().UTC().Truncate(time.Second)
	testJob := jobcache.Job{
		ID:         "job-xyz-123",
		Engine:     "spatial",
		Query:      "SELECT ST_Point(1, 2)",
		WebhookURL: "https://example.com/callback",
		State:      orchestrator.StateSucceeded,
		ExitCode:   0,
		CreatedAt:  finished.Add(-5 * time.Second),
		FinishedAt: finished,
	}
	if err := cache.Put(testJob); err != nil {
		t.Fatalf("failed to seed cache: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/jobs/job-xyz-123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp api.JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}

	if resp.ID != testJob.ID {
		t.Errorf("id = %q, want %q", resp.ID, testJob.ID)
	}
	if resp.Engine != testJob.Engine {
		t.Errorf("engine = %q, want %q", resp.Engine, testJob.Engine)
	}
	if resp.State != orchestrator.StateSucceeded {
		t.Errorf("state = %q, want %q", resp.State, orchestrator.StateSucceeded)
	}
	if resp.ExitCode == nil || *resp.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", resp.ExitCode)
	}
	if resp.FinishedAt == nil || !resp.FinishedAt.Equal(finished) {
		t.Errorf("finished_at = %v, want %v", resp.FinishedAt, finished)
	}

	// Verify query and webhook_url are omitted from GET response
	bodyStr := rec.Body.String()
	if strings.Contains(bodyStr, `"query"`) {
		t.Errorf("response body unexpectedly contains 'query': %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"webhook_url"`) {
		t.Errorf("response body unexpectedly contains 'webhook_url': %s", bodyStr)
	}
}

func TestGetJob_PendingExitCodeOmitted(t *testing.T) {
	srv, cache := setupTestServer(t)
	handler := srv.Routes()

	pendingJob := jobcache.Job{
		ID:         "job-pending-1",
		Engine:     "general",
		Query:      "SELECT 1",
		State:      orchestrator.StatePending,
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Time{}, // zero value, job not finished
	}
	if err := cache.Put(pendingJob); err != nil {
		t.Fatalf("failed to seed cache: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/jobs/job-pending-1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp api.JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if resp.ExitCode != nil {
		t.Errorf("expected exit_code to be nil for pending job, got %d", *resp.ExitCode)
	}

	// Verify "exit_code" key is omitted from raw JSON
	if strings.Contains(rec.Body.String(), "exit_code") {
		t.Errorf("expected 'exit_code' to be omitted from JSON for pending job, got %s", rec.Body.String())
	}
}

func TestGetJob_TerminalWithoutFinishedAtIncludesExitCode(t *testing.T) {
	srv, cache := setupTestServer(t)
	handler := srv.Routes()

	failedJob := jobcache.Job{
		ID:         "job-failed-no-finished-at",
		Engine:     "general",
		Query:      "SELECT 1",
		State:      orchestrator.StateFailed,
		ExitCode:   137,
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Time{}, // no timestamp recorded
	}
	if err := cache.Put(failedJob); err != nil {
		t.Fatalf("failed to seed cache: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/jobs/job-failed-no-finished-at", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp api.JobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if resp.ExitCode == nil || *resp.ExitCode != 137 {
		t.Errorf("exit_code = %v, want 137", resp.ExitCode)
	}
	if resp.FinishedAt != nil {
		t.Errorf("finished_at = %v, want nil", resp.FinishedAt)
	}
	if !strings.Contains(rec.Body.String(), `"exit_code":137`) {
		t.Errorf("expected exit_code in body: %s", rec.Body.String())
	}
}

func TestGetJob_NotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Routes()

	req := httptest.NewRequest(http.MethodGet, "/jobs/non-existent-id", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var errResp api.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error JSON: %v", err)
	}
	if !strings.Contains(strings.ToLower(errResp.Error), "not found") {
		t.Errorf("error = %q, want message containing 'not found'", errResp.Error)
	}
}

func TestCreateJob_ExactMaxSQLLength(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Routes()

	// Exactly 128,000 bytes should be accepted
	sql := strings.Repeat("A", 128000)
	body := fmt.Sprintf(`{"engine":"general","sql":%q}`, sql)
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

func TestMaxBytesLimit(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Routes()

	// Valid JSON whose SQL is 2 MiB (exceeding the 1 MiB body limit)
	hugeSQL := strings.Repeat("A", 2*1024*1024)
	body := fmt.Sprintf(`{"engine":"general","sql":%q}`, hugeSQL)
	req := httptest.NewRequest(http.MethodPost, "/jobs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d (StatusRequestEntityTooLarge) for body exceeding MaxBytesReader", rec.Code, http.StatusRequestEntityTooLarge)
	}

	var errResp api.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode error JSON: %v", err)
	}
	if !strings.Contains(strings.ToLower(errResp.Error), "large") {
		t.Errorf("error = %q, want message mentioning large", errResp.Error)
	}
}

func TestChiNotFoundAndMethodNotAllowed(t *testing.T) {
	srv, _ := setupTestServer(t)
	handler := srv.Routes()

	t.Run("404 not found in JSON error format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/unknown-route", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
		}
		var errResp api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if errResp.Error != "not found" {
			t.Errorf("error = %q, want 'not found'", errResp.Error)
		}
	})

	t.Run("405 method not allowed in JSON error format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/jobs", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
		var errResp api.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		if errResp.Error != "method not allowed" {
			t.Errorf("error = %q, want 'method not allowed'", errResp.Error)
		}
	})
}
