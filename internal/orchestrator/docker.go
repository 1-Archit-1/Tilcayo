package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
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

	// Used by Preflight only.
	Ping(ctx context.Context, options client.PingOptions) (client.PingResult, error)
	NetworkInspect(ctx context.Context, networkID string, options client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	ImageInspect(ctx context.Context, imageID string, options ...client.ImageInspectOption) (client.ImageInspectResult, error)
}

// The non-root uid:gid every worker image sets with USER.
const (
	imageDefaultUID = 65532
	imageDefaultGID = 65532
)

// How long Preflight waits for the daemon to answer, and for each inspect
// call after that. Variables so tests can shorten them.
var (
	pingTimeout       = 15 * time.Second
	pingRetryInterval = time.Second
	inspectTimeout    = 10 * time.Second
)

// DockerOrchestrator implements Orchestrator by running workers as Docker containers.
type DockerOrchestrator struct {
	cli          DockerClient
	cfg          *config.Config
	uid, gid     int                                   // workers run as uid:gid, from workerIDs
	rootFallback bool                                  // the control plane is root, so workers use the image default
	chown        func(name string, uid, gid int) error // os.Chown; replaced in tests
}

// NewDockerOrchestrator creates a new DockerOrchestrator for a control plane
// running as the current process's uid:gid.
func NewDockerOrchestrator(cli DockerClient, cfg *config.Config) *DockerOrchestrator {
	return newDockerOrchestrator(cli, cfg, os.Getuid(), os.Getgid())
}

// newDockerOrchestrator takes the control plane's uid:gid, so tests can
// exercise the root fallback without running as root.
func newDockerOrchestrator(cli DockerClient, cfg *config.Config, cpUID, cpGID int) *DockerOrchestrator {
	uid, gid := workerIDs(cpUID, cpGID)
	return &DockerOrchestrator{
		cli:          cli,
		cfg:          cfg,
		uid:          uid,
		gid:          gid,
		rootFallback: cpUID == 0,
		chown:        os.Chown,
	}
}

// workerIDs returns the uid:gid workers run as: the control plane's own, so
// store-free results belong to the operator (issue #18). A root control plane
// gets the image default instead, so workers never run as root: Docker has no
// equivalent of Kubernetes' runAsNonRoot to catch it.
func workerIDs(uid, gid int) (int, int) {
	if uid == 0 {
		return imageDefaultUID, imageDefaultGID
	}
	return uid, gid
}

// user is the container User string.
func (d *DockerOrchestrator) user() string {
	return fmt.Sprintf("%d:%d", d.uid, d.gid)
}

// Preflight checks the daemon, the worker network and the worker images
// before the API accepts jobs. It returns an error for a problem every job
// would hit (no daemon, no network), and calls warn for problems that only
// some jobs hit (a missing image) or that the operator should know about.
func (d *DockerOrchestrator) Preflight(ctx context.Context, warn func(string)) error {
	if err := d.waitForDaemon(ctx); err != nil {
		return err
	}

	network := d.cfg.DockerNetwork
	netCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
	_, err := d.cli.NetworkInspect(netCtx, network, client.NetworkInspectOptions{})
	cancel()
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("%s=%q: network does not exist; create it with 'docker network create %s'",
				config.EnvDockerNetwork, network, network)
		}
		return fmt.Errorf("%s=%q: inspect network: %w", config.EnvDockerNetwork, network, err)
	}

	if d.rootFallback {
		warn(fmt.Sprintf("the control plane runs as root, so workers run as the image default %s; files under %s must be readable by that uid, and each job's results dir is handed to it",
			d.user(), config.EnvDataSource))
	}

	// Pulling is not done here or in Run: a missing image only fails jobs
	// for its engine, so it is a warning, not a reason to refuse to start.
	for _, engine := range config.Engines() {
		image, err := d.cfg.ImageFor(engine)
		if err != nil {
			return err
		}
		imgCtx, cancel := context.WithTimeout(ctx, inspectTimeout)
		_, err = d.cli.ImageInspect(imgCtx, image)
		cancel()
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				warn(fmt.Sprintf("image %q for engine %q is not present locally; %s jobs fail until you run 'docker pull %s' or set %s%s",
					image, engine, engine, image, config.EnvImagePrefix, strings.ToUpper(engine)))
			} else {
				warn(fmt.Sprintf("image %q for engine %q: inspect failed, so %s jobs may fail: %v", image, engine, engine, err))
			}
		}
	}
	return nil
}

// waitForDaemon pings the daemon until it answers or pingTimeout passes, so
// a daemon that is still starting does not stop the control plane. The first
// successful ping also negotiates the API version.
func (d *DockerOrchestrator) waitForDaemon(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, pingTimeout)
	defer cancel()
	for {
		_, err := d.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return fmt.Errorf("waiting for the Docker daemon: %w", parent.Err())
			}
			return fmt.Errorf("cannot reach the Docker daemon after %s (check DOCKER_HOST and that the daemon is running): %w", pingTimeout, err)
		case <-time.After(pingRetryInterval):
		}
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
		"TILCAYO_MEMORY_FRACTION=" + strconv.FormatFloat(d.cfg.MemoryFraction, 'g', -1, 64),
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
		// A root control plane creates the dir as root:root, which the
		// 65532 worker cannot write to. Hand only this job's dir over; root
		// can still serve and delete what the worker writes.
		if d.rootFallback {
			if err := d.chown(outDir, d.uid, d.gid); err != nil {
				_ = os.Remove(outDir)
				return fmt.Errorf("hand results dir to worker %s: %w", d.user(), err)
			}
		}
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: outDir,
			Target: "/out",
		})
	}

	containerCfg := &container.Config{
		Image: image,
		User:  d.user(),
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
