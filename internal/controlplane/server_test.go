package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/factory"
	"github.com/owainlewis/machinist/internal/fakegh"
	"github.com/owainlewis/machinist/internal/protocol"
	"github.com/owainlewis/machinist/internal/runner"
	"github.com/owainlewis/machinist/internal/tickets"
)

type fixture struct {
	server *Server
	store  *tickets.Store
	http   *httptest.Server
	token  string
}

func newFixture(t *testing.T) *fixture {
	fakegh.Install(t, testConfig().GitHub.Handle)
	t.Helper()
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server, err := New(testConfig(), store, testFactory(), "v1.2.3", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.CreateToken(t.Context(), "runner-1")
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewServer(server.Handler())
	t.Cleanup(web.Close)
	return &fixture{server: server, store: store, http: web, token: token}
}

func testConfig() config.Config {
	var loaded config.Config
	loaded.Server.Listen = "127.0.0.1:0"
	loaded.Server.DefaultAgent = "default"
	loaded.Server.MaxTurnsWithoutHuman = 3
	loaded.GitHub.Handle = "machinist-bot"
	return loaded
}

func testFactory() *factory.Factory {
	return &factory.Factory{Agents: map[string]factory.Agent{"default": {Name: "default", Harness: "claude", Playbook: "P"}}}
}

func (f *fixture) call(t *testing.T, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, f.http.URL+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := f.http.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response, data
}

func (f *fixture) queueRun(t *testing.T) runner.RunRequest {
	t.Helper()
	if _, _, err := f.store.CreateTicket(t.Context(), tickets.NewTicket{Source: "api", Repo: "owner/repo", Title: "T", Body: "Do it", Agent: "default", CreatedBy: "alice", SourceKey: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, body := f.call(t, http.MethodPost, "/api/v1/runners/poll", f.token, protocol.PollRequest{Harnesses: []string{"claude"}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("poll = %d: %s", response.StatusCode, body)
	}
	var run runner.RunRequest
	if err := json.Unmarshal(body, &run); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestHealthzIsOpenAndReturnsVersion(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodGet, "/healthz", "", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "v1.2.3") {
		t.Fatalf("%d: %s", response.StatusCode, body)
	}
}

func TestNewRejectsMissingHandleAndUnknownDefaultAgent(t *testing.T) {
	loaded := testConfig()
	loaded.GitHub.Handle = ""
	if _, err := New(loaded, nil, testFactory(), "v", io.Discard); err == nil || !strings.Contains(err.Error(), "handle") {
		t.Fatalf("error = %v", err)
	}
	loaded = testConfig()
	loaded.Server.DefaultAgent = "ghost"
	if _, err := New(loaded, nil, testFactory(), "v", io.Discard); err == nil || !strings.Contains(err.Error(), "default_agent") {
		t.Fatalf("error = %v", err)
	}
}

func TestStaticUIIsOpenAndHasSecurityHeaders(t *testing.T) {
	f := newFixture(t)
	response, body := f.call(t, http.MethodGet, "/", "", nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `<div id="root">`) {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	if !strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers = %v", response.Header)
	}
}

func TestRepositoriesRouteTellsTheUIItIsSignedIn(t *testing.T) {
	f := newFixture(t)
	f.server.config.GitHub.Repositories = []string{"owner/repo"}
	response, body := f.call(t, http.MethodGet, "/api/v1/repositories", f.token, nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "owner/repo") {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
}

func TestHeartbeatKeepsABusyRunnerInTheList(t *testing.T) {
	f := newFixture(t)
	run := f.queueRun(t)
	contact := func() tickets.Runner {
		runners, _, err := f.store.Fleet(t.Context())
		if err != nil || len(runners) != 1 {
			t.Fatalf("runners = %+v, err = %v", runners, err)
		}
		return runners[0]
	}
	before := contact().LastContact
	time.Sleep(2 * time.Millisecond)
	if response, body := f.call(t, http.MethodPost, "/api/v1/runs/"+run.RunID+"/heartbeat", f.token, nil); response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", response.StatusCode, body)
	}
	status := contact()
	if !status.LastContact.After(before) || len(status.Harnesses) != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestServeRefusesATokenThatIsNotTheBot(t *testing.T) {
	fakegh.Install(t, "alice")
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := New(testConfig(), store, testFactory(), "v", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Serve(t.Context(), nil); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Fatalf("error = %v", err)
	}
}

func TestServeWarnsAboutUnprotectedBranchAndAdminRights(t *testing.T) {
	github := fakegh.Install(t, testConfig().GitHub.Handle)
	github.SetAccess(false, true)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var log bytes.Buffer
	withRepository := testConfig()
	withRepository.GitHub.Repositories = []string{"owner/repo"}
	server, err := New(withRepository, store, testFactory(), "v", &log)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.checkGitHub(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no protection", "admin rights"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("missing %q in %q", want, log.String())
		}
	}
}

func TestServeStopsOnCancellation(t *testing.T) {
	fakegh.Install(t, testConfig().GitHub.Handle)
	store, err := tickets.Open(filepath.Join(t.TempDir(), "tickets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server, err := New(testConfig(), store, testFactory(), "v", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	listening := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, func(address net.Addr) { listening <- address }) }()
	address := <-listening
	response, err := http.Get("http://" + address.String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
}
