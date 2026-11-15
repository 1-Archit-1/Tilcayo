package orchestrator

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"

	"github.com/1-Archit-1/Tilcayo/internal/config"
)

// makeFramedLog builds a multiplexed Docker log stream using stdcopy headers.
// Header format: [8]byte{STREAM_TYPE, 0, 0, 0, SIZE_B1, SIZE_B2, SIZE_B3, SIZE_B4}
func makeFramedLog(streamType stdcopy.StdType, payload []byte) []byte {
	hdr := make([]byte, 8)
	hdr[0] = byte(streamType)
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(payload)))
	return append(hdr, payload...)
}

func TestDeframeAndCapLogs(t *testing.T) {
	t.Run("demultiplexes stderr sentinel", func(t *testing.T) {
		var buf bytes.Buffer
		buf.Write(makeFramedLog(stdcopy.Stdout, []byte("normal stdout output\n")))
		buf.Write(makeFramedLog(stdcopy.Stderr, []byte("warning on stderr\ntilcayo-error: custom failure\\nline 2\n")))

		got, err := deframeAndCapLogs(&buf)
		if err != nil {
			t.Fatalf("deframeAndCapLogs failed: %v", err)
		}
		if !strings.Contains(got, "tilcayo-error: custom failure\\nline 2") {
			t.Errorf("deframeAndCapLogs() = %q, want sentinel line", got)
		}
	})

	t.Run("caps to 80 lines and 2048 bytes", func(t *testing.T) {
		var buf bytes.Buffer
		var bigLog strings.Builder
		for i := 0; i < 200; i++ {
			bigLog.WriteString(fmt.Sprintf("log line %03d: very long log message with padding 1234567890abcdefghijklmnopqrstuvwxyz\n", i))
		}
		buf.Write(makeFramedLog(stdcopy.Stderr, []byte(bigLog.String())))

		got, err := deframeAndCapLogs(&buf)
		if err != nil {
			t.Fatalf("deframeAndCapLogs failed: %v", err)
		}

		lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
		if len(lines) > 80 {
			t.Errorf("len(lines) = %d, want <= 80", len(lines))
		}
		if len(got) > 2048 {
			t.Errorf("len(got) = %d bytes, want <= 2048", len(got))
		}
		// Preserves the tail of the log
		if !strings.Contains(got, "log line 199") {
			t.Errorf("got log tail did not include latest line 199: %q", got)
		}
	})
}

func TestCapLogTail(t *testing.T) {
	t.Run("trailing newline does not cost a line", func(t *testing.T) {
		var b strings.Builder
		for i := range 100 {
			fmt.Fprintf(&b, "l%d\n", i)
		}
		got := capLogTail(b.String(), 2048, 80)
		lines := strings.Split(got, "\n")
		if len(lines) != 80 {
			t.Fatalf("kept %d lines, want 80: %q", len(lines), got)
		}
		if lines[0] != "l20" || lines[79] != "l99" {
			t.Errorf("kept lines %q..%q, want l20..l99", lines[0], lines[79])
		}
	})

	t.Run("byte cut keeps valid UTF-8", func(t *testing.T) {
		// "é" is 2 bytes; with a 1-byte suffix, the last 2048 bytes start
		// in the middle of one.
		s := strings.Repeat("é", 1500) + "x"
		got := capLogTail(s, 2048, 80)
		if !utf8.ValidString(got) {
			t.Errorf("tail is not valid UTF-8: %q", got[:8])
		}
		if len(got) > 2048 {
			t.Errorf("len = %d bytes, want <= 2048", len(got))
		}
		if len(got) != 2047 {
			t.Errorf("len = %d bytes, want 2047 (one partial rune dropped)", len(got))
		}
	})
}

func TestEvents_StartupSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	createdTime := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	mock := &mockDockerClient{
		listItems: []container.Summary{
			{
				ID: "cid-running",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "tilcayo",
					"tilcayo.io/job-id":            "job-snap-1",
					"tilcayo.io/engine":            "spatial",
				},
				Created: createdTime.Unix(),
			},
			{
				ID: "cid-exited",
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "tilcayo",
					"tilcayo.io/job-id":            "job-snap-2",
					"tilcayo.io/engine":            "ml",
				},
				Created: createdTime.Unix(),
			},
		},
		inspectResp: map[string]client.ContainerInspectResult{
			"cid-running": {
				Container: container.InspectResponse{
					State: &container.State{
						Running: true,
					},
				},
			},
			"cid-exited": {
				Container: container.InspectResponse{
					State: &container.State{
						Status:     "exited",
						ExitCode:   0,
						FinishedAt: "2026-11-08T12:05:00Z",
					},
				},
			},
		},
		eventsMsgCh: make(chan events.Message),
		eventsErrCh: make(chan error),
	}

	orch := NewDockerOrchestrator(mock, &config.Config{DockerNetwork: "test-net"})
	ch, err := orch.Events(ctx)
	if err != nil {
		t.Fatalf("orch.Events failed: %v", err)
	}

	// Read snapshot events
	ev1 := <-ch
	if ev1.JobID != "job-snap-1" || ev1.Engine != "spatial" || ev1.State != StateRunning {
		t.Fatalf("ev1 = %+v, want job-snap-1, spatial, StateRunning", ev1)
	}
	if !ev1.CreatedAt.Equal(createdTime) {
		t.Errorf("ev1.CreatedAt = %v, want %v", ev1.CreatedAt, createdTime)
	}

	ev2 := <-ch
	if ev2.JobID != "job-snap-2" || ev2.Engine != "ml" || ev2.State != StateSucceeded || ev2.ExitCode != 0 {
		t.Fatalf("ev2 = %+v, want job-snap-2, ml, StateSucceeded, ExitCode 0", ev2)
	}
}

func TestEvents_LiveStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	msgCh := make(chan events.Message, 5)
	errCh := make(chan error)

	var loggedTail string
	var loggedJob string

	var framedErr bytes.Buffer
	framedErr.Write(makeFramedLog(stdcopy.Stderr, []byte("fatal error\ntilcayo-error: out of bounds\\nindex 42\n")))

	mock := &mockDockerClient{
		eventsMsgCh: msgCh,
		eventsErrCh: errCh,
		inspectResp: map[string]client.ContainerInspectResult{
			"cid-die": {
				Container: container.InspectResponse{
					Created: "2026-11-08T10:00:00Z",
					State: &container.State{
						Status:     "exited",
						ExitCode:   1,
						OOMKilled:  false,
						FinishedAt: "2026-11-08T10:02:00Z",
					},
				},
			},
			"cid-oom": {
				Container: container.InspectResponse{
					Created: "2026-11-08T10:00:00Z",
					State: &container.State{
						Status:     "exited",
						ExitCode:   1, // Nonzero exit code with OOMKilled=true (Decision b)
						OOMKilled:  true,
						FinishedAt: "2026-11-08T10:03:00Z",
					},
				},
			},
			"cid-crash": {
				Container: container.InspectResponse{
					Created: "2026-11-08T10:00:00Z",
					State: &container.State{
						Status:     "exited",
						ExitCode:   139,
						OOMKilled:  false,
						FinishedAt: "2026-11-08T10:04:00Z",
					},
				},
			},
		},
		logsResp: map[string]io.ReadCloser{
			"cid-die":   io.NopCloser(&framedErr),
			"cid-oom":   io.NopCloser(strings.NewReader("")),
			"cid-crash": io.NopCloser(bytes.NewReader(makeFramedLog(stdcopy.Stderr, []byte("segmentation fault (core dumped)\n")))),
		},
	}

	orch := NewDockerOrchestrator(mock, &config.Config{DockerNetwork: "test-net"})
	orch.logRaw = func(jobID, errMsg, tail string) {
		loggedJob = jobID
		loggedTail = tail
	}

	ch, err := orch.Events(ctx)
	if err != nil {
		t.Fatalf("orch.Events failed: %v", err)
	}

	// 1. Live "start" event
	msgCh <- events.Message{
		Action: "start",
		Actor: events.Actor{
			ID: "cid-live-1",
			Attributes: map[string]string{
				"app.kubernetes.io/managed-by": "tilcayo",
				"tilcayo.io/job-id":            "job-live-1",
				"tilcayo.io/engine":            "general",
			},
		},
	}

	evStart := <-ch
	if evStart.JobID != "job-live-1" || evStart.Engine != "general" || evStart.State != StateRunning {
		t.Fatalf("evStart = %+v, want job-live-1, general, StateRunning", evStart)
	}

	// 2. Live "die" event with sentinel error
	msgCh <- events.Message{
		Action: "die",
		Actor: events.Actor{
			ID: "cid-die",
			Attributes: map[string]string{
				"app.kubernetes.io/managed-by": "tilcayo",
				"tilcayo.io/job-id":            "job-die-sentinel",
				"tilcayo.io/engine":            "general",
			},
		},
	}

	evDie := <-ch
	if evDie.JobID != "job-die-sentinel" || evDie.State != StateFailed || evDie.ExitCode != 1 {
		t.Fatalf("evDie = %+v, want StateFailed with ExitCode 1", evDie)
	}
	if evDie.Error != "out of bounds\nindex 42" {
		t.Errorf("evDie.Error = %q, want unescaped sentinel", evDie.Error)
	}

	// 3. Live "die" event with OOMKilled=true on exit 1 (Decision b)
	msgCh <- events.Message{
		Action: "die",
		Actor: events.Actor{
			ID: "cid-oom",
			Attributes: map[string]string{
				"app.kubernetes.io/managed-by": "tilcayo",
				"tilcayo.io/job-id":            "job-oom",
				"tilcayo.io/engine":            "ml",
			},
		},
	}

	evOOM := <-ch
	if evOOM.State != StateFailed || evOOM.Error != "out of memory" {
		t.Fatalf("evOOM = %+v, want StateFailed, 'out of memory'", evOOM)
	}

	// 4. Live "die" event with crash (exit 139) logs raw tail (Decision a)
	msgCh <- events.Message{
		Action: "die",
		Actor: events.Actor{
			ID: "cid-crash",
			Attributes: map[string]string{
				"app.kubernetes.io/managed-by": "tilcayo",
				"tilcayo.io/job-id":            "job-crash-139",
				"tilcayo.io/engine":            "spatial",
			},
		},
	}

	evCrash := <-ch
	if evCrash.State != StateFailed || evCrash.Error != "worker crashed (exit 139)" {
		t.Fatalf("evCrash = %+v, want StateFailed, worker crashed (exit 139)", evCrash)
	}
	if loggedJob != "job-crash-139" || !strings.Contains(loggedTail, "segmentation fault") {
		t.Errorf("raw tail was not logged via test seam! loggedJob=%q, loggedTail=%q", loggedJob, loggedTail)
	}

	// 5. Live "destroy" event emits Deleted: true (Decision d)
	msgCh <- events.Message{
		Action: "destroy",
		Actor: events.Actor{
			ID: "cid-die",
			Attributes: map[string]string{
				"app.kubernetes.io/managed-by": "tilcayo",
				"tilcayo.io/job-id":            "job-die-sentinel",
				"tilcayo.io/engine":            "general",
			},
		},
	}

	evDestroy := <-ch
	if evDestroy.JobID != "job-die-sentinel" || !evDestroy.Deleted {
		t.Fatalf("evDestroy = %+v, want JobID=job-die-sentinel, Deleted=true", evDestroy)
	}

	// 6. Context cancellation cleanly closes channel (Carry-in 8)
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Errorf("channel was not closed on ctx cancellation")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("timed out waiting for Events channel to close on cancellation")
	}
}

func TestRun_EngineLabel(t *testing.T) {
	mock := &mockDockerClient{}
	cfg := &config.Config{
		DockerNetwork: "test-net",
		ResultsMode:   config.ResultsLocal,
		ResultsDir:    t.TempDir(),
	}
	orch := newDockerOrchestrator(mock, cfg, 1000, 1000)

	err := orch.Run(context.Background(), "job-label-test", JobSpec{
		Engine: "spatial",
		Query:  "SELECT 1",
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if mock.createdConfig == nil {
		t.Fatalf("ContainerCreate was not called")
	}
	labels := mock.createdConfig.Labels
	if labels["tilcayo.io/engine"] != "spatial" {
		t.Errorf("container label tilcayo.io/engine = %q, want 'spatial'", labels["tilcayo.io/engine"])
	}
	if labels["tilcayo.io/job-id"] != "job-label-test" {
		t.Errorf("container label tilcayo.io/job-id = %q, want 'job-label-test'", labels["tilcayo.io/job-id"])
	}
}
