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
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestExecutionProfileAndManualOverrideAPI(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createProfileTask(t, store, worker.Repositories[0].ID, "")
	handler := NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	doJSON := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequestWithContext(context.Background(), method, "http://localhost"+path, bytes.NewReader(encoded))
		request.Host = "localhost"
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	profileResponse := doJSON(http.MethodPost, "/api/v1/execution-profiles", protocol.SaveExecutionProfileRequest{
		Name: "API cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 600,
		ResourceClass: "standard", MaxConcurrent: 2, Enabled: true, Healthy: true,
		FakeOutcome: "succeeded",
	})
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("create profile status %d: %s", profileResponse.Code, profileResponse.Body.String())
	}
	var profile protocol.ExecutionProfile
	if err := json.Unmarshal(profileResponse.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	runResponse := doJSON(http.MethodPost, "/api/v1/tasks/"+task.ID+"/run", protocol.RunTaskRequest{
		RequestKey: "api-cloud-override", ExecutionProfileID: profile.ID,
	})
	if runResponse.Code != http.StatusCreated {
		t.Fatalf("run status %d: %s", runResponse.Code, runResponse.Body.String())
	}
	var run protocol.RunDetail
	if err := json.Unmarshal(runResponse.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Run.Execution.ProfileID != profile.ID || run.Sessions[0].AssignedWorkerID != profile.SyntheticWorkerID {
		t.Fatalf("API override Run = %#v", run)
	}
}

func createFakeProfile(t *testing.T, store *Store, name, runtime, outcome string) protocol.ExecutionProfile {
	t.Helper()
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: name, Kind: protocol.BackendFakeCloudRun, Runtime: runtime,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 900,
		ResourceClass: "1cpu-2gib", MaxConcurrent: 4, Enabled: true, Healthy: true,
		FakeOutcome: outcome,
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func createProfileTask(t *testing.T, store *Store, repositoryID, profileID string) protocol.Task {
	t.Helper()
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Profile routing", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 2, RepositoryIDs: []string{repositoryID},
		ExecutionProfileID: profileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestExecutionProfileManualOverrideUsesExistingLifecycle(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Cloud burst", protocol.RuntimeCodex, "succeeded")
	task := createProfileTask(t, store, worker.Repositories[0].ID, "")

	persistent, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "persistent-default"})
	if err != nil {
		t.Fatal(err)
	}
	if persistent.Run.Execution.ProfileID != protocol.PersistentAutoProfileID ||
		persistent.Sessions[0].AssignedWorkerID != worker.ID || persistent.Run.State != protocol.RunQueued {
		t.Fatalf("persistent default = %#v", persistent)
	}

	cloud, created, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "cloud-override", ExecutionProfileID: profile.ID,
	})
	if err != nil || !created {
		t.Fatalf("cloud admission = %#v, created %v, err %v", cloud, created, err)
	}
	if cloud.Run.Execution.ProfileID != profile.ID || cloud.Run.Execution.ProfileVersion != 1 ||
		cloud.Run.Execution.Backend != protocol.BackendFakeCloudRun ||
		cloud.Sessions[0].AssignedWorkerID != profile.SyntheticWorkerID || cloud.Run.State != protocol.RunQueued {
		t.Fatalf("cloud override = %#v", cloud)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	cloud, err = store.Run(context.Background(), cloud.Run.ID)
	if err != nil || cloud.Run.State != protocol.RunSucceeded || len(cloud.Sessions[0].Attempts) != 1 ||
		cloud.Sessions[0].Attempts[0].WorkerID != profile.SyntheticWorkerID {
		t.Fatalf("completed cloud lifecycle = %#v, err %v", cloud, err)
	}

	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "persistent-claim", LeaseToken: tokenA})
	if err != nil || claim == nil || claim.Session.RunID != persistent.Run.ID {
		t.Fatalf("persistent claim after cloud run = %#v, err %v", claim, err)
	}
}

func TestExecutionProfileRunReplayIncludesManualOverride(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Replay cloud", protocol.RuntimeCodex, "succeeded")
	task := createProfileTask(t, store, worker.Repositories[0].ID, "")
	input := protocol.RunTaskRequest{RequestKey: "profile-replay", ExecutionProfileID: profile.ID}
	created, wasCreated, err := store.RunTask(context.Background(), task.ID, input)
	if err != nil || !wasCreated {
		t.Fatalf("create = %#v, %v, %v", created, wasCreated, err)
	}
	replayed, wasCreated, err := store.RunTask(context.Background(), task.ID, input)
	if err != nil || wasCreated || replayed.Run.ID != created.Run.ID {
		t.Fatalf("replay = %#v, %v, %v", replayed, wasCreated, err)
	}
	if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "profile-replay", ExecutionProfileID: protocol.PersistentAutoProfileID,
	}); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("changed override replay error = %v", err)
	}
}

func TestPersistentAutoManualOverrideBeatsCloudTaskDefault(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Cloud default", protocol.RuntimeCodex, "succeeded")
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Persistent override", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 1, RepositoryIDs: []string{worker.Repositories[0].ID},
		ExecutionProfileID: profile.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "persistent-override", ExecutionProfileID: protocol.PersistentAutoProfileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Run.Execution.ProfileID != protocol.PersistentAutoProfileID ||
		run.Run.Execution.Backend != protocol.BackendPersistent || run.Sessions[0].AssignedWorkerID != worker.ID {
		t.Fatalf("persistent manual override = %#v", run)
	}
}

func TestFakeCloudRetryReusesFrozenProfileVersion(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Immutable cloud", protocol.RuntimeCodex, "failed")
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "frozen-profile"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunFailed || len(run.Sessions[0].Attempts) != 1 {
		t.Fatalf("first failed Attempt = %#v", run)
	}
	if processed, err := store.DispatchFakeCloud(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("native retry occurred: processed %d, err %v", processed, err)
	}

	disabled, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: false, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: profile.Version,
	})
	if err != nil || disabled.Version != 2 {
		t.Fatalf("disabled profile update = %#v, err %v", disabled, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, run.Sessions[0].ID); !serviceErrorCode(err, "execution_profile_version_unavailable") {
		t.Fatalf("retry with disabled profile error = %v", err)
	}
	updated, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: "deepseek/new", TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: "succeeded",
		ExpectedVersion: disabled.Version,
	})
	if err != nil || updated.Version != 3 {
		t.Fatalf("profile update = %#v, err %v", updated, err)
	}
	if _, err := store.RetrySession(context.Background(), run.Run.ID, run.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.Execution.ProfileVersion != 1 || run.Run.Execution.Model != profile.Model ||
		run.Run.State != protocol.RunFailed || len(run.Sessions[0].Attempts) != 2 {
		t.Fatalf("retry did not reuse frozen version = %#v", run)
	}

	newRun, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "new-profile-version"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	newRun, _ = store.Run(context.Background(), newRun.Run.ID)
	if newRun.Run.Execution.ProfileVersion != 3 || newRun.Run.Execution.Model != "deepseek/new" ||
		newRun.Run.State != protocol.RunSucceeded {
		t.Fatalf("new Run did not freeze new version = %#v", newRun)
	}
}

func TestFakeCloudDoesNotStartQueuedRunWhileProfileIsUnready(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Dispatch health", protocol.RuntimeCodex, "succeeded")
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "dispatch-health"})
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: false, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: profile.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := store.DispatchFakeCloud(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("disabled dispatch = %d, err %v", processed, err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunQueued || len(run.Sessions[0].Attempts) != 0 {
		t.Fatalf("Run started while profile disabled = %#v", run)
	}
	if _, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: profile.FakeOutcome,
		ExpectedVersion: disabled.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunSucceeded || run.Run.Execution.ProfileVersion != 1 ||
		len(run.Sessions[0].Attempts) != 1 {
		t.Fatalf("re-enabled frozen Run = %#v", run)
	}
}

func TestFakeCloudDoesNotReleaseOrRetryDisabledRepository(t *testing.T) {
	store := newTestStore(t)
	repository, _, err := store.CreateManagedRepository(context.Background(), protocol.CreateManagedRepositoryRequest{
		RemoteIdentity: "github.com/owainlewis/factory",
	})
	if err != nil {
		t.Fatal(err)
	}
	profile := createFakeProfile(t, store, "Repository gate", protocol.RuntimeCodex, "failed")
	task := createProfileTask(t, store, repository.ID, profile.ID)

	queued, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "disable-before-dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	if processed, err := store.DispatchFakeCloud(context.Background(), 10); err != nil || processed != 0 {
		t.Fatalf("disabled repository release = %d, err %v", processed, err)
	}
	queued, _ = store.Run(context.Background(), queued.Run.ID)
	if queued.Run.State != protocol.RunBlocked || queued.Sessions[0].BlockedReason != "Repository is disabled." ||
		len(queued.Sessions[0].Attempts) != 0 {
		t.Fatalf("disabled queued Run = %#v", queued)
	}

	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, true); err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "disable-before-retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	failed, _ = store.Run(context.Background(), failed.Run.ID)
	if failed.Run.State != protocol.RunFailed {
		t.Fatalf("failed Run before retry = %#v", failed)
	}
	if _, err := store.SetManagedRepositoryEnabled(context.Background(), repository.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), failed.Run.ID, failed.Sessions[0].ID); !serviceErrorCode(err, "repository_not_available") {
		t.Fatalf("retry with disabled repository error = %v", err)
	}
}

func TestFakeCloudCancellationIsFactoryOwned(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Cancellable cloud", protocol.RuntimeCodex, "running")
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "cancel-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunRunning || run.Sessions[0].Attempts[0].State != "running" {
		t.Fatalf("running fake Attempt = %#v", run)
	}
	if _, err := store.CancelRun(context.Background(), run.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunCancelled || run.Sessions[0].Attempts[0].State != "cancelled" ||
		run.Sessions[0].TerminalMessage != "Cancelled by operator." {
		t.Fatalf("cancelled fake Attempt = %#v", run)
	}
}

func TestFakeCloudRunningAttemptHonorsFrozenTimeout(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "Timed cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "deepseek/test", TimeoutSeconds: 1,
		ResourceClass: "1cpu-2gib", MaxConcurrent: 1, Enabled: true, Healthy: true,
		FakeOutcome: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "timeout-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunRunning || run.Sessions[0].TimeoutSeconds != 1 {
		t.Fatalf("running fake Attempt = %#v", run)
	}

	now = now.Add(2 * time.Second)
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunFailed || run.Sessions[0].FailureReason != fakeCloudTimeoutReason ||
		run.Sessions[0].TerminalMessage != fakeCloudTimeoutReason ||
		len(run.Sessions[0].Attempts) != 1 || run.Sessions[0].Attempts[0].State != "failed" {
		t.Fatalf("timed-out fake Attempt = %#v", run)
	}
}

func TestFakeCloudRunningAttemptSurvivesStartupLeaseSweep(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Restarted cloud", protocol.RuntimeCodex, "running")
	task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "restart-cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunRunning || len(run.Sessions[0].Attempts) != 1 {
		t.Fatalf("running fake Attempt = %#v", run)
	}

	now = now.Add(protocol.LeaseDuration + time.Second)
	expired, err := store.SweepExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 0 {
		t.Fatalf("startup sweep expired synthetic Attempts: %#v", expired)
	}
	if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	if run.Run.State != protocol.RunRunning || run.Sessions[0].Attempts[0].State != "running" ||
		!run.Sessions[0].Attempts[0].LeaseExpiresAt.After(now) {
		t.Fatalf("recovered fake Attempt = %#v", run)
	}
}

func TestStartupLeaseSweepStillExpiresPersistentAttempts(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task := createProfileTask(t, store, worker.Repositories[0].ID, "")
	run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "expire-persistent"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "expire-persistent", LeaseToken: tokenA,
	})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.StartStage(context.Background(), claim.Attempt.ID, 0, protocol.StartStageRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}

	now = now.Add(protocol.LeaseDuration + time.Second)
	expired, err := store.SweepExpired(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].AttemptID != claim.Attempt.ID {
		t.Fatalf("expired persistent Attempts = %#v", expired)
	}
	run, _ = store.Run(context.Background(), run.Run.ID)
	work, workErr := store.Work(context.Background(), run.Sessions[0].ID)
	if workErr != nil {
		t.Fatal(workErr)
	}
	if run.Run.State != protocol.RunFailed || run.Sessions[0].Attempts[0].State != "lost" ||
		run.Sessions[0].FailureReason != "lease expired" || len(run.Sessions[0].Stages) != 1 ||
		run.Sessions[0].Stages[0].State != protocol.StageFailed || work.Stages[0].Error != "lease expired" {
		t.Fatalf("expired persistent Run = %#v", run)
	}
}

func TestUnhealthyAndIncompatibleProfilesBlockWithoutAttempt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	healthReason := strings.Repeat("🧪", protocol.MaxWaitingReasonBytes/2+1)
	unhealthy, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
		Name: "Unhealthy cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
		Provider: "openrouter", Model: "test", Enabled: true, Healthy: false, HealthReason: healthReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	incompatible := createFakeProfile(t, store, "Pi only cloud", protocol.RuntimePi, "succeeded")
	task := createProfileTask(t, store, worker.Repositories[0].ID, "")
	for _, test := range []struct {
		key, profile, reason string
	}{
		{key: "unhealthy", profile: unhealthy.ID, reason: healthReason},
		{key: "incompatible", profile: incompatible.ID, reason: "does not support runtime codex"},
	} {
		run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
			RequestKey: test.key, ExecutionProfileID: test.profile,
		})
		if err != nil {
			t.Fatal(err)
		}
		if run.Run.State != protocol.RunBlocked || len(run.Sessions[0].Attempts) != 0 ||
			run.Sessions[0].AssignedWorkerID != "" || !strings.Contains(run.Sessions[0].BlockedReason, test.reason) {
			t.Fatalf("blocked profile Run = %#v", run)
		}
		if len([]byte(run.Sessions[0].WaitingReason)) > protocol.MaxWaitingReasonBytes ||
			!utf8.ValidString(run.Sessions[0].WaitingReason) {
			t.Fatalf("waiting reason bytes=%d valid=%v", len([]byte(run.Sessions[0].WaitingReason)),
				utf8.ValidString(run.Sessions[0].WaitingReason))
		}
	}
}

func TestFakeCloudProfileBlockedRunRecoversWhenProfileIsReady(t *testing.T) {
	for _, test := range []struct {
		name          string
		enabled       bool
		healthy       bool
		healthReason  string
		blockedReason string
	}{
		{name: "disabled", enabled: false, healthy: true, blockedReason: "is disabled"},
		{name: "unhealthy", enabled: true, healthy: false, healthReason: "model secret missing", blockedReason: "model secret missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
			})
			profile, err := store.CreateExecutionProfile(context.Background(), protocol.SaveExecutionProfileRequest{
				Name: "Recovering cloud", Kind: protocol.BackendFakeCloudRun, Runtime: protocol.RuntimeCodex,
				Provider: "openrouter", Model: "deepseek/frozen", TimeoutSeconds: 900,
				ResourceClass: "1cpu-2gib", MaxConcurrent: 1, Enabled: test.enabled, Healthy: test.healthy,
				HealthReason: test.healthReason, FakeOutcome: "succeeded", FakeResult: "frozen result",
			})
			if err != nil {
				t.Fatal(err)
			}
			task := createProfileTask(t, store, worker.Repositories[0].ID, profile.ID)
			run, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "recover-cloud"})
			if err != nil {
				t.Fatal(err)
			}
			if run.Run.State != protocol.RunBlocked || len(run.Sessions[0].Attempts) != 0 ||
				!strings.Contains(run.Sessions[0].BlockedReason, test.blockedReason) {
				t.Fatalf("profile-blocked Run = %#v", run)
			}

			updated, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
				Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
				Model: "deepseek/current", TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
				MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: true, FakeOutcome: "failed",
				FakeError: "new version result", ExpectedVersion: profile.Version,
			})
			if err != nil || updated.Version != 2 {
				t.Fatalf("profile recovery = %#v, err %v", updated, err)
			}
			if _, err := store.DispatchFakeCloud(context.Background(), 10); err != nil {
				t.Fatal(err)
			}
			run, _ = store.Run(context.Background(), run.Run.ID)
			if run.Run.State != protocol.RunSucceeded || run.Run.Execution.ProfileVersion != 1 ||
				run.Run.Execution.Model != "deepseek/frozen" || run.Sessions[0].Result != "frozen result" ||
				len(run.Sessions[0].Attempts) != 1 {
				t.Fatalf("recovered frozen Run = %#v", run)
			}
		})
	}
}

func TestScheduledRunUsesSavedExecutionProfile(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	profile := createFakeProfile(t, store, "Scheduled cloud", protocol.RuntimeCodex, "succeeded")
	_, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Scheduled profile", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExecutionProfileID: profile.ID,
		Schedule: protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := store.AdmitDueTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.RunPage(context.Background(), 10, "")
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Source != "schedule" ||
		page.Runs[0].Execution.ProfileID != profile.ID {
		t.Fatalf("scheduled profile Run = %#v, err %v", page, err)
	}
}

func TestSyntheticWorkerCannotUseWorkerRoutes(t *testing.T) {
	store := newTestStore(t)
	profile := createFakeProfile(t, store, "Isolated cloud", protocol.RuntimeCodex, "succeeded")
	worker, err := store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || !worker.Synthetic || worker.ID != profile.SyntheticWorkerID {
		t.Fatalf("synthetic Worker = %#v, err %v", worker, err)
	}
	if _, err := store.CreateWorkerEnrollment(context.Background(), worker.ID); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic enrollment error = %v", err)
	}
	if _, err := store.RegisterWorker(context.Background(), worker.ID, protocol.WorkerRegistration{
		Name: "spoof", WorkerVersion: "test", ClaimProtocolVersion: protocol.ClaimProtocolVersion,
		Runtime: protocol.RuntimeCodex, RuntimeVersion: "test", Capacity: 1, Health: "healthy",
	}); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic registration error = %v", err)
	}
	if _, err := store.HeartbeatWorker(context.Background(), worker.ID); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic heartbeat error = %v", err)
	}
	if _, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "synthetic-claim", LeaseToken: tokenA,
	}); !serviceErrorCode(err, "synthetic_worker_isolated") {
		t.Fatalf("synthetic claim error = %v", err)
	}
}

func TestOverviewUsesSyntheticWorkerHealthForOnlineState(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	profile := createFakeProfile(t, store, "Overview cloud", protocol.RuntimeCodex, "succeeded")
	now = now.Add(protocol.WorkerOnlineWindow + time.Second)
	worker, err := store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || !worker.Online {
		t.Fatalf("healthy synthetic Worker = %#v, err %v", worker, err)
	}
	overview, err := store.Overview(context.Background())
	if err != nil || overview.WorkersTotal != 1 || overview.WorkersOnline != 1 {
		t.Fatalf("healthy synthetic Overview = %#v, err %v", overview, err)
	}

	if _, err := store.UpdateExecutionProfile(context.Background(), profile.ID, protocol.SaveExecutionProfileRequest{
		Name: profile.Name, Kind: profile.Kind, Runtime: profile.Runtime, Provider: profile.Provider,
		Model: profile.Model, TimeoutSeconds: profile.TimeoutSeconds, ResourceClass: profile.ResourceClass,
		MaxConcurrent: profile.MaxConcurrent, Enabled: true, Healthy: false,
		HealthReason: "validation failed", FakeOutcome: profile.FakeOutcome, ExpectedVersion: profile.Version,
	}); err != nil {
		t.Fatal(err)
	}
	overview, err = store.Overview(context.Background())
	if err != nil || overview.WorkersTotal != 1 || overview.WorkersOnline != 0 {
		t.Fatalf("unhealthy synthetic Overview = %#v, err %v", overview, err)
	}
	worker, err = store.Worker(context.Background(), profile.SyntheticWorkerID)
	if err != nil || worker.Online {
		t.Fatalf("unhealthy synthetic Worker = %#v, err %v", worker, err)
	}
}
