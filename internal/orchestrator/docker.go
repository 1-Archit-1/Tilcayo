package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

// DockerClient abstracts the methods of *client.Client used by DockerOrchestrator.
type DockerClient interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
}

// DockerOrchestrator implements Orchestrator by running workers as Docker containers.
type DockerOrchestrator struct {
	cli DockerClient
	cfg *config.Config
}

// NewDockerOrchestrator creates a new DockerOrchestrator.
func NewDockerOrchestrator(cli DockerClient, cfg *config.Config) *DockerOrchestrator {
	return &DockerOrchestrator{
		cli: cli,
		cfg: cfg,
	}
}

// Run creates and starts an ephemeral worker container for the specified job.
func (d *DockerOrchestrator) Run(ctx context.Context, jobID string, spec JobSpec) error {

	image, err := d.cfg.ImageFor(spec.Engine)
	if err != nil {
		return fmt.Errorf("resolve image: %w", err)
	}

	resources, err := d.cfg.ResourcesFor(spec.Engine)
	if err != nil {
		return fmt.Errorf("resolve resources: %w", err)
	}

	var outputPath string
	switch d.cfg.ResultsMode {
	case config.ResultsS3:
		outputPath = d.cfg.OutputPrefix + jobID + "/result.parquet"
	case config.ResultsLocal:
		outputPath = "/out/result.parquet"
	default:
		return fmt.Errorf("invalid ResultsMode: %q", d.cfg.ResultsMode)
	}

	env := []string{
		"QUERY=" + spec.Query,
		"OUTPUT_PATH=" + outputPath,
		"TILCAYO_ALLOWED_INPUTS=" + strings.Join(d.cfg.AllowedInputs(), ","),
		"TILCAYO_MEMORY_FRACTION=0.5",
	}

	if d.cfg.ResultsMode == config.ResultsS3 || len(d.cfg.InputPrefixes) > 0 {
		env = append(env,
			"TILCAYO_S3_KEY_ID="+string(d.cfg.S3KeyID),
			"TILCAYO_S3_SECRET="+string(d.cfg.S3Secret),
			"TILCAYO_S3_REGION="+d.cfg.S3Region,
			"TILCAYO_S3_ENDPOINT="+d.cfg.S3Endpoint,
			"TILCAYO_S3_URL_STYLE="+d.cfg.S3URLStyle,
			"TILCAYO_S3_USE_SSL="+fmt.Sprintf("%t", d.cfg.S3UseSSL),
		)
	}

	// Ephemeral volume mount for DuckDB disk spill at /tmp.
	mounts := []mount.Mount{
		{
			Type:   mount.TypeVolume,
			Target: "/tmp",
		},
	}
	if d.cfg.DataSource != "" {
		mounts = append(mounts, mount.Mount{
			Type:     mount.TypeBind,
			Source:   d.cfg.DataSource,
			Target:   "/data",
			ReadOnly: true,
		})
	}

	var outDir string
	if d.cfg.ResultsMode == config.ResultsLocal {
		outDir = filepath.Join(d.cfg.ResultsDir, jobID)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return fmt.Errorf("create results dir: %w", err)
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: outDir,
			Target: "/out",
		})
	}

	containerCfg := &container.Config{
		Image: image,
		User:  fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Env:   env,
		Labels: map[string]string{
			"tilcayo.io/job-id":            jobID,
			"app.kubernetes.io/managed-by": "tilcayo",
		},
	}

	hostCfg := &container.HostConfig{
		ReadonlyRootfs: true,
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges"},
		Resources: container.Resources{
			NanoCPUs:   resources.NanoCPUs(),
			Memory:     resources.MemoryBytes,
			MemorySwap: resources.MemoryBytes,
		},
		NetworkMode: container.NetworkMode(d.cfg.DockerNetwork),
		Mounts:      mounts,
	}

	resp, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     containerCfg,
		HostConfig: hostCfg,
		Name:       "tilcayo-" + jobID,
	})
	if err != nil {
		if outDir != "" {
			_ = os.Remove(outDir)
		}
		return fmt.Errorf("create container: %w", err)
	}

	if _, err := d.cli.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = d.cli.ContainerRemove(cleanCtx, resp.ID, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if outDir != "" {
			_ = os.Remove(outDir)
		}
		return fmt.Errorf("start container: %w", err)
	}
	return nil
}

// Events returns a channel of lifecycle events emitted by worker containers.
func (d *DockerOrchestrator) Events(ctx context.Context) (<-chan JobEvent, error) {
	return nil, fmt.Errorf("Events: not implemented yet (Item 8)")
}

// Cancel terminates the worker container for the specified job.
func (d *DockerOrchestrator) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("Cancel: not implemented yet")
}
