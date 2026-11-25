package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestNewDockerValidatesConfigEnvAndRuntime(t *testing.T) {
	if _, err := NewDocker(t.Context(), config.Runner{}, io.Discard); err == nil || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewDocker(t.Context(), config.Runner{Docker: &config.Docker{}}, io.Discard); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("error = %v", err)
	}

	installFakeDocker(t, `{"runc":{"path":"runc"}}`, "")
	t.Setenv("MACHINIST_DOCKER_MISSING_KEY", "")
	if _, err := NewDocker(t.Context(), config.Runner{
		Docker: &config.Docker{
			Image: "ghcr.io/owainlewis/machinist-runner:latest",
			Env:   []string{"MACHINIST_DOCKER_MISSING_KEY"},
		},
	}, io.Discard); err == nil || !strings.Contains(err.Error(), "MACHINIST_DOCKER_MISSING_KEY") {
		t.Fatalf("error = %v", err)
	}

	// Missing runsc runtime fails clearly.
	if _, err := NewDocker(t.Context(), config.Runner{
		Docker: &config.Docker{
			Image:   "ghcr.io/owainlewis/machinist-runner:latest",
			Runtime: "runsc",
		},
	}, io.Discard); err == nil || !strings.Contains(err.Error(), `"runsc" is not installed`) {
		t.Fatalf("error = %v", err)
	}

	// Installed runsc succeeds cleanly.
	installFakeDocker(t, `{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`, "claude,codex")
	t.Setenv("MACHINIST_DOCKER_MISSING_KEY", "present")
	var log bytes.Buffer
	d, err := NewDocker(t.Context(), config.Runner{
		DataDirectory: t.TempDir(),
		Docker: &config.Docker{
			Image:   "ghcr.io/owainlewis/machinist-runner:latest",
			Runtime: "runsc",
			Env:     []string{"MACHINIST_DOCKER_MISSING_KEY"},
		},
	}, &log)
	if err != nil {
		t.Fatal(err)
	}
	harnesses, err := d.ProbeHarnesses(t.Context())
	if err != nil || strings.Join(harnesses, ",") != "claude,codex" {
		t.Fatalf("harnesses = %v, err = %v", harnesses, err)
	}
}

func TestDaemonReportsDockerKindAndHarnesses(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("runner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	installFakeDocker(t, `{"runc":{"path":"runc"},"runsc":{"path":"/usr/bin/runsc"}}`, "claude,codex")

	// Probed harnesses from image.
	cfgProbed := config.Runner{
		ControlPlane:  "http://127.0.0.1:1",
		TokenFile:     tokenFile,
		DataDirectory: t.TempDir(),
		Slots:         2,
		Docker:        &config.Docker{Image: "ghcr.io/owainlewis/machinist-runner:latest", Runtime: "runsc"},
	}
	execProbed, err := NewDocker(t.Context(), cfgProbed, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	daemonProbed, err := NewDaemon(cfgProbed, execProbed, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfgProbed.Kind() != "docker (runsc)" || strings.Join(daemonProbed.installed, ",") != "claude,codex" {
		t.Fatalf("kind = %q, installed = %v", cfgProbed.Kind(), daemonProbed.installed)
	}

	// Explicitly configured harnesses skip probing.
	cfgExplicit := config.Runner{
		ControlPlane:  "http://127.0.0.1:1",
		TokenFile:     tokenFile,
		DataDirectory: t.TempDir(),
		Slots:         1,
		Docker:        &config.Docker{Image: "ghcr.io/owainlewis/machinist-runner:latest", Harnesses: []string{"pi"}},
	}
	daemonExplicit, err := NewDaemon(cfgExplicit, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfgExplicit.Kind() != "docker" || strings.Join(daemonExplicit.installed, ",") != "pi" {
		t.Fatalf("kind = %q, installed = %v", cfgExplicit.Kind(), daemonExplicit.installed)
	}
}

func TestDaemonStreamsLiveEventsDuringDockerRun(t *testing.T) {
	installFakeDocker(t, "", "claude")
	t.Setenv("GH_TOKEN", "fixture-github")

	dataDir := t.TempDir()
	gateDir := t.TempDir()
	gateFile := filepath.Join(gateDir, "release")

	var mu sync.Mutex
	var pollSeen protocol.PollRequest
	var eventCount int
	var liveEventsBeforeComplete int
	var completed bool
	liveReady := make(chan struct{}, 1)
	done := make(chan protocol.Completion, 1)
	queued := true

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/runners/poll"):
			var req protocol.PollRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			pollSeen = req
			give := queued
			queued = false
			mu.Unlock()
			if !give {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			_ = json.NewEncoder(w).Encode(runner.RunRequest{
				RunID:         fixtureRun,
				TicketID:      "t_1",
				Preset:        "default",
				Repository:    "owner/repo",
				Branch:        "machinist/t_1",
				Prompt:        "stream test",
				Steps:         []runner.Step{{Name: "live-step", Run: "echo '▸ Streaming live'; while [ ! -f " + gateFile + " ]; do sleep 0.02; done"}},
				TimeoutMillis: time.Minute.Milliseconds(),
				GitName:       "bot",
				GitEmail:      "bot@example.test",
			})
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			_ = json.NewEncoder(w).Encode(protocol.HeartbeatResponse{})
		case strings.HasSuffix(r.URL.Path, "/events"):
			var batch protocol.RunEventBatch
			_ = json.NewDecoder(r.Body).Decode(&batch)
			var last protocol.RunEvent
			if len(batch.Events) > 0 {
				_ = json.Unmarshal(batch.Events[len(batch.Events)-1], &last)
			}
			mu.Lock()
			eventCount += len(batch.Events)
			if !completed && eventCount > 0 {
				liveEventsBeforeComplete = eventCount
				select {
				case liveReady <- struct{}{}:
				default:
				}
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(protocol.RunEventPage{Cursor: last.Sequence})
		case strings.HasSuffix(r.URL.Path, "/complete"):
			var comp protocol.Completion
			_ = json.NewDecoder(r.Body).Decode(&comp)
			mu.Lock()
			completed = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			done <- comp
		}
	}))
	t.Cleanup(server.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("runner-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Runner{
		ControlPlane:  server.URL,
		TokenFile:     tokenFile,
		DataDirectory: dataDir,
		Slots:         1,
		Docker:        &config.Docker{Image: "ghcr.io/owainlewis/machinist-runner:latest"},
	}
	executor, err := NewDocker(t.Context(), cfg, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := NewDaemon(cfg, executor, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	daemon.pollInterval = 10 * time.Millisecond
	daemon.eventInterval = 20 * time.Millisecond
	daemon.heartbeatInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	go func() { _ = daemon.Run(ctx); close(finished) }()
	t.Cleanup(func() { cancel(); <-finished })

	select {
	case <-liveReady:
	case <-time.After(15 * time.Second):
		t.Fatal("no live events streamed to control plane before run completed")
	}

	// Release the running step inside the container.
	if err := os.WriteFile(gateFile, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case comp := <-done:
		if comp.State != "succeeded" {
			t.Fatalf("completion = %+v", comp)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not complete")
	}

	mu.Lock()
	gotPoll := pollSeen
	gotLive := liveEventsBeforeComplete
	mu.Unlock()
	if gotLive == 0 {
		t.Fatal("expected live events before completion")
	}
	if gotPoll.Kind != "docker" || !gotPoll.Probed || strings.Join(gotPoll.Harnesses, ",") != "steps,claude" {
		t.Fatalf("poll = %+v", gotPoll)
	}
}
