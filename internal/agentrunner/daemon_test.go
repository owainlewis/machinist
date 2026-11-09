package agentrunner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

const fixtureRun = "run_0123456789abcdef01234567"

type fakeControlPlane struct {
	mu          sync.Mutex
	queue       []string
	polls       atomic.Int64
	harnesses   []string
	token       string
	heartbeat   func() (int, protocol.HeartbeatResponse)
	completeErr atomic.Int64
	completions map[string]protocol.Completion
	done        chan string
}

func newFakeControlPlane(queue ...string) *fakeControlPlane {
	return &fakeControlPlane{queue: queue, completions: map[string]protocol.Completion{}, done: make(chan string, 16), heartbeat: func() (int, protocol.HeartbeatResponse) { return http.StatusOK, protocol.HeartbeatResponse{} }}
}

func (f *fakeControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.token = r.Header.Get("Authorization")
	f.mu.Unlock()
	switch {
	case strings.HasSuffix(r.URL.Path, "/runners/poll"):
		f.polls.Add(1)
		var request protocol.PollRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.harnesses = request.Harnesses
		if len(f.queue) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := f.queue[0]
		f.queue = f.queue[1:]
		_ = json.NewEncoder(w).Encode(runner.RunRequest{RunID: id, Harness: "claude"})
	case strings.HasSuffix(r.URL.Path, "/heartbeat"):
		status, body := f.heartbeat()
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	case strings.HasSuffix(r.URL.Path, "/events"):
		_ = json.NewEncoder(w).Encode(protocol.RunEventPage{})
	case strings.HasSuffix(r.URL.Path, "/complete"):
		if f.completeErr.Add(-1) >= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var completion protocol.Completion
		_ = json.NewDecoder(r.Body).Decode(&completion)
		id := strings.Split(r.URL.Path, "/")[4]
		f.mu.Lock()
		f.completions[id] = completion
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		f.done <- id
	}
}

func (f *fakeControlPlane) completion(id string) protocol.Completion {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.completions[id]
}

type executorFunc func(context.Context, runner.RunRequest) runner.RunResult

func (e executorFunc) Execute(ctx context.Context, request runner.RunRequest) runner.RunResult {
	return e(ctx, request)
}

func startDaemon(t *testing.T, plane *fakeControlPlane, slots int, executor Executor) (*Daemon, context.CancelFunc) {
	t.Helper()
	server := httptest.NewServer(plane)
	t.Cleanup(server.Close)
	daemon := &Daemon{
		config:            config.Runner{DataDirectory: t.TempDir(), Slots: slots, Credentials: map[string]config.Credential{"claude": {Login: true}, "codex": {Login: true}}},
		client:            NewClient(server.URL, "runner-token"),
		executor:          executor,
		log:               io.Discard,
		pollInterval:      5 * time.Millisecond,
		heartbeatInterval: 5 * time.Millisecond,
		eventInterval:     5 * time.Millisecond,
		retryInterval:     5 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	finished := make(chan struct{})
	go func() { _ = daemon.Run(ctx); close(finished) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not stop")
		}
	})
	return daemon, cancel
}

func waitDone(t *testing.T, plane *fakeControlPlane) string {
	t.Helper()
	select {
	case id := <-plane.done:
		return id
	case <-time.After(10 * time.Second):
		t.Fatal("run did not complete")
		return ""
	}
}

func TestDaemonRunsAndReportsResult(t *testing.T) {
	plane := newFakeControlPlane(fixtureRun)
	tokens, cost := int64(99), 1.5
	daemon, _ := startDaemon(t, plane, 1, executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		return runner.RunResult{State: runner.StateSucceeded, Report: &protocol.Report{Outcome: "no_change", Summary: "Done"}, Tokens: &tokens, Cost: &cost}
	}))
	waitDone(t, plane)
	completion := plane.completion(fixtureRun)
	if completion.State != "succeeded" || completion.Report.Summary != "Done" || *completion.Tokens != 99 || *completion.Cost != 1.5 {
		t.Fatalf("completion = %+v", completion)
	}
	plane.mu.Lock()
	token, harnesses := plane.token, strings.Join(plane.harnesses, ",")
	plane.mu.Unlock()
	if token != "Bearer runner-token" || harnesses != "claude,codex" {
		t.Fatalf("token = %q, harnesses = %q", token, harnesses)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(daemon.runDirectory(fixtureRun)); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("run directory was not removed")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDaemonStopsAgentWhenControlPlaneSays(t *testing.T) {
	for name, heartbeat := range map[string]func() (int, protocol.HeartbeatResponse){
		"cancel": func() (int, protocol.HeartbeatResponse) {
			return http.StatusOK, protocol.HeartbeatResponse{Cancel: true}
		},
		"conflict": func() (int, protocol.HeartbeatResponse) { return http.StatusConflict, protocol.HeartbeatResponse{} },
		"unknown":  func() (int, protocol.HeartbeatResponse) { return http.StatusNotFound, protocol.HeartbeatResponse{} },
	} {
		t.Run(name, func(t *testing.T) {
			plane := newFakeControlPlane(fixtureRun)
			plane.heartbeat = heartbeat
			startDaemon(t, plane, 1, executorFunc(func(ctx context.Context, _ runner.RunRequest) runner.RunResult {
				select {
				case <-ctx.Done():
					return runner.RunResult{State: runner.StateCancelled}
				case <-time.After(10 * time.Second):
					return runner.RunResult{State: runner.StateSucceeded}
				}
			}))
			waitDone(t, plane)
			if got := plane.completion(fixtureRun).State; got != "cancelled" {
				t.Fatalf("state = %s", got)
			}
		})
	}
}

func TestDaemonKeepsWorkingWhenControlPlaneIsUnreachable(t *testing.T) {
	plane := newFakeControlPlane(fixtureRun)
	beats := atomic.Int64{}
	plane.heartbeat = func() (int, protocol.HeartbeatResponse) {
		beats.Add(1)
		return http.StatusServiceUnavailable, protocol.HeartbeatResponse{}
	}
	stopped := atomic.Bool{}
	startDaemon(t, plane, 1, executorFunc(func(ctx context.Context, _ runner.RunRequest) runner.RunResult {
		for beats.Load() < 3 {
			if ctx.Err() != nil {
				stopped.Store(true)
				return runner.RunResult{State: runner.StateCancelled}
			}
			time.Sleep(time.Millisecond)
		}
		return runner.RunResult{State: runner.StateSucceeded}
	}))
	waitDone(t, plane)
	if stopped.Load() || plane.completion(fixtureRun).State != "succeeded" {
		t.Fatalf("stopped = %v, state = %s", stopped.Load(), plane.completion(fixtureRun).State)
	}
}

func TestDaemonRetriesCompletion(t *testing.T) {
	plane := newFakeControlPlane(fixtureRun)
	plane.completeErr.Store(3)
	startDaemon(t, plane, 1, executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		return runner.RunResult{State: runner.StateFailed, Error: "exit 1"}
	}))
	waitDone(t, plane)
	if got := plane.completion(fixtureRun); got.State != "failed" || got.Error != "exit 1" {
		t.Fatalf("completion = %+v", got)
	}
}

func TestDaemonRunsUpToItsSlots(t *testing.T) {
	second := "run_0123456789abcdef01234568"
	plane := newFakeControlPlane(fixtureRun, second)
	release := make(chan struct{})
	running := atomic.Int64{}
	startDaemon(t, plane, 2, executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		running.Add(1)
		<-release
		return runner.RunResult{State: runner.StateSucceeded}
	}))
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("running = %d", running.Load())
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	waitDone(t, plane)
	waitDone(t, plane)
}

func TestFullDaemonDoesNotPoll(t *testing.T) {
	plane := newFakeControlPlane(fixtureRun)
	release := make(chan struct{})
	started := make(chan struct{})
	startDaemon(t, plane, 1, executorFunc(func(context.Context, runner.RunRequest) runner.RunResult {
		close(started)
		<-release
		return runner.RunResult{State: runner.StateSucceeded}
	}))
	<-started
	before := plane.polls.Load()
	time.Sleep(50 * time.Millisecond)
	if after := plane.polls.Load(); after != before {
		t.Fatalf("polled while full: %d then %d", before, after)
	}
	close(release)
	waitDone(t, plane)
}

func TestStoppingDaemonStillReportsTheRun(t *testing.T) {
	plane := newFakeControlPlane(fixtureRun)
	started := make(chan struct{})
	_, cancel := startDaemon(t, plane, 1, executorFunc(func(ctx context.Context, _ runner.RunRequest) runner.RunResult {
		close(started)
		<-ctx.Done()
		return runner.RunResult{State: runner.StateCancelled}
	}))
	<-started
	cancel()
	waitDone(t, plane)
	if got := plane.completion(fixtureRun).State; got != "cancelled" {
		t.Fatalf("state = %s", got)
	}
}
