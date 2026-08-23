package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewAcceptsBareAddressesAndURLs(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1:7337", "http://127.0.0.1:7337", "http://127.0.0.1:7337/"} {
		client, err := New(address)
		if err != nil {
			t.Fatalf("New(%q) = %v", address, err)
		}
		if client.base != "http://127.0.0.1:7337" {
			t.Fatalf("New(%q) base = %q", address, client.base)
		}
	}
	if _, err := New("http://"); err == nil {
		t.Fatal("an address with no host was accepted")
	}
}

func TestDecodesStructuredErrors(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"task_limit_reached","message":"too many tasks"}}`))
	}))

	_, err := client.Runs(context.Background(), 10)
	if err == nil {
		t.Fatal("expected an error")
	}
	if Code(err) != "task_limit_reached" {
		t.Fatalf("Code = %q", Code(err))
	}
	if !strings.Contains(err.Error(), "too many tasks") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodeIgnoresOtherErrors(t *testing.T) {
	if Code(context.Canceled) != "" {
		t.Fatal("a non-API error reported a code")
	}
}

func TestUnreachableServerExplainsHowToStartIt(t *testing.T) {
	// Port 1 is reserved and refuses connections, so this exercises the dial
	// failure an operator hits when the control plane is not running.
	client, err := New("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Runs(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "just run") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureRepositoryReusesAnExistingMatch(t *testing.T) {
	var created int
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/repositories":
			_, _ = w.Write([]byte(`{"repositories":[{"id":"repo_1","remote_identity":"github.com/acme/api"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repositories":
			created++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"repo_2","remote_identity":"github.com/acme/web"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))

	existing, err := client.EnsureRepository(context.Background(), "github.com/acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if existing.ID != "repo_1" {
		t.Fatalf("id = %q, want the existing repository", existing.ID)
	}
	if created != 0 {
		t.Fatal("an existing repository was registered again")
	}

	fresh, err := client.EnsureRepository(context.Background(), "github.com/acme/web")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID != "repo_2" || created != 1 {
		t.Fatalf("id = %q created = %d", fresh.ID, created)
	}
}

func TestStartTaskSendsTheIdempotencyKey(t *testing.T) {
	var body map[string]string
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks/task_1/run" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"run":{"id":"run_9","state":"running"},"sessions":[]}`))
	}))

	detail, err := client.StartTask(context.Background(), "task_1", "key-abc")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.ID != "run_9" {
		t.Fatalf("run = %q", detail.Run.ID)
	}
	if body["request_key"] != "key-abc" {
		t.Fatalf("request_key = %q", body["request_key"])
	}
}

func TestEventsPassTheCursor(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("after"); got != "12" {
			t.Errorf("after = %q", got)
		}
		_, _ = w.Write([]byte(`{"events":[{"sequence":13,"kind":"log"}],"next_after":13,"has_more":false}`))
	}))

	page, err := client.Events(context.Background(), "attempt_1", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextAfter != 13 {
		t.Fatalf("page = %+v", page)
	}
}

func TestCancelRunPostsToTheRun(t *testing.T) {
	var called bool
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = r.Method == http.MethodPost && r.URL.Path == "/api/v1/runs/run_9/cancel"
		w.WriteHeader(http.StatusNoContent)
	}))
	if err := client.CancelRun(context.Background(), "run_9"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("cancel did not reach the run")
	}
}
