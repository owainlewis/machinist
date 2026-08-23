package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestBuildAdmissionHTTPReturnsTypedCommitStatus(t *testing.T) {
	store := newTestStore(t)
	if _, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/acme/api",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	post := func(request protocol.BuildRequest) (*http.Response, []byte) {
		t.Helper()
		body, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Post(server.URL+"/api/v1/builds", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		encoded, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, encoded
	}
	request := protocol.BuildRequest{
		RequestKey: "http-build", RepositorySpecified: true, Repository: "github.com/acme/api",
		References: []string{"https://github.com/acme/api/issues/1", "LINEAR-2"},
	}
	response, encoded := post(request)
	var admission protocol.BuildAdmission
	if response.StatusCode != http.StatusCreated || json.Unmarshal(encoded, &admission) != nil ||
		admission.Result != protocol.AdmissionAdmitted || admission.Run.Run.SessionCount != 2 {
		t.Fatalf("created HTTP response = %d %s", response.StatusCode, encoded)
	}
	response, encoded = post(request)
	if response.StatusCode != http.StatusOK || json.Unmarshal(encoded, &admission) != nil ||
		admission.Result != protocol.AdmissionReplayed {
		t.Fatalf("replayed HTTP response = %d %s", response.StatusCode, encoded)
	}
	request.Rebuild = true
	response, encoded = post(request)
	var failure protocol.ErrorBody
	if response.StatusCode != http.StatusConflict || json.Unmarshal(encoded, &failure) != nil ||
		failure.Error.AdmissionResult != protocol.AdmissionRejectedBeforeCommit ||
		failure.Error.RequestKey != request.RequestKey {
		t.Fatalf("rejected HTTP response = %d %s", response.StatusCode, encoded)
	}
	mismatch := protocol.BuildRequest{
		RequestKey: "http-mismatch", RepositorySpecified: true, Repository: "github.com/acme/api",
		References: []string{"https://github.com/acme/other/issues/3"},
	}
	response, encoded = post(mismatch)
	if response.StatusCode != http.StatusBadRequest || json.Unmarshal(encoded, &failure) != nil ||
		failure.Error.AdmissionResult != protocol.AdmissionRejectedBeforeCommit {
		t.Fatalf("mismatched HTTP response = %d %s", response.StatusCode, encoded)
	}
}

func TestStandardBuildBackingProcedureDoesNotCollideWithExistingTaskName(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.db.Exec(`DELETE FROM tasks WHERE id = ?`, protocol.StandardBuildProcedureID); err != nil {
		t.Fatal(err)
	}
	userTask, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "__factory_builtin_standard_build__", Prompt: "User-owned prompt.",
		Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureStandardBuildProcedure(context.Background()); err != nil {
		t.Fatalf("install built-in beside existing Task: %v", err)
	}
	var userPrompt, userKey, builtInKey string
	if err := store.db.QueryRow(`SELECT prompt, name_key FROM tasks WHERE id = ?`, userTask.ID).Scan(&userPrompt, &userKey); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT name_key FROM tasks WHERE id = ?`, protocol.StandardBuildProcedureID).Scan(&builtInKey); err != nil {
		t.Fatal(err)
	}
	if userPrompt != "User-owned prompt." || userKey != "__factory_builtin_standard_build__" ||
		builtInKey == userKey {
		t.Fatalf("user prompt %q, user key %q, built-in key %q", userPrompt, userKey, builtInKey)
	}
}

func TestBuildAdmissionCreatesIndependentAtomicWorkAndSchedulerClaimsIt(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/owainlewis/factory",
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: repository.RemoteIdentity,
	})
	admission, err := store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "mixed-build", RepositorySpecified: true,
		Repository: repository.RemoteIdentity,
		References: []string{
			"https://github.com/OWAINLEWIS/FACTORY/issues/341",
			"LINEAR-123",
			"LINEAR-124",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Result != protocol.AdmissionAdmitted || admission.Run.Run.SessionCount != 3 ||
		admission.Run.Run.Task.ID != protocol.StandardBuildProcedureID ||
		admission.Run.Run.Task.Generation != protocol.StandardBuildProcedureGeneration ||
		admission.Run.Run.Task.Runtime != protocol.RuntimeCodex ||
		admission.Run.Run.OutcomeContract != protocol.OutcomeAgentUpdate {
		t.Fatalf("admission = %#v", admission)
	}
	seenBranches := map[string]bool{}
	for index, work := range admission.Run.Sessions {
		if work.Target.Position != index || work.Target.TargetKind != "work_item" ||
			work.Target.RepositoryID != repository.ID || work.Target.PublishBranch == "" ||
			!strings.Contains(work.ResolvedPrompt, "Untrusted work-item context:") ||
			!strings.Contains(work.ResolvedPrompt, protocol.StandardBuildProcedurePrompt) {
			t.Fatalf("Work %d = %#v", index, work)
		}
		if seenBranches[work.Target.PublishBranch] {
			t.Fatalf("duplicate publish branch %q", work.Target.PublishBranch)
		}
		seenBranches[work.Target.PublishBranch] = true
	}
	if got := admission.Run.Sessions[0].Target.SourceKey; got != "github:owainlewis/factory:issue:341" {
		t.Fatalf("GitHub source key = %q", got)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "build-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, error %v", claim, err)
	}
	if claim.Session.RunID != admission.Run.Run.ID || claim.Session.Target.TargetKind != "work_item" ||
		claim.Session.OutcomeContract != protocol.OutcomeAgentUpdate {
		t.Fatalf("claimed Build Work = %#v", claim.Session)
	}
}

func TestBuildAdmissionRejectsWholeInvalidBatchAndDuplicates(t *testing.T) {
	store := newTestStore(t)
	first, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/acme/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/acme/web",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "mismatch", RepositorySpecified: true, Repository: first.RemoteIdentity,
		References: []string{"LINEAR-1", "https://github.com/acme/web/issues/2"},
	})
	if !serviceErrorCode(err, "invalid_build") {
		t.Fatalf("repository mismatch error = %v", err)
	}
	var runs int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = 'mismatch'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("mismatch Runs = %d, error %v", runs, err)
	}
	_, err = store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "duplicate", References: []string{
			"https://github.com/acme/api/issues/7",
			"https://github.com/ACME/API/issues/007",
		},
	})
	if !serviceErrorCode(err, "duplicate_build_target") {
		t.Fatalf("duplicate target error = %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = 'duplicate'`).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("duplicate Runs = %d, error %v", runs, err)
	}
}

func TestBuildAdmissionReplayWinsAndRebuildSelectsLatestTerminalPredecessor(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/acme/api",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.BuildRequest{
		RequestKey: "initial", References: []string{"https://github.com/acme/api/issues/9"},
	}
	initial, err := store.AdmitBuild(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE sessions SET state = 'failed', terminal_at = admitted_at, terminal_message = 'failed'
		WHERE id = ?
	`, initial.Run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 0 WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultBuildRuntime(protocol.RuntimePi); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AdmitBuild(context.Background(), request)
	if err != nil || replayed.Result != protocol.AdmissionReplayed || replayed.Run.Run.ID != initial.Run.Run.ID ||
		replayed.Run.Run.Task.Runtime != protocol.RuntimeCodex {
		t.Fatalf("replay = %#v, error %v", replayed, err)
	}
	request.Runtime, request.RuntimeSpecified = protocol.RuntimePi, true
	if _, err := store.AdmitBuild(context.Background(), request); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("fingerprint conflict = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE repositories SET enabled = 1 WHERE id = ?`, repository.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "without-rebuild", References: []string{"https://github.com/acme/api/issues/9"},
	}); !serviceErrorCode(err, "rebuild_required") {
		t.Fatalf("missing rebuild error = %v", err)
	}
	rebuilt, err := store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "rebuilt", Rebuild: true,
		References: []string{"https://github.com/acme/api/issues/9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Run.Sessions[0].PredecessorWorkID != initial.Run.Sessions[0].ID ||
		rebuilt.Run.Run.Task.Runtime != protocol.RuntimePi {
		t.Fatalf("rebuilt = %#v", rebuilt)
	}
	if _, err := store.AdmitBuild(context.Background(), protocol.BuildRequest{
		RequestKey: "duplicate-active", Rebuild: true,
		References: []string{"https://github.com/acme/api/issues/9"},
	}); !serviceErrorCode(err, "duplicate_build_active") {
		t.Fatalf("active duplicate error = %v", err)
	}
}
