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
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestProcedureRunFreezesAllEnabledRepositoriesAndReplaysBeforeChanges(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	web := createManagedRepositoryForProcedure(t, store, "github.com/acme/web")
	api := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	disabled := createManagedRepositoryForProcedure(t, store, "github.com/acme/disabled")
	if _, err := store.SetManagedRepositoryEnabled(ctx, disabled.ID, false); err != nil {
		t.Fatal(err)
	}
	procedure, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Bug-Fix", Prompt: "Find and fix one concrete bug.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 900, ConcurrencyLimit: 2, OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.ProcedureRunRequest{
		RequestKey: "fleet-all", Procedure: "bug-fix", AllRepositories: true,
	}
	admission, err := store.AdmitProcedureRun(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Result != protocol.AdmissionAdmitted || admission.Run.Run.SessionCount != 2 ||
		admission.Run.Run.Task.Generation != procedure.Generation ||
		admission.Run.Run.Task.Prompt != "Find and fix one concrete bug." ||
		admission.Run.Run.Task.TimeoutSeconds != 900 || admission.Run.Run.Task.ConcurrencyLimit != 2 ||
		admission.Run.Run.OutcomeContract != protocol.OutcomeAgentUpdate {
		t.Fatalf("admission = %#v", admission)
	}
	if got := []string{
		admission.Run.Sessions[0].RepositoryIdentity,
		admission.Run.Sessions[1].RepositoryIdentity,
	}; got[0] != api.RemoteIdentity || got[1] != web.RemoteIdentity {
		t.Fatalf("frozen all order = %#v", got)
	}
	for index, work := range admission.Run.Sessions {
		if work.Target.Position != index || work.Target.TargetKind != "repository" ||
			work.Target.TargetKey != "repository:"+work.RepositoryID ||
			work.Target.SourceReference != work.RepositoryIdentity || work.Target.PublishBranch == "" {
			t.Fatalf("Work %d = %#v", index, work)
		}
	}
	if _, err := store.UpdateTask(ctx, procedure.ID, protocol.SaveTaskRequest{
		Name: procedure.Name, Prompt: "Changed instructions.", Runtime: protocol.RuntimePi,
		TimeoutSeconds: 1200, ConcurrencyLimit: 1, ExpectedGeneration: procedure.Generation,
		OutcomeContract: protocol.OutcomeAgentUpdate,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(ctx, api.ID, false); err != nil {
		t.Fatal(err)
	}
	replayed, err := store.AdmitProcedureRun(ctx, request)
	if err != nil || replayed.Result != protocol.AdmissionReplayed ||
		replayed.Run.Run.ID != admission.Run.Run.ID || replayed.Run.Run.Task.Runtime != protocol.RuntimeCodex ||
		len(replayed.Run.Run.Targets) != 2 {
		t.Fatalf("replay = %#v, err %v", replayed, err)
	}
	request.Repositories, request.AllRepositories = []string{web.RemoteIdentity}, false
	if _, err := store.AdmitProcedureRun(ctx, request); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("changed fingerprint error = %v", err)
	}
}

func TestProcedureRunPreservesExplicitOrderAndIsAtomic(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	api := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	web := createManagedRepositoryForProcedure(t, store, "github.com/acme/web")
	procedure, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Review", Prompt: "Review this repository.", Runtime: protocol.RuntimeCodex,
		OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := store.AdmitProcedureRun(ctx, protocol.ProcedureRunRequest{
		RequestKey: "explicit-fleet", Procedure: procedure.Name,
		Repositories: []string{web.RemoteIdentity, api.RemoteIdentity},
	})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Run.Sessions[0].RepositoryID != web.ID || admission.Run.Sessions[1].RepositoryID != api.ID {
		t.Fatalf("explicit order = %#v", admission.Run.Sessions)
	}
	if _, err := store.AdmitProcedureRun(ctx, protocol.ProcedureRunRequest{
		RequestKey: "invalid-fleet", Procedure: procedure.Name,
		Repositories: []string{api.RemoteIdentity, "github.com/acme/missing"},
	}); !serviceErrorCode(err, "repository_not_managed") {
		t.Fatalf("invalid fleet error = %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE request_key = 'invalid-fleet'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial invalid fleet count = %d, err %v", count, err)
	}
}

func TestProcedureRunRebuildUsesLatestExactProcedureRepositoryPredecessor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	first := createProcedureForTest(t, store, "Bug fix")
	other := createProcedureForTest(t, store, "Documentation")
	terminalRun := admitProcedureForTest(t, store, "terminal", first.Name, repository.RemoteIdentity, false)
	otherRun := admitProcedureForTest(t, store, "other", other.Name, repository.RemoteIdentity, false)
	const terminalID = "zzzz-terminal-work"
	if _, err := store.db.Exec(`UPDATE sessions SET id = ? WHERE id = ?`, terminalID, terminalRun.Run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, workID := range []string{terminalID, otherRun.Run.Sessions[0].ID} {
		if _, err := store.db.Exec(`
			UPDATE sessions SET state = 'failed', terminal_at = admitted_at, terminal_message = 'failed'
			WHERE id = ?
		`, workID); err != nil {
			t.Fatal(err)
		}
	}
	rebuilt := admitProcedureForTest(t, store, "rebuilt", first.Name, repository.RemoteIdentity, true)
	if got := rebuilt.Run.Sessions[0].PredecessorWorkID; got != terminalID {
		t.Fatalf("predecessor = %q, want %q", got, terminalID)
	}
	if _, err := store.AdmitProcedureRun(ctx, protocol.ProcedureRunRequest{
		RequestKey: "active-rebuild", Procedure: first.Name,
		Repositories: []string{repository.RemoteIdentity}, Rebuild: true,
	}); !serviceErrorCode(err, "procedure_rebuild_active") {
		t.Fatalf("active rebuild error = %v", err)
	}
	const rebuiltID = "aaaa-rebuilt-work"
	if _, err := store.db.Exec(`
		UPDATE sessions SET id = ?, state = 'failed', admitted_at = 1000,
		       terminal_at = 1000, terminal_message = 'failed'
		WHERE id = ?
	`, rebuiltID, rebuilt.Run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE sessions SET admitted_at = 1000, terminal_at = 1000 WHERE id = ?`, terminalID); err != nil {
		t.Fatal(err)
	}
	newest := admitProcedureForTest(t, store, "newest", first.Name, repository.RemoteIdentity, true)
	if got := newest.Run.Sessions[0].PredecessorWorkID; got != rebuiltID {
		t.Fatalf("latest predecessor = %q, want %q", got, rebuiltID)
	}
}

func TestClaimSchedulingAlternatesEligibleRuns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	api := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	web := createManagedRepositoryForProcedure(t, store, "github.com/acme/web")
	worker := registerTestWorker(t, store, workerA, 10,
		protocol.RepositoryRegistration{Key: "api", RemoteIdentity: api.RemoteIdentity},
		protocol.RepositoryRegistration{Key: "web", RemoteIdentity: web.RemoteIdentity},
	)
	large := createProcedureForTest(t, store, "Large fleet")
	small := createProcedureForTest(t, store, "Small fleet")
	largeRun, err := store.AdmitProcedureRun(ctx, protocol.ProcedureRunRequest{
		RequestKey: "large", Procedure: large.Name,
		Repositories: []string{api.RemoteIdentity, web.RemoteIdentity},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	smallRun := admitProcedureForTest(t, store, "small", small.Name, api.RemoteIdentity, false)
	now = now.Add(time.Second)
	first, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "fair-1", LeaseToken: strings.Repeat("a", 64),
	})
	if err != nil || first == nil || first.Session.RunID != largeRun.Run.Run.ID {
		t.Fatalf("first claim = %#v, err %v", first, err)
	}
	now = now.Add(time.Second)
	second, err := store.Claim(ctx, worker.ID, protocol.ClaimRequest{
		RequestID: "fair-2", LeaseToken: strings.Repeat("b", 64),
	})
	if err != nil || second == nil || second.Session.RunID != smallRun.Run.Run.ID {
		t.Fatalf("second claim = %#v, err %v", second, err)
	}
}

func TestFakeCloudSchedulingAlternatesEligibleRuns(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	api := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	web := createManagedRepositoryForProcedure(t, store, "github.com/acme/web")
	profile := createFakeProfile(t, store, "Fleet cloud", protocol.RuntimeCodex, "succeeded")
	createCloudProcedure := func(name string) protocol.Task {
		procedure, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
			Name: name, Prompt: "Review this repository.", Runtime: protocol.RuntimeCodex,
			ExecutionProfileID: profile.ID, OutcomeContract: protocol.OutcomeProcessExit,
			ConcurrencyLimit: 10,
		})
		if err != nil {
			t.Fatal(err)
		}
		return procedure
	}
	large := createCloudProcedure("Large cloud fleet")
	small := createCloudProcedure("Small cloud fleet")
	largeRun, err := store.AdmitProcedureRun(ctx, protocol.ProcedureRunRequest{
		RequestKey: "large-cloud", Procedure: large.Name,
		Repositories: []string{api.RemoteIdentity, web.RemoteIdentity},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	smallRun := admitProcedureForTest(t, store, "small-cloud", small.Name, api.RemoteIdentity, false)
	for iteration := 0; iteration < 2; iteration++ {
		now = now.Add(time.Second)
		if processed, err := store.DispatchFakeCloud(ctx, 1); err != nil || processed != 1 {
			t.Fatalf("dispatch %d = %d, err %v", iteration, processed, err)
		}
	}
	largeDetail, err := store.Run(ctx, largeRun.Run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	smallDetail, err := store.Run(ctx, smallRun.Run.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	largeAttempts := len(largeDetail.Sessions[0].Attempts) + len(largeDetail.Sessions[1].Attempts)
	if largeAttempts != 1 || len(smallDetail.Sessions[0].Attempts) != 1 {
		t.Fatalf("fair fake-cloud attempts = large %d, small %d", largeAttempts, len(smallDetail.Sessions[0].Attempts))
	}
}

func TestProcedureRunHTTPReturnsTypedAdmissionAndProcedures(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := createManagedRepositoryForProcedure(t, store, "github.com/acme/api")
	procedure := createProcedureForTest(t, store, "Bug fix")
	server := httptest.NewServer(NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(server.Close)
	listRequest, err := http.NewRequestWithContext(
		ctx, http.MethodGet, server.URL+"/api/v1/procedures?limit=200", nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(listRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var page protocol.ProcedurePage
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&page) != nil ||
		len(page.Procedures) != 1 || page.Procedures[0].Name != procedure.Name {
		t.Fatalf("Procedure page = %d %#v", response.StatusCode, page)
	}
	body, _ := json.Marshal(protocol.ProcedureRunRequest{
		RequestKey: "http-fleet", Procedure: procedure.Name,
		Repositories: []string{repository.RemoteIdentity},
	})
	runRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, server.URL+"/api/v1/procedure-runs", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	runRequest.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(runRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var admission protocol.ProcedureRunAdmission
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&admission) != nil ||
		admission.Result != protocol.AdmissionAdmitted || admission.Run.Run.SessionCount != 1 {
		t.Fatalf("Procedure admission = %d %#v", response.StatusCode, admission)
	}
}

func createManagedRepositoryForProcedure(t *testing.T, store *Store, identity string) protocol.ManagedRepository {
	t.Helper()
	repository, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func createProcedureForTest(t *testing.T, store *Store, name string) protocol.Task {
	t.Helper()
	procedure, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: name, Prompt: "Complete one repository review.", Runtime: protocol.RuntimeCodex,
		OutcomeContract: protocol.OutcomeAgentUpdate,
	})
	if err != nil {
		t.Fatal(err)
	}
	return procedure
}

func admitProcedureForTest(
	t *testing.T,
	store *Store,
	requestKey, procedure, repository string,
	rebuild bool,
) protocol.ProcedureRunAdmission {
	t.Helper()
	admission, err := store.AdmitProcedureRun(context.Background(), protocol.ProcedureRunRequest{
		RequestKey: requestKey, Procedure: procedure, Repositories: []string{repository}, Rebuild: rebuild,
	})
	if err != nil {
		t.Fatal(err)
	}
	return admission
}
