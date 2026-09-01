package managedworker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/controlplane"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestManagedWorkerExecutesControlPlaneRun(t *testing.T) {
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	scriptDirectory := filepath.Join(repository, "scripts")
	if err := os.MkdirAll(scriptDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -eu\ninput=$(cat)\n[ \"$input\" = \"managed request\" ]\n[ \"$(pwd)\" = \"$MACHINIST_REPOSITORY\" ]\nprintf managed-output\nprintf managed-error >&2\nprintf 2468 > \"$MACHINIST_TOKEN_USAGE_PATH\"\n"
	if err := os.WriteFile(filepath.Join(scriptDirectory, "workflow.sh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("{{machinist.prompt}}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(definitionPath, []byte("[commands.plan]\nexecutor=\"test\"\nprompt_file=\"plan.md\"\ntimeout=\"5s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := controlplane.OpenStore(filepath.Join(directory, "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agent, err := config.LoadCommand(definitionPath, "plan")
	if err != nil {
		t.Fatal(err)
	}
	agent, err = config.RenderPrompt(agent, "managed request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), "managed request", "machinist", "plan", agent); err != nil {
		t.Fatal(err)
	}
	server, err := controlplane.NewServer(store, definitionPath, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := New(config.Worker{
		Name:          "local-test",
		DataDirectory: filepath.Join(directory, "worker-data"),
		ControlPlane:  config.ControlPlane{URL: httpServer.URL, TokenFile: tokenPath},
		Executors:     map[string]config.Executor{"test": {Command: []string{"./scripts/workflow.sh"}}},
		Repositories:  map[string]config.Repository{"machinist": {Path: repository}},
	}, os.Stdout, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := store.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Jobs) == 1 && snapshot.Jobs[0].State == "succeeded" {
			run := snapshot.Jobs[0].Runs[0]
			output, outputErr := store.RunOutput(t.Context(), run.ID)
			if run.State != "succeeded" || outputErr != nil || output.Events == "" || output.Result == "" || run.WorkerName != "local-test" || run.DurationMillis == nil || run.TokenUsage == nil || *run.TokenUsage != 2468 {
				t.Fatalf("completed run = %#v", run)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete: %#v", snapshot.Jobs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestScheduledShepherdExecutesInDisposableRepository(t *testing.T) {
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(filepath.Join(directory, "shepherd.md"), []byte("Trusted schedule:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := `[commands.shepherd]
executor = "test"
prompt_file = "shepherd.md"
timeout = "5s"

[shepherd.disposable]
repository = "disposable"
every = "1m"
max_actions = 2
`
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	schedules, err := config.LoadShepherdSchedules(definitionPath)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("schedules = %#v, %v", schedules, err)
	}
	store, err := controlplane.OpenStore(filepath.Join(directory, "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, created, err := store.CreateScheduledJob(t.Context(), schedules[0]); err != nil || !created {
		t.Fatalf("scheduled job created = %t, %v", created, err)
	}
	server, err := controlplane.NewServer(store, definitionPath, "secret", 0)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	tokenPath := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker, err := New(config.Worker{
		Name:          "shepherd-worker",
		DataDirectory: filepath.Join(directory, "worker-data"),
		ControlPlane:  config.ControlPlane{URL: httpServer.URL, TokenFile: tokenPath},
		Executors:     map[string]config.Executor{"test": {Command: []string{"/bin/sh", "-c", `input=$(cat); case "$input" in *"at most 2 mutating actions"*) exit 0;; *) exit 7;; esac`}}},
		Repositories:  map[string]config.Repository{"disposable": {Path: repository}},
	}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, err := store.Snapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Jobs) == 1 && snapshot.Jobs[0].State == "succeeded" {
			if snapshot.Jobs[0].ScheduleName != "disposable" || snapshot.Jobs[0].Runs[0].Command != "shepherd" {
				t.Fatalf("scheduled job = %#v", snapshot.Jobs[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled job did not complete: %#v", snapshot.Jobs)
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestManagedWorkerReportsPreflightFailureWithoutStartingRun(t *testing.T) {
	directory := t.TempDir()
	worker := &Worker{
		config: config.Worker{
			DataDirectory: directory,
			Executors:     map[string]config.Executor{},
			Repositories:  map[string]config.Repository{},
		},
		instanceID: "worker-test",
	}
	completion := worker.execute(t.Context(), protocol.RunSpec{ID: "run_0123456789abcdef01234567", Executor: "missing", Repository: "missing", LeaseToken: "lease"})
	if completion.State != "failed" || completion.Error == "" || completion.Result != nil || completion.Events != "" {
		t.Fatalf("completion = %#v", completion)
	}
	if _, err := os.Stat(filepath.Join(directory, "runs")); !os.IsNotExist(err) {
		t.Fatalf("runner started during preflight: %v", err)
	}
}

func TestManagedWorkerRequiresHTTPSForRemoteControlPlane(t *testing.T) {
	_, err := New(config.Worker{
		Name:         "remote",
		ControlPlane: config.ControlPlane{URL: "http://10.0.0.5:7331", TokenFile: "unused"},
		Executors:    map[string]config.Executor{"test": {Command: []string{"agent"}}},
		Repositories: map[string]config.Repository{"machinist": {Path: "."}},
	}, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("error = %v", err)
	}
}

func TestCompletionDoesNotRetryPermanentClientError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(response, "too large", http.StatusRequestEntityTooLarge)
	}))
	defer server.Close()
	worker := &Worker{
		config:     config.Worker{ControlPlane: config.ControlPlane{URL: server.URL}},
		instanceID: "worker-test",
		client:     newClient(server.URL, "secret", server.Client()),
		stderr:     io.Discard,
	}
	err := worker.deliver(t.Context(), "run_0123456789abcdef01234567", protocol.Completion{})
	if err == nil || requests.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests.Load())
	}
}

func TestManagedWorkerHeartbeatsDuringExecutionAndContinuesAfterFailure(t *testing.T) {
	if heartbeatInterval != 10*time.Second {
		t.Fatalf("heartbeat interval = %v", heartbeatInterval)
	}
	var requests atomic.Int32
	heartbeats := make(chan protocol.Heartbeat, 2)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/runs/run-test/heartbeat" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request = %s authorization %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		var heartbeat protocol.Heartbeat
		if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
			t.Errorf("decode heartbeat: %v", err)
		}
		heartbeats <- heartbeat
		if requests.Add(1) == 1 {
			http.Error(response, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ticks := make(chan time.Time, 2)
	started := make(chan struct{})
	release := make(chan struct{})
	var stderr strings.Builder
	worker := &Worker{
		config:         config.Worker{ControlPlane: config.ControlPlane{URL: server.URL}},
		instanceID:     "worker-test",
		client:         newClient(server.URL, "secret", server.Client()),
		stderr:         &stderr,
		heartbeatTicks: ticks,
		executeRun: func(context.Context, protocol.RunSpec) protocol.Completion {
			close(started)
			<-release
			return protocol.Completion{State: "succeeded"}
		},
	}
	spec := protocol.RunSpec{ID: "run-test", LeaseToken: "lease-test"}
	done := make(chan protocol.Completion, 1)
	go func() { done <- worker.executeWithHeartbeats(t.Context(), spec) }()
	<-started

	for index := 0; index < 2; index++ {
		ticks <- time.Time{}
		heartbeat := <-heartbeats
		if heartbeat.InstanceID != "worker-test" || heartbeat.LeaseToken != "lease-test" {
			t.Fatalf("heartbeat = %#v", heartbeat)
		}
	}
	select {
	case completion := <-done:
		t.Fatalf("agent stopped after heartbeat failure: %#v", completion)
	default:
	}
	close(release)
	completion := <-done
	if completion.State != "succeeded" || requests.Load() != 2 {
		t.Fatalf("completion = %#v, requests = %d", completion, requests.Load())
	}
	if !strings.Contains(stderr.String(), "machinist: heartbeat run run-test") {
		t.Fatalf("heartbeat failure was not logged: %q", stderr.String())
	}
}

func TestManagedWorkerHeartbeatsUntilCompletionIsAcknowledged(t *testing.T) {
	heartbeats := make(chan protocol.Heartbeat, 2)
	completionStarted := make(chan struct{})
	releaseCompletion := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseCompletion) }) })
	var completionRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/runs/run-test/heartbeat":
			var heartbeat protocol.Heartbeat
			if err := json.NewDecoder(request.Body).Decode(&heartbeat); err != nil {
				t.Errorf("decode heartbeat: %v", err)
			}
			heartbeats <- heartbeat
			response.WriteHeader(http.StatusNoContent)
		case "/api/v1/runs/run-test/complete":
			if completionRequests.Add(1) == 1 {
				close(completionStarted)
				<-releaseCompletion
				http.Error(response, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request path %q", request.URL.Path)
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	ticks := make(chan time.Time, 1)
	worker := &Worker{
		config:         config.Worker{ControlPlane: config.ControlPlane{URL: server.URL}},
		instanceID:     "worker-test",
		client:         newClient(server.URL, "secret", server.Client()),
		stderr:         io.Discard,
		heartbeatTicks: ticks,
	}
	spec := protocol.RunSpec{ID: "run-test", LeaseToken: "lease-test"}
	done := make(chan error, 1)
	go func() {
		done <- worker.deliverWithHeartbeats(t.Context(), spec, protocol.Completion{InstanceID: "worker-test", LeaseToken: "lease-test", State: "succeeded"})
	}()

	immediate := <-heartbeats
	if immediate.InstanceID != "worker-test" || immediate.LeaseToken != "lease-test" {
		t.Fatalf("immediate heartbeat = %#v", immediate)
	}
	<-completionStarted
	ticks <- time.Time{}
	duringRetry := <-heartbeats
	if duringRetry.InstanceID != "worker-test" || duringRetry.LeaseToken != "lease-test" {
		t.Fatalf("completion heartbeat = %#v", duringRetry)
	}
	releaseOnce.Do(func() { close(releaseCompletion) })
	if err := <-done; err != nil || completionRequests.Load() != 2 {
		t.Fatalf("delivery error = %v, completion requests = %d", err, completionRequests.Load())
	}
}

func TestManagedWorkerSeparatesRedispatchedLeaseArtifacts(t *testing.T) {
	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	worker := &Worker{
		config: config.Worker{
			DataDirectory: directory,
			Executors:     map[string]config.Executor{"test": {Command: []string{"/bin/sh", "-c", "cat >/dev/null"}}},
			Repositories:  map[string]config.Repository{"machinist": {Path: repository}},
		},
		instanceID: "worker-test",
		stdout:     io.Discard,
		stderr:     io.Discard,
	}
	runID := "run_0123456789abcdef01234567"
	for _, lease := range []string{"lease_first", "lease_second"} {
		completion := worker.execute(t.Context(), protocol.RunSpec{
			ID:             runID,
			Command:        "plan",
			CommandHash:    "plan-hash",
			Executor:       "test",
			Repository:     "machinist",
			RenderedPrompt: "managed prompt",
			TimeoutMillis:  5000,
			LeaseToken:     lease,
		})
		if completion.State != "succeeded" || completion.Result == nil || completion.Events == "" {
			t.Fatalf("completion for %s = %#v", lease, completion)
		}
		for _, name := range []string{"events.jsonl", "result.json"} {
			if _, err := os.Stat(filepath.Join(directory, "runs", runID, lease, name)); err != nil {
				t.Fatalf("artifact %s for %s: %v", name, lease, err)
			}
		}
	}
}

func TestManagedWorkerStopsHeartbeatLoopWhenTerminated(t *testing.T) {
	ticks := make(chan time.Time, 1)
	started := make(chan struct{})
	exited := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	worker := &Worker{
		heartbeatTicks: ticks,
		executeRun: func(ctx context.Context, _ protocol.RunSpec) protocol.Completion {
			close(started)
			<-ctx.Done()
			close(exited)
			return protocol.Completion{State: "cancelled", ExitCode: 130}
		},
	}
	done := make(chan protocol.Completion, 1)
	go func() { done <- worker.executeWithHeartbeats(ctx, protocol.RunSpec{}) }()
	<-started
	cancel()
	<-exited
	completion := <-done
	if completion.State != "cancelled" || completion.ExitCode != 130 {
		t.Fatalf("completion = %#v", completion)
	}
	ticks <- time.Time{}
}
