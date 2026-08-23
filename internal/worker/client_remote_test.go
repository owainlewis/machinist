package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRemoteClientRejectsRedirectsBeforeSendingSecrets(t *testing.T) {
	targetRequests := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client := newClient(redirect.URL, redirect.Client())
	client.credential = "factory_worker_credential"
	if _, err := client.request(context.Background(), http.MethodPost, "/credential", struct{}{}, nil); err == nil {
		t.Fatal("credential request followed a redirect")
	}
	if _, err := client.requestWithoutCredential(context.Background(), http.MethodPost, "/enrollment", map[string]string{
		"enrollment_token": "factory_enroll_secret",
	}, nil); err == nil {
		t.Fatal("enrollment request followed a redirect")
	}
	if targetRequests != 0 {
		t.Fatalf("redirect target received %d secret-bearing requests", targetRequests)
	}
	if redirect.Client().CheckRedirect != nil {
		t.Fatal("newClient mutated the caller's HTTP client")
	}
}

func TestRemoteClientEnrollsOnceAndPersistsCredential(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	credential := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/worker-enrollments/exchange":
			var input protocol.ExchangeWorkerEnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.WorkerID != "remote-worker" || input.EnrollmentToken != enrollment ||
				!strings.HasPrefix(input.Credential, "factory_worker_") {
				t.Errorf("exchange input = %#v", input)
			}
			credential = input.Credential
			_ = json.NewEncoder(w).Encode(protocol.WorkerCredential{Credential: input.Credential})
		case "/api/v1/workers/remote-worker":
			if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
				t.Errorf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(protocol.Worker{
				ID: "remote-worker", Name: "remote", Runtime: protocol.RuntimeCodex,
				Repositories: []protocol.Repository{}, RetainedWorktrees: []protocol.RetainedWorktree{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	path := filepath.Join(directory, "worker-credential")
	client := newClient(server.URL, server.Client())
	if err := client.enroll(context.Background(), "remote-worker", enrollment, path); err != nil {
		t.Fatal(err)
	}
	stored, err := loadCredentialFile(path, server.URL)
	if err != nil || stored != credential {
		t.Fatalf("stored credential = %q, err %v", stored, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v", info.Mode().Perm())
	}
	if _, err := client.register(context.Background(), "remote-worker", protocol.WorkerRegistration{}); err != nil {
		t.Fatal(err)
	}
	if err := client.enroll(context.Background(), "remote-worker", "wrong", path); err != nil {
		t.Fatalf("saved credential triggered re-enrollment: %v", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want one exchange and one registration", requests)
	}
	if _, err := loadCredentialFile(path, "https://other-factory.example.com:7443"); err == nil {
		t.Fatal("Worker credential was accepted for a different Factory server")
	}
}

func TestLegacyRemoteWorkerCredentialIsAdoptedOnRestart(t *testing.T) {
	const credential = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer "+credential {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.Worker{ID: "remote-worker"})
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "runner-credential")
	if err := writeCredentialFile(legacyPath, server.URL, credential); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyWorkerCredentialFiles(directory, server.URL); err != nil {
		t.Fatal(err)
	}
	workerPath := filepath.Join(directory, "worker-credential")
	stored, err := loadCredentialFile(workerPath, server.URL)
	if err != nil || stored != credential {
		t.Fatalf("adopted credential = %q, err %v", stored, err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy credential remains after adoption: %v", err)
	}
	client := newClient(server.URL, server.Client())
	client.credential = stored
	if _, err := client.register(context.Background(), "remote-worker", protocol.WorkerRegistration{}); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("registration requests = %d, want 1", requests)
	}
}

func TestUnusedLegacyPendingCredentialIsRegenerated(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const legacyCredential = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	requests := 0
	generatedCredential := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input protocol.ExchangeWorkerEnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if input.Credential != legacyCredential {
				t.Errorf("first credential = %q, want legacy pending credential", input.Credential)
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"worker_credential_regeneration_required","message":"regenerate it"}}`)
			return
		}
		if !strings.HasPrefix(input.Credential, "factory_worker_") {
			t.Errorf("regenerated credential = %q", input.Credential)
		}
		generatedCredential = input.Credential
		_ = json.NewEncoder(w).Encode(protocol.WorkerCredential{Credential: input.Credential})
	}))
	t.Cleanup(server.Close)

	directory := t.TempDir()
	legacyPendingPath := filepath.Join(directory, "runner-credential.pending")
	if err := writeCredentialFile(legacyPendingPath, server.URL, legacyCredential); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyWorkerCredentialFiles(directory, server.URL); err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(directory, "worker-credential")
	client := newClient(server.URL, server.Client())
	if err := client.enroll(context.Background(), "remote-worker", enrollment, credentialPath); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("exchange requests = %d, want 2", requests)
	}
	stored, err := loadCredentialFile(credentialPath, server.URL)
	if err != nil || stored != generatedCredential {
		t.Fatalf("stored regenerated credential = %q, err %v", stored, err)
	}
	if _, err := os.Stat(credentialPath + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending credential remains after exchange: %v", err)
	}
}

func TestLegacyCredentialAdoptionDoesNotOverwriteWorkerState(t *testing.T) {
	const legacy = "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const current = "factory_worker_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "runner-credential")
	workerPath := filepath.Join(directory, "worker-credential")
	if err := writeCredentialFile(legacyPath, "https://factory.example.com:7443", legacy); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialFile(workerPath, "https://factory.example.com:7443", current); err != nil {
		t.Fatal(err)
	}
	if err := adoptLegacyWorkerCredentialFiles(directory, "https://factory.example.com:7443"); err != nil {
		t.Fatal(err)
	}
	stored, err := loadCredentialFile(workerPath, "https://factory.example.com:7443")
	if err != nil || stored != current {
		t.Fatalf("current credential = %q, err %v", stored, err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy credential should remain when destination exists: %v", err)
	}
}

func TestLegacyCredentialAdoptionNamesLegacyFileForDifferentServer(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "runner-credential")
	if err := writeCredentialFile(legacyPath, "https://old-factory.example.com:7443", "factory_runner_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}

	err := adoptLegacyWorkerCredentialFiles(directory, "https://new-factory.example.com:7443")
	if err == nil || !strings.Contains(err.Error(), "remove runner-credential") {
		t.Fatalf("adoption error = %v, want recovery instruction for runner-credential", err)
	}
}

func TestRemoteClientRetriesTheSamePendingCredentialAfterResponseLoss(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	requests := 0
	firstCredential := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var input protocol.ExchangeWorkerEnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if requests == 1 {
			firstCredential = input.Credential
			panic(http.ErrAbortHandler)
		}
		if input.Credential != firstCredential {
			t.Errorf("retried credential = %q; want %q", input.Credential, firstCredential)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(protocol.WorkerCredential{Credential: input.Credential})
	}))
	t.Cleanup(server.Close)

	path := filepath.Join(t.TempDir(), "worker-credential")
	first := newClient(server.URL, server.Client())
	if err := first.enroll(context.Background(), "remote-worker", enrollment, path); err == nil {
		t.Fatal("enrollment unexpectedly survived a lost response")
	}
	if _, err := os.Stat(path + ".pending"); err != nil {
		t.Fatalf("pending credential was not preserved: %v", err)
	}
	second := newClient(server.URL, server.Client())
	if err := second.enroll(context.Background(), "remote-worker", enrollment, path); err != nil {
		t.Fatal(err)
	}
	stored, err := loadCredentialFile(path, server.URL)
	if err != nil || stored != firstCredential {
		t.Fatalf("recovered credential = %q, err %v; want %q", stored, err, firstCredential)
	}
	if _, err := os.Stat(path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending credential still exists after recovery: %v", err)
	}
}

func TestManagerRetriesTransientRemoteEnrollmentWithoutRestart(t *testing.T) {
	const enrollment = "factory_enroll_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	exchanges := 0
	registered := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/worker-enrollments/exchange":
			exchanges++
			var input protocol.ExchangeWorkerEnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if exchanges == 1 {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(protocol.WorkerCredential{Credential: input.Credential})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/workers/"):
			select {
			case registered <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(protocol.Worker{})
		case strings.HasSuffix(r.URL.Path, "/claims"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	codexPath := filepath.Join(t.TempDir(), "codex")
	writeFakeCodex(t, codexPath)
	options := testOptions(codexPath)
	options.HTTPClient = server.Client()
	options.TransportBackoffMin = 10 * time.Millisecond
	options.TransportBackoffMax = 20 * time.Millisecond
	manager, err := New(Config{
		Server: server.URL, Name: "remote-enrollment-retry", EnrollmentToken: enrollment,
		Runtime: protocol.RuntimeCodex, MaxConcurrent: 1, DataDirectory: filepath.Join(t.TempDir(), "worker"),
	}, options, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- manager.Run(ctx) }()
	select {
	case <-registered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Manager did not recover from transient enrollment failure")
	}
	if exchanges != 2 {
		cancel()
		t.Fatalf("enrollment exchanges = %d, want 2", exchanges)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Manager stopped after enrollment recovery: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Manager did not stop after cancellation")
	}
}

func TestRemoteClientRemovesStalePendingCredentialAfterCompletedEnrollment(t *testing.T) {
	const credential = "factory_worker_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("completed enrollment unexpectedly contacted the server")
	}))
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "worker-credential")
	if err := writeCredentialFile(path, server.URL, credential); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialFile(path+".pending", server.URL, credential); err != nil {
		t.Fatal(err)
	}
	client := newClient(server.URL, server.Client())
	client.credential = credential
	if err := client.enroll(context.Background(), "remote-worker", "", path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pending credential still exists: %v", err)
	}
}

func TestWorkerCredentialAtomicInstallDoesNotReplaceAnExistingFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "worker-credential")
	const server = "https://factory.example.com:7443"
	const original = "factory_worker_original"
	if err := writeCredentialFile(path, server, original); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialFile(path, server, "factory_worker_replacement"); err == nil {
		t.Fatal("credential writer replaced an existing file")
	}
	stored, err := loadCredentialFile(path, server)
	if err != nil || stored != original {
		t.Fatalf("stored credential = %q, err %v; want original", stored, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("credential directory contains temporary files: %#v", entries)
	}
}

func TestWorkerCredentialRejectsBroadPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker-credential")
	if err := os.WriteFile(path, []byte(`{"server":"https://factory.example.com:7443","credential":"factory_worker_secret"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentialFile(path, "https://factory.example.com:7443"); err == nil {
		t.Fatal("accepted broadly readable Worker credential")
	}
}

func TestStageCompletionRequestIsBoundedAfterJSONEscaping(t *testing.T) {
	input := protocol.CompleteStageRequest{
		LeaseToken: strings.Repeat("a", 64), State: protocol.StageSucceeded,
		Result: strings.Repeat("\x00", protocol.MaxResultBytes),
		Error:  strings.Repeat("\x00", protocol.MaxErrorBytes),
	}
	bounded := boundedStageCompletionRequest(input)
	body, err := json.Marshal(bounded)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > protocol.MaxBodyBytes {
		t.Fatalf("bounded stage completion body = %d bytes", len(body))
	}
	if bounded.LeaseToken != input.LeaseToken || bounded.State != input.State || bounded.Result == "" || bounded.Error == "" {
		t.Fatalf("bounded stage completion lost required fields: %#v", bounded)
	}
	if len(bounded.Result) == len(input.Result) && len(bounded.Error) == len(input.Error) {
		t.Fatal("escaped stage completion payload was not reduced")
	}
}
