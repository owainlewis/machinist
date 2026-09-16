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
	if status.CSRFToken == "" || len(status.Commands) != 1 || status.Commands[0] != "plan" {
		t.Fatalf("status = %#v", status)
	}
	workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{"instance_id": "worker-a", "name": "test-worker", "executors": []string{"test"}, "repositories": []string{"machinist"}}, map[string]string{"Authorization": "Bearer secret"})
	if workerPoll.StatusCode != http.StatusOK {
		t.Fatalf("worker poll status = %d", workerPoll.StatusCode)
	}
	workerPoll.Body.Close()
	status = getStatus(t, webServer.URL)
	if len(status.Workers) != 1 || !status.Workers[0].Connected || len(status.Workers[0].Repositories) != 1 || status.Workers[0].Repositories[0] != "machinist" || len(status.Repositories) != 1 || status.Repositories[0] != "machinist" {
		t.Fatalf("status = %#v", status)
	}

	unauthorized := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{}, nil)
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	unauthorized.Body.Close()

	foreignHeaders := map[string]string{"Origin": "https://evil.example", "X-Machinist-CSRF": status.CSRFToken}
	foreign := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "command": "plan"}, foreignHeaders)
	if foreign.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign submission status = %d", foreign.StatusCode)
	}
	foreign.Body.Close()

	legacyHeaders := map[string]string{"Origin": webServer.URL, "X-Factory-CSRF": status.CSRFToken}
	legacy := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "command": "plan"}, legacyHeaders)
	if legacy.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy CSRF header status = %d", legacy.StatusCode)
	}
	legacy.Body.Close()

	headers := map[string]string{"Origin": webServer.URL, "X-Machinist-CSRF": status.CSRFToken}
	created := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{"prompt": "Work locally", "repository": "machinist", "command": "plan", "model": "luna"}, headers)
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

func TestServerRejectsNonLoopbackRequestHosts(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	for _, test := range []struct {
		name string
		host string
		want int
	}{
		{name: "IPv4 with port", host: "127.0.0.1:7331", want: http.StatusOK},
		{name: "IPv6 with port", host: "[::1]:7331", want: http.StatusOK},
		{name: "localhost", host: "LOCALHOST:7331", want: http.StatusOK},
		{name: "DNS rebinding host", host: "attacker.example:7331", want: http.StatusForbidden},
		{name: "non-loopback IP", host: "192.0.2.1:7331", want: http.StatusForbidden},
		{name: "invalid port", host: "localhost:not-a-port", want: http.StatusForbidden},
		{name: "malformed host", host: "localhost:invalid:7331", want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
			request.Host = test.host
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestServerMarksStaleWorkerDisconnected(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	workerPoll := postJSON(t, webServer.URL+"/api/v1/workers/poll", map[string]any{
		"instance_id": "worker-stale", "name": "test-worker", "executors": []string{"test"}, "repositories": []string{"machinist"},
	}, map[string]string{"Authorization": "Bearer secret"})
	if workerPoll.StatusCode != http.StatusOK {
		t.Fatalf("worker poll status = %d", workerPoll.StatusCode)
	}
	workerPoll.Body.Close()

	lastSeen := server.store.now().Add(-workerAvailabilityWindow - time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := server.store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id='worker-stale'`, lastSeen); err != nil {
		t.Fatal(err)
	}
	status := getStatus(t, webServer.URL)
	if len(status.Workers) != 1 || status.Workers[0].Connected {
		t.Fatalf("workers = %#v", status.Workers)
	}
}

func TestServerDeletesOnlyTerminalJobsWithSubmissionAuthorization(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	jobID, err := server.store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := deleteRequest(t, webServer.URL+"/api/v1/jobs/"+jobID, nil)
	if unauthorized.StatusCode != http.StatusForbidden {
		t.Fatalf("unauthorized delete status = %d", unauthorized.StatusCode)
	}
	if err := unauthorized.Body.Close(); err != nil {
		t.Fatal(err)
	}
	status := getStatus(t, webServer.URL)
	headers := map[string]string{"Origin": webServer.URL, "X-Machinist-CSRF": status.CSRFToken}
	active := deleteRequest(t, webServer.URL+"/api/v1/jobs/"+jobID, headers)
	if active.StatusCode != http.StatusConflict {
		t.Fatalf("active delete status = %d", active.StatusCode)
	}
	if err := active.Body.Close(); err != nil {
		t.Fatal(err)
	}
	run, err := server.store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := server.store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	deleted := deleteRequest(t, webServer.URL+"/api/v1/jobs/"+jobID, headers)
	if deleted.StatusCode != http.StatusNoContent {
		t.Fatalf("terminal delete status = %d", deleted.StatusCode)
	}
	if err := deleted.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if jobs := getStatus(t, webServer.URL).Jobs; len(jobs) != 0 {
		t.Fatalf("jobs after delete = %#v", jobs)
	}
	missing := deleteRequest(t, webServer.URL+"/api/v1/jobs/"+jobID, headers)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing delete status = %d", missing.StatusCode)
	}
	if err := missing.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestServerAppliesConcurrentJobLimitToWorkerPolls(t *testing.T) {
	server, webServer := newTestHTTPServerWithLimit(t, 1)
	defer webServer.Close()
	agent := config.ResolvedCommand{Name: "plan", Executor: "test", Prompt: "Plan request", Timeout: time.Minute}
	for _, prompt := range []string{"first", "second"} {
		if _, err := server.store.CreateJob(t.Context(), prompt, "machinist", "plan", agent); err != nil {
			t.Fatal(err)
		}
	}
	poll := func(instance string) *protocol.RunSpec {
		t.Helper()
		response := postJSON(t, webServer.URL+"/api/v1/workers/poll", protocol.PollRequest{
			InstanceID: instance, Name: instance, Executors: []string{"test"}, Repositories: []string{"machinist"},
		}, map[string]string{"Authorization": "Bearer secret"})
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("poll status = %d", response.StatusCode)
		}
		var body protocol.PollResponse
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Run
	}
	if first := poll("worker-a"); first == nil {
		t.Fatal("first worker did not receive a job")
	}
	if blocked := poll("worker-b"); blocked != nil {
		t.Fatalf("second worker received job at capacity: %#v", blocked)
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
		"prompt": "queue from terminal", "repository": "machinist", "command": "plan",
	}, map[string]string{"Authorization": "Bearer secret"})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("bearer submission status = %d", created.StatusCode)
	}
	created.Body.Close()

	invalid := postJSON(t, webServer.URL+"/api/v1/jobs", map[string]string{
		"prompt": "must not queue", "repository": "machinist", "command": "plan",
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
		"prompt": "unknown repository", "repository": "missing", "command": "plan",
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
		"prompt": "queue while worker is away", "repository": "machinist", "command": "plan",
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
		"prompt": "queue while advertising workers are away", "repository": "removed", "command": "plan",
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

func TestServerServeReportsBoundListenerAndReleasesItOnImmediateCancellation(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	webServer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var address net.Addr
	err := server.Serve(ctx, "127.0.0.1:0", func(bound net.Addr) {
		address = bound
		duplicate, listenErr := net.Listen("tcp", bound.String())
		if listenErr == nil {
			duplicate.Close()
			t.Fatalf("reported address %s was not bound", bound)
		}
		cancel()
	})
	if err != nil {
		t.Fatalf("Serve returned after cancellation: %v", err)
	}
	if address == nil {
		t.Fatal("Serve did not report listening")
	}
	rebound, err := net.Listen("tcp", address.String())
	if err != nil {
		t.Fatalf("reported address %s was not released: %v", address, err)
	}
	rebound.Close()
}

func TestServerServeDoesNotReportListeningWhenAddressIsInUse(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	webServer.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	reported := false
	err = server.Serve(t.Context(), occupied.Addr().String(), func(net.Addr) {
		reported = true
	})
	if err == nil || !strings.Contains(err.Error(), "listen on "+occupied.Addr().String()) {
		t.Fatalf("Serve error = %v", err)
	}
	if reported {
		t.Fatal("Serve reported listening after bind failed")
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
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || len(definitions.Commands) != 1 || definitions.Commands[0].Name != "plan" || definitions.Commands[0].Executor != "test" || definitions.Commands[0].Timeout != "1m0s" || !strings.Contains(definitions.Commands[0].Prompt, "{{machinist.prompt}}") {
		t.Fatalf("definitions = %#v", definitions)
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

func TestSizeLimitedEndpointsRejectOversizedJSON(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	endpoints := []struct {
		name  string
		path  string
		limit int64
	}{
		{name: "submit", path: "/api/v1/jobs", limit: 1 << 20},
		{name: "poll", path: "/api/v1/workers/poll", limit: 1 << 20},
		{name: "heartbeat", path: "/api/v1/runs/missing/heartbeat", limit: 1 << 20},
		{name: "complete", path: "/api/v1/runs/missing/complete", limit: maxCompletionBytes},
	}
	stages := []struct {
		name   string
		prefix string
		fill   byte
	}{
		{name: "first decode", prefix: `{"padding":"`, fill: 'x'},
		{name: "trailing decode", prefix: `{}`, fill: ' '},
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint.name+"/known content length", func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.path, strings.NewReader("x"))
			request.Host = "127.0.0.1:7331"
			request.ContentLength = endpoint.limit + 1
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()

			server.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
		})
		for _, stage := range stages {
			t.Run(endpoint.name+"/"+stage.name, func(t *testing.T) {
				body := io.MultiReader(strings.NewReader(stage.prefix), io.LimitReader(repeatingByteReader(stage.fill), endpoint.limit+1))
				request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, endpoint.path, body)
				request.Host = "127.0.0.1:7331"
				request.Header.Set("Authorization", "Bearer secret")
				response := httptest.NewRecorder()

				server.Handler().ServeHTTP(response, request)

				if response.Code != http.StatusRequestEntityTooLarge {
					t.Fatalf("status = %d, body = %s", response.Code, response.Body)
				}
				if response.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
				}
				if response.Body.Len() > 256 {
					t.Fatalf("error response is not bounded: %d bytes", response.Body.Len())
				}
				var result map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					t.Fatalf("decode error response: %v", err)
				}
				if len(result) != 1 || result["error"] != "request body is too large" {
					t.Fatalf("error response = %#v", result)
				}
			})
		}
	}
}

func TestSizeLimitedEndpointsKeepMalformedJSONAtBadRequest(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()

	for name, path := range map[string]string{
		"submit":    "/api/v1/jobs",
		"poll":      "/api/v1/workers/poll",
		"heartbeat": "/api/v1/runs/missing/heartbeat",
		"complete":  "/api/v1/runs/missing/complete",
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(`{`))
			request.Host = "127.0.0.1:7331"
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()

			server.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body)
			}
		})
	}
}

type repeatingByteReader byte

func (reader repeatingByteReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(reader)
	}
	return len(buffer), nil
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

	if _, err := server.store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
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

func TestCompletionEndpointClassifiesClientAndPersistenceErrors(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	auth := map[string]string{"Authorization": "Bearer secret"}

	missing := postJSON(t, webServer.URL+"/api/v1/runs/missing/complete", protocol.Completion{
		InstanceID: "worker-a", LeaseToken: "lease", State: "succeeded", ExitCode: 0,
	}, auth)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing run status = %d", missing.StatusCode)
	}
	missing.Body.Close()

	if _, err := server.store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
		t.Fatal(err)
	}
	run, err := server.store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	endpoint := webServer.URL + "/api/v1/runs/" + run.ID + "/complete"

	for name, test := range map[string]struct {
		completion protocol.Completion
		want       int
	}{
		"invalid lease": {
			completion: protocol.Completion{InstanceID: "worker-a", LeaseToken: "stale", State: "succeeded", ExitCode: 0},
			want:       http.StatusConflict,
		},
		"invalid terminal state": {
			completion: protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "running", ExitCode: 0},
			want:       http.StatusBadRequest,
		},
		"invalid outcome": {
			completion: protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 1},
			want:       http.StatusBadRequest,
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := postJSON(t, endpoint, test.completion, auth)
			defer response.Body.Close()
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.want)
			}
		})
	}

	if _, err := server.store.db.ExecContext(t.Context(), `
		CREATE TRIGGER fail_completion
		BEFORE UPDATE OF state ON runs
		WHEN NEW.state = 'succeeded'
		BEGIN
			SELECT RAISE(FAIL, 'temporary completion write failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	persistenceFailure := postJSON(t, endpoint, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0,
	}, auth)
	defer persistenceFailure.Body.Close()
	if persistenceFailure.StatusCode != http.StatusInternalServerError {
		t.Fatalf("persistence failure status = %d, want %d", persistenceFailure.StatusCode, http.StatusInternalServerError)
	}
	persistenceBody, err := io.ReadAll(persistenceFailure.Body)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persistenceBody, []byte("temporary completion write failure")) {
		t.Fatalf("persistence response exposes storage error: %s", persistenceBody)
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
		if err := os.WriteFile(definitionPath, []byte("[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := openTestStore(t, filepath.Join(directory, "machinist.db"))
		server, err := NewServer(store, definitionPath, "secret", 0)
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
		go func() { serveDone <- server.Serve(ctx, address, nil) }()
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

func TestServerForcesCloseWhenGracefulShutdownTimesOut(t *testing.T) {
	server, webServer := newTestHTTPServer(t)
	defer webServer.Close()
	server.shutdownTimeout = 20 * time.Millisecond
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server.handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(handlerStarted)
		<-releaseHandler
	})

	ctx, cancel := context.WithCancel(t.Context())
	listening := make(chan net.Addr, 1)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(ctx, "127.0.0.1:0", func(address net.Addr) {
			listening <- address
		})
	}()
	address := <-listening
	requestDone := make(chan error, 1)
	go func() {
		response, err := http.Get("http://" + address.String())
		if response != nil {
			response.Body.Close()
		}
		requestDone <- err
	}()
	<-handlerStarted
	cancel()
	select {
	case err := <-serveDone:
		if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("Serve error = %v, want graceful shutdown timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve hung after graceful shutdown timed out")
	}
	close(releaseHandler)
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("stalled request did not finish after forced close")
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
	return newTestHTTPServerWithLimit(t, 0)
}

func newTestHTTPServerWithLimit(t *testing.T, maxConcurrentJobs int) (*Server, *httptest.Server) {
	t.Helper()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "plan.md")
	if err := os.WriteFile(promptPath, []byte("Plan this request:\n{{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definitionPath := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(definitionPath, []byte("[commands.plan]\nexecutor = \"test\"\nprompt_file = \"plan.md\"\ntimeout = \"1m\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(directory, "machinist.db"))
	server, err := NewServer(store, definitionPath, "secret", maxConcurrentJobs)
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

func deleteRequest(t *testing.T, endpoint string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
