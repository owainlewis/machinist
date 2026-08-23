package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestWorkerLocalAgentUpdateScopesTokenAndForwardsWithoutWorkerCredentials(t *testing.T) {
	var forwarded atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwarded.Add(1)
		if request.Header.Get("Authorization") != "" {
			t.Errorf("local control-plane request unexpectedly had authorization")
		}
		var input protocol.AttemptUpdateRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode forwarded update: %v", err)
		}
		if input.LeaseToken != strings.Repeat("l", 64) || input.Status != protocol.WorkUpdateNoChange {
			t.Errorf("forwarded update = %#v", input)
		}
		_ = json.NewEncoder(writer).Encode(protocol.WorkUpdate{
			ID: "update-1", WorkID: testWorkID, AttemptID: testAttemptID,
			RequestID: input.RequestID, Status: input.Status, Message: input.Message,
		})
	}))
	defer controlPlane.Close()
	dataDirectory, err := os.MkdirTemp("/tmp", "factory-worker-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDirectory) })

	manager := &Manager{
		options:       Options{Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64))},
		dataDirectory: dataDirectory, client: newClient(controlPlane.URL, controlPlane.Client()),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &attemptHandle{context: ctx, cancel: cancel, done: make(chan struct{}), heartbeatDone: make(chan struct{})}
	close(handle.heartbeatDone)
	claim := protocol.Claim{
		Attempt: protocol.Attempt{ID: testAttemptID},
		Session: protocol.ClaimedSession{ID: testWorkID, OutcomeContract: protocol.OutcomeAgentUpdate},
	}
	server, err := manager.startAgentUpdateServer(claim, strings.Repeat("l", 64), handle, Repository{}, worktree{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	info, err := os.Stat(server.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}

	wrong := protocol.AgentUpdateRequest{
		WorkID: testWorkID, AttemptID: testAttemptID, UpdateToken: "wrong",
		RequestID: "50000000-0000-4000-8000-000000000001",
		Status:    protocol.WorkUpdateNoChange, Message: "No change.",
	}
	if status := postWorkerUpdate(t, server.socketPath, wrong, nil); status != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", status)
	}
	if forwarded.Load() != 0 {
		t.Fatal("wrong token reached the control plane")
	}
	wrong.UpdateToken = server.token
	var accepted protocol.WorkUpdate
	if status := postWorkerUpdate(t, server.socketPath, wrong, &accepted); status != http.StatusOK {
		t.Fatalf("accepted update status = %d", status)
	}
	if accepted.Status != protocol.WorkUpdateNoChange || forwarded.Load() != 1 {
		t.Fatalf("accepted = %#v, forwarded = %d", accepted, forwarded.Load())
	}
	if outcome, ok := handle.reportedOutcome(); !ok || outcome.Status != protocol.WorkUpdateNoChange {
		t.Fatalf("reported outcome = %#v, %t", outcome, ok)
	}
}

type outcomeObservingWriter struct {
	header http.Header
	handle *attemptHandle
	want   protocol.WorkUpdateStatus
	t      *testing.T
}

func (writer *outcomeObservingWriter) Header() http.Header {
	return writer.header
}

func (writer *outcomeObservingWriter) WriteHeader(status int) {
	writer.t.Helper()
	if status != http.StatusOK {
		writer.t.Fatalf("response status = %d", status)
	}
	outcome, ok := writer.handle.reportedOutcome()
	if !ok || outcome.Status != writer.want {
		writer.t.Fatalf("outcome at response exposure = %#v, %t", outcome, ok)
	}
}

func (writer *outcomeObservingWriter) Write(body []byte) (int, error) {
	return len(body), nil
}

func TestAcceptedOutcomeIsRecordedBeforeReadyOrFailedResponse(t *testing.T) {
	for _, status := range []protocol.WorkUpdateStatus{protocol.WorkUpdateReady, protocol.WorkUpdateFailed} {
		t.Run(string(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			handle := &attemptHandle{context: ctx, cancel: cancel}
			writer := &outcomeObservingWriter{
				header: make(http.Header), handle: handle, want: status, t: t,
			}
			respondAcceptedAgentUpdate(writer, protocol.WorkUpdate{Status: status}, handle)
		})
	}
}

func TestReadyReplaySkipsMutableDeliveryValidation(t *testing.T) {
	var forwarded atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forwarded.Add(1)
		var input protocol.AttemptUpdateRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Errorf("decode forwarded update: %v", err)
		}
		if !input.ReplayOnly {
			t.Errorf("ready replay unexpectedly requested fresh acceptance: %#v", input)
		}
		_ = json.NewEncoder(writer).Encode(protocol.WorkUpdate{
			ID: "stored-ready", WorkID: testWorkID, AttemptID: testAttemptID,
			RequestID: input.RequestID, Status: protocol.WorkUpdateReady, Message: input.Message,
			PullRequestURL: input.PullRequestURL, PullRequestHeadBranch: "factory/work-ready",
			PullRequestHeadSHA: strings.Repeat("a", 40),
		})
	}))
	defer controlPlane.Close()
	manager := &Manager{
		options: Options{GitExecutable: "missing-git", GitHubExecutable: "missing-gh"},
		client:  newClient(controlPlane.URL, controlPlane.Client()),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle := &attemptHandle{context: ctx, cancel: cancel}
	input := protocol.AgentUpdateRequest{
		WorkID: testWorkID, AttemptID: testAttemptID, UpdateToken: "token",
		RequestID: "51000000-0000-4000-8000-000000000001", Status: protocol.WorkUpdateReady,
		Message: "Ready.", PullRequestURL: "https://github.com/owainlewis/factory/pull/342",
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://factory.local/update", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	manager.handleAgentUpdate(recorder, request, protocol.Claim{
		Attempt: protocol.Attempt{ID: testAttemptID},
		Session: protocol.ClaimedSession{ID: testWorkID},
	}, "lease", sha256.Sum256([]byte("token")), handle, Repository{}, worktree{})
	if recorder.Code != http.StatusOK || forwarded.Load() != 1 {
		t.Fatalf("ready replay status = %d, forwarded = %d, body = %s", recorder.Code, forwarded.Load(), recorder.Body.String())
	}
}

const (
	testWorkID    = "11111111-1111-4111-8111-111111111111"
	testAttemptID = "22222222-2222-4222-8222-222222222222"
)

func postWorkerUpdate(t *testing.T, socket string, input protocol.AgentUpdateRequest, output any) int {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodPost, "http://factory.local/update", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if output != nil {
		if err := json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

func TestReadyDeliveryRequiresMatchingRepositoryBranchAndFetchedHead(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	checkout := filepath.Join(root, "checkout")
	runTestCommand(t, root, "git", "init", "--bare", remote)
	runTestCommand(t, root, "git", "clone", remote, checkout)
	runTestCommand(t, checkout, "git", "config", "user.email", "factory@example.com")
	runTestCommand(t, checkout, "git", "config", "user.name", "Factory Test")
	if err := os.WriteFile(filepath.Join(checkout, "proof.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, checkout, "git", "add", "proof.txt")
	runTestCommand(t, checkout, "git", "commit", "-m", "test: delivery proof")
	const publishBranch = "factory/work-1111111111111111"
	runTestCommand(t, checkout, "git", "branch", "-M", publishBranch)
	runTestCommand(t, checkout, "git", "push", "origin", "HEAD:refs/heads/"+publishBranch)
	head := strings.TrimSpace(runTestCommand(t, checkout, "git", "rev-parse", "HEAD"))
	repository, err := resolveRepository("factory", checkout, "git")
	if err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(root, "gh")
	writeFakeGitHubPR(t, gh, head, publishBranch, "owainlewis/factory")
	manager := &Manager{options: Options{GitExecutable: "git", GitHubExecutable: gh}}
	claim := protocol.Claim{
		Attempt: protocol.Attempt{ID: testAttemptID},
		Session: protocol.ClaimedSession{
			ID: testWorkID, Target: protocol.WorkTarget{PublishBranch: publishBranch},
		},
		Repository: protocol.Repository{RemoteIdentity: "github.com/owainlewis/factory"},
	}
	evidence, validationErr := manager.validateReadyDelivery(
		context.Background(), claim, repository, worktree{Path: checkout},
		"https://github.com/owainlewis/factory/pull/123",
	)
	if validationErr != nil || evidence.HeadSHA != head || evidence.HeadBranch != publishBranch {
		t.Fatalf("ready evidence = %#v, err %v", evidence, validationErr)
	}
	writeFakeGitHubPR(t, gh, strings.Repeat("a", 40), publishBranch, "owainlewis/factory")
	if _, validationErr := manager.validateReadyDelivery(
		context.Background(), claim, repository, worktree{Path: checkout},
		"https://github.com/owainlewis/factory/pull/123",
	); validationErr == nil || validationErr.code != "delivery_head_mismatch" || validationErr.retriable {
		t.Fatalf("mismatched delivery error = %#v", validationErr)
	}
	manager.options.GitHubExecutable = filepath.Join(root, "missing-gh")
	if _, validationErr := manager.validateReadyDelivery(
		context.Background(), claim, repository, worktree{Path: checkout},
		"https://github.com/owainlewis/factory/pull/123",
	); validationErr == nil || validationErr.code != "github_validation_unavailable" || !validationErr.retriable {
		t.Fatalf("provider outage error = %#v", validationErr)
	}
}

func runTestCommand(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	body, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, arguments, err, body)
	}
	return string(body)
}

func writeFakeGitHubPR(t *testing.T, path, head, branch, repository string) {
	t.Helper()
	body, err := json.Marshal(gitHubPullRequest{
		HTMLURL: "https://github.com/owainlewis/factory/pull/123",
		Head: struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		}{Ref: branch, SHA: head, Repo: struct {
			FullName string `json:"full_name"`
		}{FullName: repository}},
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s' '" + string(body) + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
