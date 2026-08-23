package worker

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/controlplane"
	"github.com/owainlewis/factory/internal/protocol"
)

func TestAgentUpdateSupervisorHelper(t *testing.T) {
	if os.Getenv("FACTORY_TEST_SUPERVISOR_HELPER") != "1" {
		return
	}
	control := os.NewFile(3, "factory-worker-control")
	if err := RunSupervisor(control, os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestFakeAgentReportsProgressAndOutcomeThroughRealWorkerEndpoint(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl is required for the fake-agent endpoint smoke test")
	}
	root, err := os.MkdirTemp("/tmp", "factory-agent-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	remote := filepath.Join(root, "remote.git")
	repositoryPath := filepath.Join(root, "repository")
	runTestCommand(t, root, "git", "init", "--bare", remote)
	runTestCommand(t, root, "git", "clone", remote, repositoryPath)
	runTestCommand(t, repositoryPath, "git", "config", "user.email", "factory@example.com")
	runTestCommand(t, repositoryPath, "git", "config", "user.name", "Factory Test")
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, repositoryPath, "git", "add", "README.md")
	runTestCommand(t, repositoryPath, "git", "commit", "-m", "test: seed repository")
	runTestCommand(t, repositoryPath, "git", "branch", "-M", "main")
	runTestCommand(t, repositoryPath, "git", "push", "-u", "origin", "main")
	runTestCommand(t, remote, "git", "symbolic-ref", "HEAD", "refs/heads/main")

	serverRoot, err := os.MkdirTemp("/tmp", "factory-server-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(serverRoot) })
	store, err := controlplane.Open(context.Background(), filepath.Join(serverRoot, "factory.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewServer(controlplane.NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	fakeCodex := filepath.Join(root, "codex")
	writeEndpointFakeCodex(t, fakeCodex)
	t.Setenv("FACTORY_TEST_SUPERVISOR_HELPER", "1")
	options := testOptions(fakeCodex)
	options.HTTPClient = server.Client()
	options.SupervisorCommand = []string{os.Args[0], "-test.run=TestAgentUpdateSupervisorHelper"}
	options.PollInterval = 10 * time.Millisecond
	options.RegistrationInterval = 20 * time.Millisecond
	manager, err := New(Config{
		Server: server.URL, Name: "agent-update-e2e", Runtime: protocol.RuntimeCodex,
		MaxConcurrent: 1, DataDirectory: filepath.Join(root, "worker"),
		Repositories: map[string]RepositoryConfig{"factory": {Path: repositoryPath, BaseBranch: "main"}},
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Manager shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Manager did not stop")
		}
	}()

	var registered protocol.Worker
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		registered, err = store.Worker(context.Background(), manager.ID())
		if err == nil && len(registered.Repositories) == 1 && registered.Health == "healthy" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || len(registered.Repositories) != 1 || registered.Health != "healthy" {
		t.Fatalf("Worker registration = %#v, err %v", registered, err)
	}
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Endpoint smoke", Prompt: "Report progress and no change.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{registered.Repositories[0].ID}, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "endpoint-smoke"})
	if err != nil {
		t.Fatal(err)
	}
	var work protocol.Work
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		work, err = store.Work(context.Background(), run.Sessions[0].ID)
		if err == nil && work.State == protocol.WorkNoChange {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || work.State != protocol.WorkNoChange {
		t.Fatalf("fake-agent Work = %#v, err %v", work, err)
	}
	updates, err := store.WorkUpdates(context.Background(), work.ID, 10, 0)
	if err != nil || len(updates.Updates) != 2 || updates.Updates[0].Status != protocol.WorkUpdateRunning ||
		updates.Updates[1].Status != protocol.WorkUpdateNoChange {
		t.Fatalf("fake-agent updates = %#v, err %v", updates, err)
	}
	if len(work.Attempts) != 1 || work.Attempts[0].State != "succeeded" {
		t.Fatalf("fake-agent Attempt = %#v", work.Attempts)
	}
}

func writeEndpointFakeCodex(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  echo codex-test
  exit 0
fi
if [ "${1:-}" = "login" ]; then
  echo 'Logged in'
  exit 0
fi
result_path=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "--output-last-message" ]; then result_path="$argument"; fi
  previous="$argument"
done
printf 'fake agent completed\n' > "$result_path"
progress='{"work_id":"'"$FACTORY_WORK_ID"'","attempt_id":"'"$FACTORY_ATTEMPT_ID"'","update_token":"'"$FACTORY_UPDATE_TOKEN"'","request_id":"60000000-0000-4000-8000-000000000001","status":"running","message":"Fake agent is checking the task."}'
outcome='{"work_id":"'"$FACTORY_WORK_ID"'","attempt_id":"'"$FACTORY_ATTEMPT_ID"'","update_token":"'"$FACTORY_UPDATE_TOKEN"'","request_id":"60000000-0000-4000-8000-000000000002","status":"no-change","message":"No defensible change exists."}'
curl --silent --show-error --fail --unix-socket "$FACTORY_UPDATE_SOCKET" -H 'Content-Type: application/json' --data "$progress" http://factory.local/update >/dev/null
curl --silent --show-error --fail --unix-socket "$FACTORY_UPDATE_SOCKET" -H 'Content-Type: application/json' --data "$outcome" http://factory.local/update >/dev/null
sleep 30
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
