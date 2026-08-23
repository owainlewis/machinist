package factorycli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestFiniteCommandsUseLoopbackAPIWithHumanAndJSONOutput(t *testing.T) {
	now := time.Date(2026, time.August, 22, 10, 11, 12, 0, time.FixedZone("BST", 3600))
	run := protocol.Run{
		ID: "run-\x1b[2J1", Task: protocol.TaskSnapshot{Name: "Review Factory"},
		State: protocol.RunState("running\x00"), Source: "manual", SessionCount: 1, ActiveCount: 1,
		AdmittedAt: now, UpdatedAt: now,
	}
	runList := protocol.RunListSummary{
		ID: run.ID, TaskName: run.Task.Name, State: run.State, Source: run.Source,
		SessionCount: run.SessionCount, ActiveCount: run.ActiveCount,
		AdmittedAt: run.AdmittedAt, UpdatedAt: run.UpdatedAt,
	}
	detail := protocol.RunDetail{Run: run, Sessions: []protocol.Session{{
		ID: "session-1", RepositoryIdentity: "github.com/owainlewis/factory",
		State: protocol.SessionRunning, AssignedWorkerID: "worker-1",
		Attempts: []protocol.Attempt{{ID: "attempt-1"}}, Result: "safe\x1b[2Junsafe\x00end\n" + strings.Repeat("🙂", 100),
	}}}
	summary := protocol.RunSummary{
		ID: run.ID, TaskName: run.Task.Name, State: run.State, Source: run.Source,
		AdmittedAt: run.AdmittedAt, UpdatedAt: run.UpdatedAt,
		Sessions: []protocol.RunSessionSummary{{
			ID: detail.Sessions[0].ID, RepositoryIdentity: detail.Sessions[0].RepositoryIdentity,
			State: detail.Sessions[0].State, AssignedWorkerID: detail.Sessions[0].AssignedWorkerID,
			AttemptCount: len(detail.Sessions[0].Attempts), Result: detail.Sessions[0].Result,
		}},
	}
	worker := protocol.Worker{
		ID: "worker-1", Name: "local", Online: true, Health: "healthy",
		Runtime: "codex\x1b[2J", ActiveCount: 1, Capacity: 10, LastHeartbeat: now,
	}
	workerSummary := protocol.WorkerSummaryPage{Workers: []protocol.WorkerSummary{{
		ID: worker.ID, Name: worker.Name, Online: worker.Online, Health: worker.Health,
		Runtime: worker.Runtime, ActiveCount: worker.ActiveCount, Capacity: worker.Capacity,
		LastHeartbeat: worker.LastHeartbeat,
	}}}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.RequestURI())
		output.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/runs":
			if err := json.NewEncoder(output).Encode(protocol.RunListPage{Runs: []protocol.RunListSummary{runList}, NextCursor: "cursor\x1b[2J"}); err != nil {
				t.Error(err)
			}
		case "/api/v1/runs/run-1":
			if request.URL.Query().Get("view") == "summary" {
				if err := json.NewEncoder(output).Encode(summary); err != nil {
					t.Error(err)
				}
			} else {
				if err := json.NewEncoder(output).Encode(detail); err != nil {
					t.Error(err)
				}
			}
		case "/api/v1/workers":
			if request.URL.Query().Get("view") == "summary" {
				if err := json.NewEncoder(output).Encode(workerSummary); err != nil {
					t.Error(err)
				}
			} else {
				if err := json.NewEncoder(output).Encode(workerPage{Workers: []protocol.Worker{worker}}); err != nil {
					t.Error(err)
				}
			}
		default:
			http.NotFound(output, request)
		}
	}))
	t.Cleanup(server.Close)

	tests := []struct {
		arguments []string
		contains  []string
	}{
		{[]string{"--server", server.URL, "status"}, []string{"RUN ID", `run-\u001B[2J1`, "Review Factory", `running\u0000`, `cursor\u001B[2J`, "2026-08-22T09:11:12Z"}},
		{[]string{"--server", server.URL, "show", "run-1"}, []string{`Run: run-\u001B[2J1`, "SESSION ID", "github.com/owainlewis/factory", "ATTEMPTS", `safe\u001B[2Junsafe\u0000end`}},
		{[]string{"--server", server.URL, "workers"}, []string{"WORKER ID", "worker-1", "healthy", `codex\u001B[2J`}},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		if code := Run(Options{Arguments: test.arguments, Stdout: &stdout, Stderr: &stderr}); code != 0 {
			t.Fatalf("Run(%v) = %d, stderr %q", test.arguments, code, stderr.String())
		}
		for _, want := range test.contains {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("Run(%v) output %q does not contain %q", test.arguments, stdout.String(), want)
			}
		}
		if stderr.Len() != 0 {
			t.Fatalf("Run(%v) stderr = %q", test.arguments, stderr.String())
		}
		if !utf8.ValidString(stdout.String()) {
			t.Fatalf("Run(%v) wrote invalid UTF-8: %q", test.arguments, stdout.String())
		}
		if strings.ContainsAny(stdout.String(), "\x00\x1b") {
			t.Fatalf("Run(%v) wrote terminal control bytes: %q", test.arguments, stdout.String())
		}
	}

	var stdout, stderr bytes.Buffer
	if code := Run(Options{Arguments: []string{"--server", server.URL, "status", "--cursor", "next_page-2"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("cursor status = %d, stderr %q", code, stderr.String())
	}
	stdout.Reset()
	if code := Run(Options{Arguments: []string{"--server", server.URL, "--json", "status"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("JSON status = %d, stderr %q", code, stderr.String())
	}
	var page protocol.RunListPage
	if err := json.Unmarshal(stdout.Bytes(), &page); err != nil || len(page.Runs) != 1 || page.Runs[0].ID != "run-\x1b[2J1" {
		t.Fatalf("JSON status = %#v, error %v", page, err)
	}
	stdout.Reset()
	if code := Run(Options{Arguments: []string{"--server", server.URL, "--json", "show", "run-1"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("JSON show = %d, stderr %q", code, stderr.String())
	}
	var shown protocol.RunDetail
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil || len(shown.Sessions) != 1 || len(shown.Sessions[0].Attempts) != 1 {
		t.Fatalf("JSON show = %#v, error %v", shown, err)
	}
	stdout.Reset()
	if code := Run(Options{Arguments: []string{"--server", server.URL, "--json", "workers"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("JSON workers = %d, stderr %q", code, stderr.String())
	}
	var workers workerPage
	if err := json.Unmarshal(stdout.Bytes(), &workers); err != nil || len(workers.Workers) != 1 || workers.Workers[0].ID != worker.ID {
		t.Fatalf("JSON workers = %#v, error %v", workers, err)
	}
	if got, want := strings.Join(paths, ","), "/api/v1/runs?limit=50&view=summary,/api/v1/runs/run-1?view=summary,/api/v1/workers?view=summary,/api/v1/runs?cursor=next_page-2&limit=50&view=summary,/api/v1/runs?limit=50&view=summary,/api/v1/runs/run-1,/api/v1/workers"; got != want {
		t.Fatalf("API paths = %q, want %q", got, want)
	}
}

func TestFiniteCommandsRejectRedirectsWithoutMutatingTheHTTPClient(t *testing.T) {
	targetCalled := false
	target := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		targetCalled = true
		if err := json.NewEncoder(output).Encode(protocol.RunListPage{}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		http.Redirect(output, request, target.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Timeout: time.Second}
	var stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"--server", server.URL, "status"}, Stderr: &stderr,
		HTTPClient: client,
	})
	if code != 1 || !strings.Contains(stderr.String(), "server returned 302 Found") {
		t.Fatalf("redirect = %d, stderr %q", code, stderr.String())
	}
	if targetCalled {
		t.Fatal("redirect target received a request")
	}
	if client.CheckRedirect != nil {
		t.Fatal("caller's HTTP client was mutated")
	}
}

func TestFiniteCommandsReportEmptyResultsAndServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		output.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/v1/runs":
			if err := json.NewEncoder(output).Encode(protocol.RunListPage{}); err != nil {
				t.Error(err)
			}
		case "/api/v1/workers":
			if err := json.NewEncoder(output).Encode(workerPage{}); err != nil {
				t.Error(err)
			}
		default:
			output.WriteHeader(http.StatusNotFound)
			if err := json.NewEncoder(output).Encode(protocol.ErrorBody{Error: protocol.APIError{Code: "not_found", Message: "Run was not found"}}); err != nil {
				t.Error(err)
			}
		}
	}))
	t.Cleanup(server.Close)

	for _, test := range []struct {
		command string
		want    string
	}{{"status", "No Runs."}, {"workers", "No Workers."}} {
		var stdout bytes.Buffer
		if code := Run(Options{Arguments: []string{"--server", server.URL, test.command}, Stdout: &stdout}); code != 0 || !strings.Contains(stdout.String(), test.want) {
			t.Fatalf("%s = code %d, output %q", test.command, code, stdout.String())
		}
	}
	var stderr bytes.Buffer
	if code := Run(Options{Arguments: []string{"--server", server.URL, "show", "missing"}, Stderr: &stderr}); code != 1 {
		t.Fatalf("missing show = %d, stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "404 Not Found: Run was not found") {
		t.Fatalf("missing show stderr = %q", stderr.String())
	}
}

func TestFiniteCommandsRejectUnsafeEndpointsAndMalformedResponses(t *testing.T) {
	for _, endpoint := range []string{
		"https://127.0.0.1:7337", "http://example.com:7337", "http://127.0.0.1:0", "http://127.0.0.1:00", "http://127.0.0.1:65536",
		"http://127.0.0.1:7337/path", "http://user@127.0.0.1:7337", "http://localhost.:7337",
	} {
		var stderr bytes.Buffer
		if code := Run(Options{Arguments: []string{"--server", endpoint, "status"}, Stderr: &stderr}); code != 1 {
			t.Fatalf("endpoint %q = %d, stderr %q", endpoint, code, stderr.String())
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(output, "not-json")
	}))
	t.Cleanup(server.Close)
	var stderr bytes.Buffer
	if code := Run(Options{Arguments: []string{"--server", server.URL, "status"}, Stderr: &stderr}); code != 1 || !strings.Contains(stderr.String(), "decode server response") {
		t.Fatalf("malformed response = %d, stderr %q", code, stderr.String())
	}
}

func TestFiniteCommandsNormalizeLocalhostBeforeProxySelection(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		output.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(output).Encode(protocol.RunListPage{}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(target.Close)
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyHits := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyHits++
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(request *http.Request) (*url.URL, error) {
		if address := net.ParseIP(request.URL.Hostname()); address != nil && address.IsLoopback() {
			return nil, nil
		}
		return proxyURL, nil
	}

	var stdout, stderr bytes.Buffer
	code := Run(Options{
		Arguments:  []string{"--server", "http://LOCALHOST:" + targetURL.Port(), "status"},
		Stdout:     &stdout,
		Stderr:     &stderr,
		HTTPClient: &http.Client{Transport: transport},
	})
	if code != 0 || !strings.Contains(stdout.String(), "No Runs.") || proxyHits != 0 {
		t.Fatalf("uppercase localhost = %d, stdout %q, stderr %q, proxy hits %d", code, stdout.String(), stderr.String(), proxyHits)
	}
}

func TestPinnedTransportTriesEveryValidatedLoopbackAddress(t *testing.T) {
	var attempts []string
	transport := &http.Transport{DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
		attempts = append(attempts, address)
		if address == "[::1]:7337" {
			client, server := net.Pipe()
			server.Close()
			return client, nil
		}
		return nil, errors.New("not listening")
	}}
	client := &http.Client{Transport: transport}
	if err := pinLoopbackTransport(client, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}); err != nil {
		t.Fatal(err)
	}
	pinned := client.Transport.(*http.Transport)
	connection, err := pinned.DialContext(context.Background(), "tcp", "127.0.0.1:7337")
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	if got, want := strings.Join(attempts, ","), "127.0.0.1:7337,[::1]:7337"; got != want {
		t.Fatalf("dial attempts = %q, want %q", got, want)
	}
}

func TestShowAllowsSummariesLargerThanListLimit(t *testing.T) {
	result := strings.Repeat("x", maxResponseBytes)
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("view") != "summary" {
			t.Errorf("show query = %q", request.URL.RawQuery)
		}
		output.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(output).Encode(protocol.RunSummary{
			ID: "run-large", TaskName: "Large run",
			Sessions: []protocol.RunSessionSummary{{
				ID: "session-large", RepositoryIdentity: "github.com/owainlewis/factory",
				State: protocol.SessionSucceeded, Result: result,
			}},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	var stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"--server", server.URL, "show", "run-large"},
		Stdout:    io.Discard,
		Stderr:    &stderr,
	})
	if code != 0 {
		t.Fatalf("large show = %d, stderr %q", code, stderr.String())
	}
}

func TestShowUsesBlockedReasonAndReturnsOutputErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		output.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(output).Encode(protocol.RunSummary{
			ID: "run-blocked", TaskName: "Blocked run", State: protocol.RunBlocked,
			Sessions: []protocol.RunSessionSummary{{
				ID: "session-blocked", RepositoryIdentity: "github.com/owainlewis/factory",
				State: protocol.SessionBlocked, BlockedReason: "Repository is disabled.",
			}},
		}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	arguments := []string{"--server", server.URL, "show", "run-blocked"}
	if code := Run(Options{Arguments: arguments, Stdout: &stdout, Stderr: &stderr}); code != 0 || !strings.Contains(stdout.String(), "Repository is disabled.") {
		t.Fatalf("blocked show = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := Run(Options{Arguments: arguments, Stdout: failingWriter{}, Stderr: &stderr}); code != 1 || !strings.Contains(stderr.String(), "write show output") {
		t.Fatalf("failed show output = %d, stderr %q", code, stderr.String())
	}
}

func TestFiniteCommandsSanitizeHTTPReasonPhrases(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	served := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				served <- readErr
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, err = io.WriteString(connection, "HTTP/1.1 500 BAD\x1b[2JSTATUS\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		served <- err
	}()
	var stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"--server", "http://" + listener.Addr().String(), "status"},
		Stderr:    &stderr,
	})
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(stderr.String(), `500 BAD\u001B[2JSTATUS`) {
		t.Fatalf("unsafe HTTP status = %d, stderr %q", code, stderr.String())
	}
	if strings.ContainsRune(stderr.String(), '\x1b') {
		t.Fatalf("unsafe HTTP status wrote a terminal control byte: %q", stderr.String())
	}
}

func TestStartCommandsExecCompatibilityBinariesWithConfig(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"factory", "factory-server", "factory-worker"} {
		path := filepath.Join(bin, name)
		if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		role string
		key  string
	}{
		{"server", "FACTORY_SERVER_CONFIG"},
		{"worker", "FACTORY_WORKER_CONFIG"},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			var path string
			var arguments, environment []string
			var stderr bytes.Buffer
			code := Run(Options{
				Arguments: []string{test.role, "start", "--config", "config.toml"},
				Stderr:    &stderr, Environment: []string{test.key + "=old", "KEEP=value"},
				Executable: func() (string, error) { return filepath.Join(bin, "factory"), nil },
				LookPath:   func(string) (string, error) { return "", errors.New("not found") },
				Exec: func(gotPath string, gotArguments, gotEnvironment []string) error {
					path, arguments, environment = gotPath, gotArguments, gotEnvironment
					return nil
				},
			})
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("start = %d, stderr %q", code, stderr.String())
			}
			resolvedBin, err := filepath.EvalSymlinks(bin)
			if err != nil {
				t.Fatal(err)
			}
			if path != filepath.Join(resolvedBin, "factory-"+test.role) || len(arguments) != 1 || arguments[0] != "factory-"+test.role {
				t.Fatalf("exec = %q %#v", path, arguments)
			}
			if got := strings.Join(environment, ","); got != "KEEP=value,"+test.key+"=config.toml" {
				t.Fatalf("environment = %q", got)
			}
		})
	}
}

func TestStartCommandsReturnUsefulUsageAndLookupErrors(t *testing.T) {
	tests := []struct {
		arguments []string
		code      int
		want      string
	}{
		{nil, 2, "a command is required"},
		{[]string{"status", "extra"}, 2, "unexpected status arguments"},
		{[]string{"status", "--cursor="}, 2, "--cursor requires a non-empty value"},
		{[]string{"server"}, 2, "server requires the start command"},
		{[]string{"worker", "start", "extra"}, 2, "unexpected worker start arguments"},
		{[]string{"server", "start", "--config="}, 2, "--config requires a non-empty path"},
		{[]string{"--json", "server", "start"}, 2, "--json is available only"},
		{[]string{"server", "start"}, 1, "locate factory-server"},
	}
	for _, test := range tests {
		var stderr bytes.Buffer
		code := Run(Options{
			Arguments: test.arguments, Stderr: &stderr,
			Executable: func() (string, error) { return "/missing/factory", nil },
			LookPath:   func(string) (string, error) { return "", errors.New("not found") },
		})
		if code != test.code || !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("Run(%v) = %d, stderr %q; want %d containing %q", test.arguments, code, stderr.String(), test.code, test.want)
		}
	}
}

func TestHelpIsConciseAndAdvertisesImplementedCommandsOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(Options{Arguments: []string{"help"}, Stdout: &stdout, Stderr: &stderr}); code != 0 {
		t.Fatalf("help = %d, stderr %q", code, stderr.String())
	}
	for _, want := range []string{"factory server start", "factory worker start", "factory [--server URL] [--json] build", "factory [--server URL] [--json] status [--cursor CURSOR]", "FACTORY_SERVER"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help %q does not contain %q", stdout.String(), want)
		}
	}
	for _, forbidden := range []string{"factory run", "factory update", "factory answer"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("help advertises %q: %q", forbidden, stdout.String())
		}
	}
}

func TestFactoryServerEnvironmentOverridesDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(output http.ResponseWriter, _ *http.Request) {
		if err := json.NewEncoder(output).Encode(protocol.RunListPage{}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	var stdout, stderr bytes.Buffer
	code := Run(Options{
		Arguments: []string{"status"}, Stdout: &stdout, Stderr: &stderr,
		Getenv: func(key string) string {
			if key == "FACTORY_SERVER" {
				return "  " + server.URL + "  "
			}
			return ""
		},
	})
	if code != 0 || stdout.String() != "No Runs.\n" {
		t.Fatalf("environment endpoint = %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("output is closed")
}
