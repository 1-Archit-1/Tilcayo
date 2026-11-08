package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

type mockDockerClient struct {
	createdName   string
	createdConfig *container.Config
	createdHost   *container.HostConfig
	startID       string
	removeID      string
	removeOpts    client.ContainerRemoveOptions
	removeCtx     context.Context
	failCreate    error
	failStart     error

	pingFailures  int // Ping fails this many times, then succeeds
	pingCalls     int
	failNetwork   error
	missingImages map[string]bool
	failImage     error // every ImageInspect fails with this
	inspected     []string
}

func (m *mockDockerClient) Ping(ctx context.Context, opts client.PingOptions) (client.PingResult, error) {
	m.pingCalls++
	if m.pingCalls <= m.pingFailures {
		return client.PingResult{}, errors.New("connection refused")
	}
	return client.PingResult{}, nil
}

func (m *mockDockerClient) NetworkInspect(ctx context.Context, id string, opts client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	return client.NetworkInspectResult{}, m.failNetwork
}

func (m *mockDockerClient) ImageInspect(ctx context.Context, image string, opts ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	m.inspected = append(m.inspected, image)
	if m.failImage != nil {
		return client.ImageInspectResult{}, m.failImage
	}
	if m.missingImages[image] {
		return client.ImageInspectResult{}, fmt.Errorf("no such image: %s: %w", image, cerrdefs.ErrNotFound)
	}
	return client.ImageInspectResult{}, nil
}

func (m *mockDockerClient) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if m.failCreate != nil {
		return client.ContainerCreateResult{}, m.failCreate
	}
	m.createdName = opts.Name
	m.createdConfig = opts.Config
	m.createdHost = opts.HostConfig
	return client.ContainerCreateResult{ID: "c-1234567890ab"}, nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error) {
	m.startID = containerID
	return client.ContainerStartResult{}, m.failStart
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	m.removeCtx = ctx
	m.removeID = containerID
	m.removeOpts = options
	return client.ContainerRemoveResult{}, nil
}

func TestDockerOrchestrator_Run_S3Success(t *testing.T) {
	cfg := &config.Config{
		ResultsMode:    "s3",
		OutputPrefix:   "s3://my-bucket/results/",
		InputPrefixes:  []string{"s3://input-bucket/data/"},
		DockerNetwork:  "tilcayo-net",
		S3KeyID:        "test-key",
		S3Secret:       "test-secret",
		S3Region:       "us-east-1",
		S3Endpoint:     "http://minio:9000",
		S3URLStyle:     "path",
		S3UseSSL:       false,
		MemoryFraction: 0.25, // not the default, so a hardcoded value would fail
	}

	mock := &mockDockerClient{}
	orch := newDockerOrchestrator(mock, cfg, 1000, 1000)

	jobID := "job-xyz-987"
	spec := JobSpec{
		Engine: "general",
		Query:  "SELECT 42",
	}

	err := orch.Run(context.Background(), jobID, spec)
	if err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}

	// Verify container name
	if mock.createdName != "tilcayo-job-xyz-987" {
		t.Errorf("container name = %q; want %q", mock.createdName, "tilcayo-job-xyz-987")
	}

	// Verify User
	if mock.createdConfig.User != "1000:1000" {
		t.Errorf("User = %q; want the control plane's 1000:1000", mock.createdConfig.User)
	}

	// Verify Labels
	if mock.createdConfig.Labels["tilcayo.io/job-id"] != jobID {
		t.Errorf("missing or invalid job-id label: %v", mock.createdConfig.Labels)
	}
	if mock.createdConfig.Labels["app.kubernetes.io/managed-by"] != "tilcayo" {
		t.Errorf("missing or invalid managed-by label: %v", mock.createdConfig.Labels)
	}

	// Verify Environment Variables
	expectedEnv := map[string]string{
		"QUERY":                   "SELECT 42",
		"OUTPUT_PATH":             "s3://my-bucket/results/job-xyz-987/result.parquet",
		"TILCAYO_ALLOWED_INPUTS":  "s3://input-bucket/data/",
		"TILCAYO_MEMORY_FRACTION": "0.25",
		"TILCAYO_S3_KEY_ID":       "test-key",
		"TILCAYO_S3_SECRET":       "test-secret",
		"TILCAYO_S3_REGION":       "us-east-1",
		"TILCAYO_S3_ENDPOINT":     "http://minio:9000",
		"TILCAYO_S3_URL_STYLE":    "path",
		"TILCAYO_S3_USE_SSL":      "false",
	}
	for k, expectedVal := range expectedEnv {
		prefix := k + "="
		found := false
		for _, e := range mock.createdConfig.Env {
			if strings.HasPrefix(e, prefix) {
				found = true
				val := strings.TrimPrefix(e, prefix)
				if val != expectedVal {
					t.Errorf("env %s = %q; want %q", k, val, expectedVal)
				}
				break
			}
		}
		if !found {
			t.Errorf("missing expected env var %s", k)
		}
	}

	// Verify Hardening in HostConfig
	if !mock.createdHost.ReadonlyRootfs {
		t.Errorf("ReadonlyRootfs = false; want true")
	}
	if !slices.Contains(mock.createdHost.CapDrop, "ALL") {
		t.Errorf("CapDrop does not contain ALL: %v", mock.createdHost.CapDrop)
	}
	if !slices.Contains(mock.createdHost.SecurityOpt, "no-new-privileges") {
		t.Errorf("SecurityOpt does not contain no-new-privileges: %v", mock.createdHost.SecurityOpt)
	}
	if mock.createdHost.NetworkMode != "tilcayo-net" {
		t.Errorf("NetworkMode = %q; want tilcayo-net", mock.createdHost.NetworkMode)
	}

	// Verify Resources (general engine defaults to 1 CPU, 1 GiB)
	if mock.createdHost.Resources.Memory != 1024*1024*1024 {
		t.Errorf("Memory = %d; want %d", mock.createdHost.Resources.Memory, 1024*1024*1024)
	}
	if mock.createdHost.Resources.MemorySwap != 1024*1024*1024 {
		t.Errorf("MemorySwap = %d; want %d", mock.createdHost.Resources.MemorySwap, 1024*1024*1024)
	}
	if mock.createdHost.Resources.NanoCPUs != 1e9 {
		t.Errorf("NanoCPUs = %d; want %d", mock.createdHost.Resources.NanoCPUs, int64(1e9))
	}

	// Verify /tmp volume mount
	hasTmp := false
	for _, m := range mock.createdHost.Mounts {
		if m.Target == "/tmp" && m.Type == mount.TypeVolume {
			hasTmp = true
			break
		}
	}
	if !hasTmp {
		t.Errorf("Mounts missing /tmp anonymous volume: %v", mock.createdHost.Mounts)
	}

	// Verify ContainerStart was called
	if mock.startID != "c-1234567890ab" {
		t.Errorf("ContainerStart ID = %q; want c-1234567890ab", mock.startID)
	}
}

func TestDockerOrchestrator_Run_LocalSuccess(t *testing.T) {
	tempResults := t.TempDir()
	tempData := t.TempDir()

	cfg := &config.Config{
		ResultsMode:   "local",
		ResultsDir:    tempResults,
		DataSource:    tempData,
		DockerNetwork: "bridge",
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

	jobID := "job-local-123"
	spec := JobSpec{
		Engine: "general",
		Query:  "SELECT count(*) FROM '/data/input.csv'",
	}

	err := orch.Run(context.Background(), jobID, spec)
	if err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}

	// Verify /out and /data mounts
	hasData := false
	hasOut := false
	expectedOutDir := filepath.Join(tempResults, jobID)

	for _, m := range mock.createdHost.Mounts {
		if m.Target == "/data" {
			hasData = true
			if m.Source != tempData || !m.ReadOnly {
				t.Errorf("invalid /data mount: %+v", m)
			}
		}
		if m.Target == "/out" {
			hasOut = true
			if m.Source != expectedOutDir || m.ReadOnly {
				t.Errorf("invalid /out mount: %+v", m)
			}
		}
	}
	if !hasData {
		t.Errorf("missing /data read-only mount")
	}
	if !hasOut {
		t.Errorf("missing /out bind mount")
	}

	// Verify local directory was created on host
	if _, err := os.Stat(expectedOutDir); os.IsNotExist(err) {
		t.Errorf("results directory %s was not created", expectedOutDir)
	}

	// Verify OUTPUT_PATH in Env is /out/result.parquet
	if !slices.Contains(mock.createdConfig.Env, "OUTPUT_PATH=/out/result.parquet") {
		t.Errorf("missing OUTPUT_PATH=/out/result.parquet in env: %v", mock.createdConfig.Env)
	}
}

func TestDockerOrchestrator_Run_UnknownEngine(t *testing.T) {
	cfg := &config.Config{
		ResultsMode: "local",
		ResultsDir:  t.TempDir(),
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-1", JobSpec{Engine: "nonexistent", Query: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error for unknown engine, got nil")
	}
	if !strings.Contains(err.Error(), "unknown engine") {
		t.Errorf("error %q should mention unknown engine", err.Error())
	}
}

func TestDockerOrchestrator_Run_StartFailureCleanup(t *testing.T) {
	tempResults := t.TempDir()
	cfg := &config.Config{
		ResultsMode: "local",
		ResultsDir:  tempResults,
	}

	startErr := errors.New("daemon connection lost")
	mock := &mockDockerClient{failStart: startErr}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-1", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error when ContainerStart fails, got nil")
	}
	if !strings.Contains(err.Error(), "daemon connection lost") {
		t.Errorf("error = %v; want daemon connection lost", err)
	}

	// Verify ContainerRemove was called to clean up created container
	if mock.removeID != "c-1234567890ab" {
		t.Errorf("ContainerRemove called for %q; want c-1234567890ab", mock.removeID)
	}
	if !mock.removeOpts.Force {
		t.Errorf("ContainerRemove Force = false; want true")
	}
	if !mock.removeOpts.RemoveVolumes {
		t.Errorf("ContainerRemove RemoveVolumes = false; want true")
	}
	if mock.removeCtx == nil {
		t.Fatal("ContainerRemove called with nil context")
	}
	if _, ok := mock.removeCtx.Deadline(); !ok {
		t.Errorf("ContainerRemove context has no deadline")
	}

	// Verify job directory was removed on start failure
	jobDir := filepath.Join(tempResults, "job-1")
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("expected job directory %s to be removed, got err: %v", jobDir, err)
	}
}

func TestDockerOrchestrator_Run_CreateFailureCleansResultsDir(t *testing.T) {
	tempResults := t.TempDir()
	cfg := &config.Config{
		ResultsMode: "local",
		ResultsDir:  tempResults,
	}

	createErr := errors.New("cannot create container")
	mock := &mockDockerClient{failCreate: createErr}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-create-fail", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error when ContainerCreate fails, got nil")
	}
	if !strings.Contains(err.Error(), "cannot create container") {
		t.Errorf("error = %v; want cannot create container", err)
	}

	// Verify job directory was removed on create failure
	jobDir := filepath.Join(tempResults, "job-create-fail")
	if _, err := os.Stat(jobDir); !os.IsNotExist(err) {
		t.Errorf("expected job directory %s to be removed, got err: %v", jobDir, err)
	}
}

func TestDockerOrchestrator_Run_NonEmptyResultsDirPreservedOnFailure(t *testing.T) {
	tempResults := t.TempDir()
	cfg := &config.Config{
		ResultsMode: "local",
		ResultsDir:  tempResults,
	}

	jobID := "job-non-empty"
	jobDir := filepath.Join(tempResults, jobID)
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		t.Fatalf("failed to create jobDir: %v", err)
	}
	dummyFile := filepath.Join(jobDir, "keep-me.txt")
	if err := os.WriteFile(dummyFile, []byte("important"), 0o644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	mock := &mockDockerClient{failStart: errors.New("start failed")}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), jobID, JobSpec{Engine: "general", Query: "SELECT 1"})
	if err == nil {
		t.Fatal("expected Run error, got nil")
	}

	// Verify that the pre-existing directory and file were NOT deleted by os.Remove
	if _, err := os.Stat(dummyFile); os.IsNotExist(err) {
		t.Errorf("pre-existing file %s was deleted unexpectedly", dummyFile)
	}
}

func TestDockerOrchestrator_Run_LocalStoreFree_NoS3Env(t *testing.T) {
	tempResults := t.TempDir()

	cfg := &config.Config{
		ResultsMode:   "local",
		ResultsDir:    tempResults,
		InputPrefixes: nil, // store-free: no S3 input prefixes
		DockerNetwork: "bridge",
		S3KeyID:       "should-not-pass-key",
		S3Secret:      "should-not-pass-secret",
		S3Region:      "us-west-2",
		S3Endpoint:    "http://minio:9000",
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-storefree", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}

	for _, e := range mock.createdConfig.Env {
		if strings.HasPrefix(e, "TILCAYO_S3_") {
			t.Errorf("store-free mode container unexpectedly received S3 env var: %q", e)
		}
	}
}

func TestDockerOrchestrator_Run_LocalWithS3Inputs(t *testing.T) {
	tempResults := t.TempDir()

	cfg := &config.Config{
		ResultsMode:   "local",
		ResultsDir:    tempResults,
		InputPrefixes: []string{"s3://data-bucket/inputs/"},
		DockerNetwork: "bridge",
		S3KeyID:       "input-key",
		S3Secret:      "input-secret",
		S3Region:      "us-west-2",
		S3Endpoint:    "http://minio:9000",
		S3URLStyle:    "path",
		S3UseSSL:      true,
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-local-s3", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}

	if !slices.Contains(mock.createdConfig.Env, "TILCAYO_S3_KEY_ID=input-key") {
		t.Errorf("missing TILCAYO_S3_KEY_ID in env: %v", mock.createdConfig.Env)
	}
	if !slices.Contains(mock.createdConfig.Env, "TILCAYO_S3_SECRET=input-secret") {
		t.Errorf("missing TILCAYO_S3_SECRET in env: %v", mock.createdConfig.Env)
	}
	if !slices.Contains(mock.createdConfig.Env, "OUTPUT_PATH=/out/result.parquet") {
		t.Errorf("missing OUTPUT_PATH=/out/result.parquet in env: %v", mock.createdConfig.Env)
	}
}

func TestDockerOrchestrator_Run_InvalidResultsMode(t *testing.T) {
	cfg := &config.Config{
		ResultsMode: "unknown",
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

	err := orch.Run(context.Background(), "job-1", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err == nil {
		t.Fatal("expected error for invalid results mode, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ResultsMode") {
		t.Errorf("error %q should mention invalid ResultsMode", err.Error())
	}
}

func TestWorkerIDs(t *testing.T) {
	tests := []struct{ uid, gid, wantUID, wantGID int }{
		{1000, 1000, 1000, 1000},
		{1000, 0, 1000, 0},
		{0, 0, 65532, 65532},    // root control plane: workers never run as root
		{0, 1000, 65532, 65532}, // uid 0 is root whatever the group
	}
	for _, tt := range tests {
		if uid, gid := workerIDs(tt.uid, tt.gid); uid != tt.wantUID || gid != tt.wantGID {
			t.Errorf("workerIDs(%d, %d) = %d:%d; want %d:%d", tt.uid, tt.gid, uid, gid, tt.wantUID, tt.wantGID)
		}
	}
}

type chownCall struct {
	path     string
	uid, gid int
}

// rootLocalOrchestrator is a store-free orchestrator for a root control plane,
// with chown recorded instead of performed (the tests do not run as root).
func rootLocalOrchestrator(t *testing.T, mock *mockDockerClient, chownErr error) (*DockerOrchestrator, string, *[]chownCall) {
	t.Helper()
	results := t.TempDir()
	cfg := &config.Config{ResultsMode: "local", ResultsDir: results, DockerNetwork: "tilcayo-net", MemoryFraction: 0.5}
	orch := newDockerOrchestrator(mock, cfg, 0, 0)
	var calls []chownCall
	orch.chown = func(path string, uid, gid int) error {
		calls = append(calls, chownCall{path, uid, gid})
		return chownErr
	}
	return orch, results, &calls
}

func TestDockerOrchestrator_Run_RootHandsResultsDirToWorker(t *testing.T) {
	mock := &mockDockerClient{}
	orch, results, calls := rootLocalOrchestrator(t, mock, nil)

	if err := orch.Run(context.Background(), "job-root", JobSpec{Engine: "general", Query: "SELECT 1"}); err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}
	if mock.createdConfig.User != "65532:65532" {
		t.Errorf("User = %q; want 65532:65532 for a root control plane", mock.createdConfig.User)
	}
	want := []chownCall{{filepath.Join(results, "job-root"), 65532, 65532}}
	if !slices.Equal(*calls, want) {
		t.Errorf("chown calls = %v; want %v", *calls, want)
	}
}

func TestDockerOrchestrator_Run_NonRootDoesNotChown(t *testing.T) {
	mock := &mockDockerClient{}
	cfg := &config.Config{ResultsMode: "local", ResultsDir: t.TempDir(), DockerNetwork: "tilcayo-net", MemoryFraction: 0.5}
	orch := newDockerOrchestrator(mock, cfg, 1000, 1000)
	orch.chown = func(path string, uid, gid int) error {
		t.Errorf("chown(%q, %d, %d) called for a non-root control plane", path, uid, gid)
		return nil
	}
	if err := orch.Run(context.Background(), "job-user", JobSpec{Engine: "general", Query: "SELECT 1"}); err != nil {
		t.Fatalf("unexpected Run error: %v", err)
	}
}

func TestDockerOrchestrator_Run_ChownFailure(t *testing.T) {
	mock := &mockDockerClient{}
	orch, results, _ := rootLocalOrchestrator(t, mock, errors.New("operation not permitted"))

	err := orch.Run(context.Background(), "job-chown", JobSpec{Engine: "general", Query: "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("err = %v; want the chown error", err)
	}
	if mock.createdConfig != nil {
		t.Errorf("container created after chown failed")
	}
	if _, err := os.Stat(filepath.Join(results, "job-chown")); !os.IsNotExist(err) {
		t.Errorf("results dir not removed after chown failed: %v", err)
	}
}

func TestPreflight_RootWarns(t *testing.T) {
	shortPing(t)
	var warnings []string
	orch := newDockerOrchestrator(&mockDockerClient{}, preflightConfig(), 0, 0)
	if err := orch.Preflight(context.Background(), func(s string) { warnings = append(warnings, s) }); err != nil {
		t.Fatalf("unexpected Preflight error: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "root") || !strings.Contains(warnings[0], "65532:65532") {
		t.Errorf("warnings = %v; want one root warning naming 65532:65532", warnings)
	}
}

func TestPreflight_ImageInspectErrorWarns(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{failImage: errors.New("daemon busy")}
	var warnings []string
	if err := newDockerOrchestrator(mock, preflightConfig(), 1000, 1000).Preflight(context.Background(), func(s string) { warnings = append(warnings, s) }); err != nil {
		t.Fatalf("an inspect error must only warn, got: %v", err)
	}
	if len(warnings) != len(config.Engines()) {
		t.Fatalf("warnings = %v; want one per engine", warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "inspect failed") || !strings.Contains(w, "daemon busy") || strings.Contains(w, "not present locally") {
			t.Errorf("warning %q should report the inspect error, not a missing image", w)
		}
	}
}

func TestPreflight_ParentCancelled(t *testing.T) {
	shortPing(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newDockerOrchestrator(&mockDockerClient{pingFailures: 1 << 30}, preflightConfig(), 1000, 1000).Preflight(ctx, func(string) {})
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "cannot reach") {
		t.Errorf("err = %v; want context.Canceled, not 'cannot reach'", err)
	}
}

// preflightConfig names an image override so the test can tell images apart.
func preflightConfig() *config.Config {
	return &config.Config{
		DockerNetwork: "tilcayo-net",
		Images:        map[string]string{"general": "worker-general:test"},
	}
}

func shortPing(t *testing.T) {
	t.Helper()
	oldTimeout, oldInterval := pingTimeout, pingRetryInterval
	pingTimeout, pingRetryInterval = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { pingTimeout, pingRetryInterval = oldTimeout, oldInterval })
}

func TestPreflight_OK(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{pingFailures: 2} // daemon still starting
	var warnings []string
	err := NewDockerOrchestrator(mock, preflightConfig()).Preflight(context.Background(), func(s string) { warnings = append(warnings, s) })
	if err != nil {
		t.Fatalf("unexpected Preflight error: %v", err)
	}
	if mock.pingCalls != 3 {
		t.Errorf("Ping called %d times; want 3 (two failures, then success)", mock.pingCalls)
	}
	if len(mock.inspected) != len(config.Engines()) {
		t.Errorf("inspected %v; want one image per engine %v", mock.inspected, config.Engines())
	}
	if os.Getuid() != 0 && len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
}

func TestPreflight_DaemonUnreachable(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{pingFailures: 1 << 30}
	err := NewDockerOrchestrator(mock, preflightConfig()).Preflight(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "cannot reach the Docker daemon") {
		t.Fatalf("err = %v; want cannot reach the Docker daemon", err)
	}
	if mock.pingCalls < 2 {
		t.Errorf("Ping called %d times; want retries", mock.pingCalls)
	}
}

func TestPreflight_NetworkMissing(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{failNetwork: fmt.Errorf("network tilcayo-net not found: %w", cerrdefs.ErrNotFound)}
	err := NewDockerOrchestrator(mock, preflightConfig()).Preflight(context.Background(), func(string) {})
	if err == nil {
		t.Fatal("expected an error for a missing network, got nil")
	}
	for _, want := range []string{"TILCAYO_DOCKER_NETWORK", "does not exist", "docker network create tilcayo-net"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	if mock.pingCalls != 1 {
		t.Errorf("Ping called %d times; a missing network must not be retried", mock.pingCalls)
	}
}

func TestPreflight_NetworkInspectError(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{failNetwork: errors.New("permission denied")}
	err := NewDockerOrchestrator(mock, preflightConfig()).Preflight(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v; want the inspect error, not 'does not exist'", err)
	}
}

func TestPreflight_MissingImageWarns(t *testing.T) {
	shortPing(t)
	mock := &mockDockerClient{missingImages: map[string]bool{"worker-general:test": true}}
	var warnings []string
	err := NewDockerOrchestrator(mock, preflightConfig()).Preflight(context.Background(), func(s string) { warnings = append(warnings, s) })
	if err != nil {
		t.Fatalf("a missing image must only warn, got error: %v", err)
	}
	var imageWarnings []string
	for _, w := range warnings {
		if strings.Contains(w, "image") {
			imageWarnings = append(imageWarnings, w)
		}
	}
	if len(imageWarnings) != 1 {
		t.Fatalf("image warnings = %v; want exactly one", imageWarnings)
	}
	for _, want := range []string{"worker-general:test", `"general"`, "docker pull worker-general:test", "TILCAYO_IMAGE_GENERAL"} {
		if !strings.Contains(imageWarnings[0], want) {
			t.Errorf("warning %q should contain %q", imageWarnings[0], want)
		}
	}
}
