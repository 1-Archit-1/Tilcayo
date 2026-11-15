package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

// DockerClient abstracts the methods of *client.Client used by DockerOrchestrator.
type DockerClient interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	Events(ctx context.Context, options client.EventsListOptions) client.EventsResult

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
	logRaw       func(jobID, errMsg, tail string)      // test seam: logs raw tail on crash / failed without message
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
			"tilcayo.io/engine":            spec.Engine,
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

// deframeAndCapLogs demultiplexes non-TTY container logs using stdcopy.StdCopy
// and returns the capped log tail (at most 2048 bytes and 80 lines).
func deframeAndCapLogs(r io.Reader) (string, error) {
	var stdoutBuf, stderrBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdoutBuf, &stderrBuf, r); err != nil {
		return "", fmt.Errorf("deframe logs: %w", err)
	}
	text := stderrBuf.String()
	if text == "" {
		text = stdoutBuf.String()
	}
	return capLogTail(text, 2048, 80), nil
}

// capLogTail caps a log string to maxLines lines and maxBytes bytes.
func capLogTail(s string, maxBytes, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > maxBytes {
		tail = tail[len(tail)-maxBytes:]
	}
	return tail
}

func (d *DockerOrchestrator) logRawTail(jobID, errMsg, tail string) {
	if d.logRaw != nil {
		d.logRaw(jobID, errMsg, tail)
		return
	}
	fmt.Fprintf(os.Stderr, "tilcayo: job %s %s; log tail:\n%s\n", jobID, errMsg, tail)
}

func (d *DockerOrchestrator) fetchLogTail(ctx context.Context, containerID string) string {
	res, err := d.cli.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       "80",
	})
	if err != nil {
		return ""
	}
	defer res.Close()

	tail, err := deframeAndCapLogs(res)
	if err != nil {
		return ""
	}
	return tail
}

// Events returns a channel of lifecycle events emitted by worker containers.
// It first snapshots existing Tilcayo containers to rebuild state, then streams live events.
// Cancelling ctx terminates the background watch goroutine and closes the channel.
func (d *DockerOrchestrator) Events(ctx context.Context) (<-chan JobEvent, error) {
	out := make(chan JobEvent)
	go d.watchEvents(ctx, out)
	return out, nil
}

// watchEvents runs in a background goroutine for the lifetime of ctx.
// On each connection cycle it subscribes to live Docker events first, snapshots
// existing Tilcayo containers so no transitions are missed in the gap, and then
// streams live events until the stream breaks or ctx is cancelled.
func (d *DockerOrchestrator) watchEvents(ctx context.Context, out chan<- JobEvent) {
	defer close(out)

	eventFilter := client.Filters{}.
		Add("type", "container").
		Add("label", "app.kubernetes.io/managed-by=tilcayo")

	for ctx.Err() == nil {
		if !d.runEventCycle(ctx, out, eventFilter) {
			return
		}

		// Brief pause before reconnecting if the stream closed or errored.
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// runEventCycle executes one subscribe -> snapshot -> stream cycle with a
// per-iteration child context so the Docker SDK's HTTP stream is always closed
// when the cycle ends. Returns false if the parent ctx was cancelled.
func (d *DockerOrchestrator) runEventCycle(ctx context.Context, out chan<- JobEvent, filter client.Filters) bool {
	iterCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Subscribe before snapshotting so events firing during ContainerList/Inspect
	// are buffered in the stream rather than lost in the gap.
	res := d.cli.Events(iterCtx, client.EventsListOptions{Filters: filter})

	if !d.emitSnapshot(iterCtx, out) {
		return false
	}

	return d.consumeEventStream(iterCtx, out, res.Messages, res.Err)
}

// emitEvent sends ev on out unless ctx is cancelled first.
// Returning false signals the caller to shut down.
func emitEvent(ctx context.Context, out chan<- JobEvent, ev JobEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- ev:
		return true
	}
}

// buildExitEvent inspects a container's exit state and logs to produce a terminal JobEvent.
// Shared by both the startup snapshot (for already-exited containers) and live "die" events.
func (d *DockerOrchestrator) buildExitEvent(
	ctx context.Context,
	containerID, jobID, engine string,
	createdAt time.Time,
	st *container.State,
) JobEvent {
	var finishedAt time.Time
	if t, err := time.Parse(time.RFC3339Nano, st.FinishedAt); err == nil {
		finishedAt = t
	}

	tail := d.fetchLogTail(ctx, containerID)
	state, errMsg, logRaw := Classify(st.ExitCode, st.OOMKilled, tail)
	if logRaw {
		d.logRawTail(jobID, errMsg, tail)
	}

	return JobEvent{
		JobID:      jobID,
		Engine:     engine,
		CreatedAt:  createdAt,
		State:      state,
		ExitCode:   st.ExitCode,
		Error:      errMsg,
		FinishedAt: finishedAt,
	}
}

// emitSnapshot lists all existing Tilcayo containers and emits their current state
// so the cache can recover running or finished jobs after a control-plane restart.
func (d *DockerOrchestrator) emitSnapshot(ctx context.Context, out chan<- JobEvent) bool {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "app.kubernetes.io/managed-by=tilcayo"),
	})
	if err != nil {
		// Snapshot is best-effort; proceed to live streaming if listing fails.
		return ctx.Err() == nil
	}

	for _, c := range list.Items {
		if c.Labels["app.kubernetes.io/managed-by"] != "tilcayo" {
			continue
		}
		jobID := c.Labels["tilcayo.io/job-id"]
		if jobID == "" {
			continue
		}
		engine := c.Labels["tilcayo.io/engine"]
		createdAt := time.Unix(c.Created, 0).UTC()

		insp, err := d.cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			continue
		}

		var ev JobEvent
		switch {
		case insp.Container.State.Running:
			ev = JobEvent{
				JobID:     jobID,
				Engine:    engine,
				CreatedAt: createdAt,
				State:     StateRunning,
			}
		case insp.Container.State.Status == "exited" || insp.Container.State.ExitCode != 0:
			ev = d.buildExitEvent(ctx, c.ID, jobID, engine, createdAt, insp.Container.State)
		default:
			ev = JobEvent{
				JobID:     jobID,
				Engine:    engine,
				CreatedAt: createdAt,
				State:     StatePending,
			}
		}

		if !emitEvent(ctx, out, ev) {
			return false
		}
	}
	return true
}

// consumeEventStream reads live Docker events until the stream errors, closes,
// or ctx is cancelled. Returns false if ctx was cancelled, true to reconnect.
func (d *DockerOrchestrator) consumeEventStream(
	ctx context.Context,
	out chan<- JobEvent,
	msgs <-chan events.Message,
	errs <-chan error,
) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-errs:
			return true
		case msg, ok := <-msgs:
			if !ok {
				return true
			}
			if !d.handleDockerEvent(ctx, out, msg) {
				return false
			}
		}
	}
}

// handleDockerEvent translates a single Docker container event into a JobEvent.
// Returns false if ctx was cancelled while sending.
func (d *DockerOrchestrator) handleDockerEvent(ctx context.Context, out chan<- JobEvent, msg events.Message) bool {
	if msg.Actor.Attributes["app.kubernetes.io/managed-by"] != "tilcayo" {
		return true
	}
	jobID := msg.Actor.Attributes["tilcayo.io/job-id"]
	if jobID == "" {
		return true
	}
	engine := msg.Actor.Attributes["tilcayo.io/engine"]

	switch msg.Action {
	case "start":
		return emitEvent(ctx, out, JobEvent{
			JobID:  jobID,
			Engine: engine,
			State:  StateRunning,
		})

	case "die":
		insp, err := d.cli.ContainerInspect(ctx, msg.Actor.ID, client.ContainerInspectOptions{})
		if err != nil {
			return true
		}
		var createdAt time.Time
		if t, err := time.Parse(time.RFC3339Nano, insp.Container.Created); err == nil {
			createdAt = t
		}
		ev := d.buildExitEvent(ctx, msg.Actor.ID, jobID, engine, createdAt, insp.Container.State)
		return emitEvent(ctx, out, ev)

	case "destroy":
		return emitEvent(ctx, out, JobEvent{
			JobID:   jobID,
			Deleted: true,
		})
	}
	return true
}

// Cancel terminates the worker container for the specified job.
func (d *DockerOrchestrator) Cancel(ctx context.Context, jobID string) error {
	return fmt.Errorf("Cancel: not implemented yet")
}
