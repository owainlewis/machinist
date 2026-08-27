package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
)

func TestServerProtectsSubmissionAndWorkerAPIs(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	status := getStatus(t, webServer.URL)
	if status.CSRFToken == "" || len(status.Agents) != 1 || status.Agents[0] != "plan" {
		t.Fatalf("status = %#v", status)
	}
	workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{"instance_id": "worker-a", "name": "test-worker", "executors": []string{"test"}, "repositories": []string{"machinist"}}, map[string]string{"Authorization": "Bearer secret"})
	if workerPoll.StatusCode != http.StatusOK {
		t.Fatalf("worker poll status = %d", workerPoll.StatusCode)
	}
	workerPoll.Body.Close()
	status = getStatus(t, webServer.URL)
	if len(status.Workers) != 1 || len(status.Workers[0].Repositories) != 1 || status.Workers[0].Repositories[0] != "machinist" || len(status.Repositories) != 1 || status.Repositories[0] != "machinist" {
		t.Fatalf("status = %#v", status)
	}

	unauthorized := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()

	foreignHeaders := map[string]string{"Origin": "https://evil.example", "X-Machinist-CSRF": status.CSRFToken}
	foreign := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "agent": "plan"}, foreignHeaders)
	if foreign.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign submission status = %d", foreign.StatusCode)
	}
	foreign.Body.Close()

	legacyHeaders := map[string]string{"Origin": webServer.URL, "X-Factory-CSRF": status.CSRFToken}
	legacy := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "agent": "plan"}, legacyHeaders)
	if legacy.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy CSRF header status = %d", legacy.StatusCode)
	}
	legacy.Body.Close()

	headers := map[string]string{"Origin": webServer.URL, "X-Machinist-CSRF": status.CSRFToken}
	created := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "agent": "plan", "model": "luna"}, headers)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", created.StatusCode)
	}
	created.Body.Close()

	status = getStatus(t, webServer.URL)
	if len(status.Jobs) != 1 || status.Jobs[0].Prompt != "Work locally" || status.Jobs[0].Runs[0].State != "queued" || status.Jobs[0].Runs[0].Model != "luna" {
		t.Fatalf("jobs = %#v", status.Jobs)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", strings.NewReader(`{"prompt":"x"}`))
	request.Host = "127.0.0.1:7331"
	request.Header.Set("Origin", "http://127.0.0.1:7331")
	request.Header.Set("X-Machinist-CSRF", server.csrfToken)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selection status = %d", response.Code)
	}
}

func TestServerAcceptsBearerSubmissionAndRejectsInvalidToken(t *testing.T) {
	_, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{
		"instance_id": "worker-a", "name": "test-worker", "executors": []string{"test"}, "repositories": []string{"machinist"},
	}, map[string]string{"Authorization": "Bearer secret"})
	if workerPoll.StatusCode != http.StatusOK {
		t.Fatalf("worker poll status = %d", workerPoll.StatusCode)
	}
	workerPoll.Body.Close()

	created := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "queue from terminal", "repository": "machinist", "agent": "plan",
	}, map[string]string{"Authorization": "Bearer secret"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("bearer submission status = %d", created.StatusCode)
	}
	created.Body.Close()

	invalid := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "must not queue", "repository": "machinist", "agent": "plan",
	}, map[string]string{"Authorization": "Bearer invalid"})
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status = %d", invalid.StatusCode)
	}
	invalid.Body.Close()

	status := getStatus(t, webServer.URL)
	if len(status.Jobs) != 1 || status.Jobs[0].Prompt != "queue from terminal" {
		t.Fatalf("jobs after bearer submissions = %#v", status.Jobs)
	}
}

func TestServerRejectsUnavailableRepositoryBeforePersistence(t *testing.T) {
	_, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	status := getStatus(t, webServer.URL)
	headers := map[string]string{"Origin": webServer.URL, "X-Machinist-CSRF": status.CSRFToken}
	response := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "unknown repository", "repository": "missing", "agent": "plan",
	}, headers)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown repository status = %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !strings.Contains(string(body), "not defined in the control plane") {
		t.Fatalf("unknown repository body = %q, error = %v", body, err)
	}

	status = getStatus(t, webServer.URL)
	if len(status.Jobs) != 0 {
		t.Fatalf("unknown repository was persisted: %#v", status.Jobs)
	}
}

func TestServerQueuesKnownRepositoryWithoutLiveWorker(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	auth := map[string]string{"Authorization": "Bearer secret"}
	workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{
		"instance_id": "worker-a", "name": "test-worker", "executors": []string{"test"}, "repositories": []string{"machinist"},
	}, auth)
	if workerPoll.StatusCode != http.StatusOK {
		t.Fatalf("worker poll status = %d", workerPoll.StatusCode)
	}
	workerPoll.Body.Close()
	if _, err := server.store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id=?`, time.Now().Add(-workerAvailabilityWindow-time.Second).UTC().Format(time.RFC3339Nano), "worker-a"); err != nil {
		t.Fatal(err)
	}

	response := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "queue while worker is away", "repository": "machinist", "agent": "plan",
	}, auth)
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("submission status = %d, body = %s", response.StatusCode, body)
	}
	response.Body.Close()

	catalogHTTPResponse, err := http.Get(webServer.URL + "/api/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer catalogHTTPResponse.Body.Close()
	var catalog catalogResponse
	if err := json.NewDecoder(catalogHTTPResponse.Body).Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalogHTTPResponse.StatusCode != http.StatusOK || len(catalog.Repositories) != 1 || catalog.Repositories[0] != "machinist" {
		t.Fatalf("catalog = %#v, status = %d", catalog, catalogHTTPResponse.StatusCode)
	}
}

func TestServerQueuesKnownRepositoryWithUnrelatedLiveWorker(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	auth := map[string]string{"Authorization": "Bearer secret"}
	for _, instance := range []string{"worker-old-a", "worker-old-b"} {
		workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{
			"instance_id": instance, "name": instance, "executors": []string{"test"}, "repositories": []string{"removed"},
		}, auth)
		if workerPoll.StatusCode != http.StatusOK {
			t.Fatalf("historical worker poll status = %d", workerPoll.StatusCode)
		}
		workerPoll.Body.Close()
		if _, err := server.store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id=?`, time.Now().Add(-workerAvailabilityWindow-time.Second).UTC().Format(time.RFC3339Nano), instance); err != nil {
			t.Fatal(err)
		}
	}

	currentPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{
		"instance_id": "worker-current", "name": "current-worker", "executors": []string{"test"}, "repositories": []string{"machinist"},
	}, auth)
	if currentPoll.StatusCode != http.StatusOK {
		t.Fatalf("current worker poll status = %d", currentPoll.StatusCode)
	}
	currentPoll.Body.Close()

	response := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "queue while advertising workers are away", "repository": "removed", "agent": "plan",
	}, auth)
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("submission status = %d, body = %s", response.StatusCode, body)
	}
	response.Body.Close()

	status := getStatus(t, webServer.URL)
	if len(status.Jobs) != 1 || status.Jobs[0].Repository != "removed" {
		t.Fatalf("jobs = %#v", status.Jobs)
	}
}

func TestServerServesEmbeddedReactAppAndRejectsRemoteListen(t *testing.T) {
	_, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	response, err := http.Get(webServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(body.String(), `<div id="root"></div>`) {
		t.Fatalf("status = %d body = %q", response.StatusCode, body.String())
	}
	if err := validateLoopbackListen("0.0.0.0:7331"); err == nil {
		t.Fatal("expected non-loopback listen rejection")
	}
	if err := validateLoopbackListen("127.0.0.1:7331"); err != nil {
		t.Fatal(err)
	}
}

func TestServerExposesReadOnlyDefinitions(t *testing.T) {
	_, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	response, err := http.Get(webServer.URL + "/api/v1/definitions")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var definitions definitionsResponse
	if err := json.NewDecoder(response.Body).Decode(&definitions); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || len(definitions.Agents) != 1 || definitions.Agents[0].Name != "plan" || definitions.Agents[0].Executor != "test" || definitions.Agents[0].Timeout != "1m0s" || !strings.Contains(definitions.Agents[0].Prompt, "{{machinist.prompt}}") {
		t.Fatalf("definitions = %#v", definitions)
	}
	if len(definitions.Pipelines) != 1 || definitions.Pipelines[0].Name != "default" || len(definitions.Pipelines[0].Agents) != 1 || definitions.Pipelines[0].Agents[0] != "plan" {
		t.Fatalf("pipelines = %#v", definitions.Pipelines)
	}
}

func TestCompletionLimitFitsMaximumRecordedOutput(t *testing.T) {
	// encoding/json can double a string when every byte requires escaping.
	worstCaseEncodedEvents := 2 * runner.MaxEventLogBytes
	const envelopeAllowance = 4 << 20
	if maxCompletionBytes < worstCaseEncodedEvents+envelopeAllowance {
		t.Fatalf("completion limit %d cannot hold encoded events and envelope %d", maxCompletionBytes, worstCaseEncodedEvents+envelopeAllowance)
	}
}

func TestHeartbeatEndpointAuthenticatesAndRejectsInvalidLeases(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	auth := map[string]string{"Authorization": "Bearer secret"}

	unauthorized := postJSON(t, webServer.URL+"/api/v1/runs/missing/heartbeat", protocol.Heartbeat{}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized heartbeat status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()
	unknown := postJSON(t, webServer.URL+"/api/v1/runs/missing/heartbeat", protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: "lease"}, auth)
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown heartbeat status = %d", unknown.StatusCode)
	}
	unknown.Body.Close()

	if _, err := server.store.CreateJob(t.Context(), "request", "machinist", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}
	pollResponse := postJSON(t, webServer.URL+"/api/v1/workers/poll", pollRequest("worker-a", []string{"codex"}, []string{"machinist"}), auth)
	if pollResponse.StatusCode != http.StatusOK {
		t.Fatalf("poll status = %d", pollResponse.StatusCode)
	}
	var polled protocol.PollResponse
	if err := json.NewDecoder(pollResponse.Body).Decode(&polled); err != nil {
		t.Fatal(err)
	}
	pollResponse.Body.Close()
	if polled.Run == nil {
		t.Fatal("poll returned no run")
	}

	for name, heartbeat := range map[string]protocol.Heartbeat{
		"other worker": {InstanceID: "worker-b", LeaseToken: polled.Run.LeaseToken},
		"stale token":  {InstanceID: "worker-a", LeaseToken: "stale"},
	} {
		t.Run(name, func(t *testing.T) {
			response := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", heartbeat, auth)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d", response.StatusCode)
			}
			response.Body.Close()
		})
	}
	validHeartbeat := protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: polled.Run.LeaseToken}
	renewed := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", validHeartbeat, auth)
	if renewed.StatusCode != http.StatusNoContent {
		t.Fatalf("valid heartbeat status = %d", renewed.StatusCode)
	}
	renewed.Body.Close()

	completion := protocol.Completion{InstanceID: "worker-a", LeaseToken: polled.Run.LeaseToken, State: "succeeded", ExitCode: 0, Result: json.RawMessage(`{"duration_millis":1750,"token_usage":9007199254740993}`)}
	completed := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/complete", completion, auth)
	if completed.StatusCode != http.StatusNoContent {
		t.Fatalf("completion status = %d", completed.StatusCode)
	}
	completed.Body.Close()
	response, err := http.Get(webServer.URL + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	statusBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(statusBody, []byte(`"token_usage":"9007199254740993"`)) {
		t.Fatalf("status token usage lost precision: %s", statusBody)
	}
	var status statusResponse
	if err := json.Unmarshal(statusBody, &status); err != nil {
		t.Fatal(err)
	}
	stored := status.Jobs[0].Runs[0]
	if stored.DurationMillis == nil || *stored.DurationMillis != 1750 || stored.TokenUsage == nil || *stored.TokenUsage != 9007199254740993 {
		t.Fatalf("status run metrics = %#v", stored)
	}
	nonRunning := postJSON(t, webServer.URL+"/api/v1/runs/"+polled.Run.ID+"/heartbeat", validHeartbeat, auth)
	if nonRunning.StatusCode != http.StatusConflict {
		t.Fatalf("non-running heartbeat status = %d", nonRunning.StatusCode)
	}
	nonRunning.Body.Close()
}

func TestServerEnqueuesConfiguredShepherdSchedule(t *testing.T) {
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "shepherd.md")
	if err := os.WriteFile(promptPath, []byte("Queue policy:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	definition := `[agents.shepherd]
executor = "test"
prompt_file = "shepherd.md"
timeout = "1m"

[shepherd.machinist]
repository = "machinist"
every = "10m"
max_actions = 2
`
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, definitionPath, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.enqueueScheduledRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := server.enqueueScheduledRuns(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ScheduleName != "machinist" || snapshot.Jobs[0].Prompt == "" {
		t.Fatalf("scheduled jobs = %#v", snapshot.Jobs)
	}
	if len(snapshot.Jobs[0].Runs) != 1 || !strings.Contains(snapshot.Jobs[0].Runs[0].Agent, "shepherd") {
		t.Fatalf("scheduled runs = %#v", snapshot.Jobs[0].Runs)
	}
}

func TestServerCancellationAlwaysStopsHTTPServer(t *testing.T) {
	for range 32 {
		directory := t.TempDir()
		promptPath := filepath.Join(directory, "plan.md")
		if err := os.WriteFile(promptPath, []byte("Plan this request:\n{{machinist.prompt}}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		definitionPath := filepath.Join(directory, "config.toml")
		if err := os.WriteFile(definitionPath, []byte("[agents.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := openTestStore(t, filepath.Join(directory, "machinist.db"))
		server, err := NewServer(store, definitionPath, "secret")
		if err != nil {
			t.Fatal(err)
		}

		probe, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := probe.Addr().String()
		if err := probe.Close(); err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(ctx, address) }()
		waitForServer(t, address)
		cancel()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Fatalf("Serve returned %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Serve did not return after cancellation")
		}
		dialCtx, cancelDial := context.WithTimeout(t.Context(), 100*time.Millisecond)
		connection, err := new(net.Dialer).DialContext(dialCtx, "tcp", address)
		cancelDial()
		if err == nil {
			connection.Close()
			t.Fatalf("HTTP server at %s still accepts connections after Serve returned", address)
		}
	}
}

func TestScheduledAdmissionFailureDoesNotStopServer(t *testing.T) {
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "shepherd.md")
	if err := os.WriteFile(promptPath, []byte("Queue policy:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	definition := `[agents.shepherd]
executor = "test"
prompt_file = "shepherd.md"
timeout = "1m"

[shepherd.machinist]
repository = "machinist"
every = "10m"
max_actions = 2
`
	if err := os.WriteFile(definitionPath, []byte(definition), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, definitionPath, "secret")
	if err != nil {
		t.Fatal(err)
	}
	server.schedulerEvery = time.Millisecond
	probe, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, address) }()
	waitForServer(t, address)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	dialCtx, cancelDial := context.WithTimeout(t.Context(), 100*time.Millisecond)
	connection, err := new(net.Dialer).DialContext(dialCtx, "tcp", address)
	cancelDial()
	if err != nil {
		t.Fatalf("HTTP server stopped after scheduled admission failure: %v", err)
	}
	connection.Close()
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}

func waitForServer(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dialCtx, cancelDial := context.WithTimeout(t.Context(), 50*time.Millisecond)
		connection, err := new(net.Dialer).DialContext(dialCtx, "tcp", address)
		cancelDial()
		if err == nil {
			connection.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("HTTP server at %s did not start", address)
}

func newTestHTTPServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("Plan this request:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath, []byte("[agents.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n\n[pipelines.default]\nagents = [\"plan\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, definitionPath, "secret")
	if err != nil {
		t.Fatal(err)
	}
	return server, httptest.NewServer(server.Handler())
}

func getStatus(t *testing.T, endpoint string) statusResponse {
	t.Helper()
	response, err := http.Get(endpoint + "/api/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var status statusResponse
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func postJSON(t *testing.T, endpoint string, body any, headers map[string]string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
