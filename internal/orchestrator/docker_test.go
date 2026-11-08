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

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

type mockDockerClient struct {
	createdName   string
	createdConfig *container.Config
	createdHost   *container.HostConfig
	startID       string
	removeID      string
	removeOpts    container.RemoveOptions
	failCreate    error
	failStart     error
}

func (m *mockDockerClient) ContainerCreate(ctx context.Context, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error) {
	if m.failCreate != nil {
		return container.CreateResponse{}, m.failCreate
	}
	m.createdName = containerName
	m.createdConfig = cfg
	m.createdHost = hostCfg
	return container.CreateResponse{ID: "c-1234567890ab"}, nil
}

func (m *mockDockerClient) ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error {
	m.startID = containerID
	return m.failStart
}

func (m *mockDockerClient) ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error {
	m.removeID = containerID
	m.removeOpts = options
	return nil
}

func TestDockerOrchestrator_Run_S3Success(t *testing.T) {
	cfg := &config.Config{
		ResultsMode:   "s3",
		OutputPrefix:  "s3://my-bucket/results/",
		InputPrefixes: []string{"s3://input-bucket/data/"},
		DockerNetwork: "tilcayo-net",
		S3KeyID:       "test-key",
		S3Secret:      "test-secret",
		S3Region:      "us-east-1",
		S3Endpoint:    "http://minio:9000",
		S3URLStyle:    "path",
		S3UseSSL:      false,
	}

	mock := &mockDockerClient{}
	orch := NewDockerOrchestrator(mock, cfg)

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
	expectedUser := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	if mock.createdConfig.User != expectedUser {
		t.Errorf("User = %q; want %q", mock.createdConfig.User, expectedUser)
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
		"TILCAYO_MEMORY_FRACTION": "0.5",
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
	cfg := &config.Config{
		ResultsMode: "local",
		ResultsDir:  t.TempDir(),
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
