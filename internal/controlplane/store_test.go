package controlplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestStoreLeasesPipelineInOrderAndPersistsState(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	store := openTestStore(t, database)
	agents := []config.ResolvedAgent{
		testAgent("plan", "Plan request"),
		testAgent("build", "Build request"),
		testAgent("verify", "Verify request"),
	}
	agents[0].Model = "luna"
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "pipeline", "code", agents)
	if err != nil {
		t.Fatal(err)
	}

	incompatible, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"other"}, []string{"machinist"}))
	if err != nil || incompatible != nil {
		t.Fatalf("incompatible poll = %#v, %v", incompatible, err)
	}
	wrongModel := pollRequest("worker-b", []string{"codex"}, []string{"machinist"})
	wrongModel.Models = map[string][]string{"codex": {"terra"}}
	incompatible, err = store.Poll(t.Context(), wrongModel)
	if err != nil || incompatible != nil {
		t.Fatalf("wrong-model poll = %#v, %v", incompatible, err)
	}
	compatible := pollRequest("worker-a", []string{"codex"}, []string{"machinist"})
	compatible.Models = map[string][]string{"codex": {"luna"}}
	first, err := store.Poll(t.Context(), compatible)
	if err != nil || first == nil || first.Agent != "plan" || first.RenderedPrompt != "Plan request" || first.Model != "luna" {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	repeated, err := store.Poll(t.Context(), compatible)
	if err != nil || repeated == nil || repeated.ID != first.ID || repeated.LeaseToken != first.LeaseToken {
		t.Fatalf("repeated lease = %#v, %v", repeated, err)
	}
	if err := store.Complete(t.Context(), first.ID, protocol.Completion{InstanceID: "worker-b", LeaseToken: first.LeaseToken, State: "succeeded"}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("cross-worker completion error = %v", err)
	}
	completion := protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0, Events: "event\n"}
	if err := store.Complete(t.Context(), first.ID, completion); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), first.ID, completion); err != nil {
		t.Fatalf("idempotent completion: %v", err)
	}
	output, err := store.RunOutput(t.Context(), first.ID)
	if err != nil || output.Events != "event\n" {
		t.Fatalf("run output = %#v, %v", output, err)
	}
	second, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || second == nil || second.Agent != "build" {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: second.LeaseToken, State: "failed", ExitCode: 9, Error: "build failed"}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Jobs[0].Runs[0].Model != "luna" {
		t.Fatalf("stored model = %q", snapshot.Jobs[0].Runs[0].Model)
	}
	assertFailedPipeline(t, snapshot, jobID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, database)
	snapshot, err = reopened.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	assertFailedPipeline(t, snapshot, jobID)
}

func TestStorePersistsRunMetricsWithAndWithoutTokenUsage(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	store := openTestStore(t, database)
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "pipeline", "metrics", []config.ResolvedAgent{
		testAgent("reported", "Report usage"),
		testAgent("missing", "Do not report usage"),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), first.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0,
		Result: json.RawMessage(`{"duration_millis":1250,"token_usage":987}`),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), second.ID, protocol.Completion{
		InstanceID: "worker-a", LeaseToken: second.LeaseToken, State: "succeeded", ExitCode: 0,
		Result: json.RawMessage(`{"duration_millis":2500}`),
	}); err != nil {
		t.Fatal(err)
	}

	assertStoredMetrics(t, store, jobID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertStoredMetrics(t, reopened, jobID)
}

func TestStoreRejectsContradictoryOutcomes(t *testing.T) {
	for _, test := range []struct {
		state    string
		exitCode int
	}{
		{state: "succeeded", exitCode: 1},
		{state: "failed", exitCode: 0},
		{state: "timed_out", exitCode: 1},
		{state: "cancelled", exitCode: 1},
	} {
		t.Run(test.state, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
			if _, err := store.CreateJob(t.Context(), "request", "machinist", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
				t.Fatal(err)
			}
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
			if err != nil {
				t.Fatal(err)
			}
			err = store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: test.state, ExitCode: test.exitCode})
			if err == nil {
				t.Fatal("expected contradictory outcome rejection")
			}
			snapshot, snapshotErr := store.Snapshot(t.Context())
			if snapshotErr != nil || snapshot.Jobs[0].Runs[0].State != "running" {
				t.Fatalf("snapshot = %#v, %v", snapshot, snapshotErr)
			}
		})
	}
}

func TestConcurrentPollsLeaseRunOnce(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			run, err := store.Poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}))
			results <- run
			errorsChannel <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	leased := 0
	for run := range results {
		if run != nil {
			leased++
		}
	}
	if leased != 1 {
		t.Fatalf("leased runs = %d, want 1", leased)
	}
}

func TestStoreRenewsAndRedispatchesExpiredLease(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "pipeline", "code", []config.ResolvedAgent{
		testAgent("plan", "Plan request"),
		testAgent("build", "Build request"),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || first == nil {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))
	clock.Advance(9 * time.Second)
	repeated, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || repeated == nil || repeated.LeaseToken != first.LeaseToken {
		t.Fatalf("repeated lease = %#v, %v", repeated, err)
	}
	assertLeaseExpiry(t, store, first.ID, time.Date(2026, time.August, 25, 12, 0, 30, 0, time.UTC))

	heartbeat := protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: first.LeaseToken}
	if err := store.Heartbeat(t.Context(), first.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))
	if err := store.Heartbeat(t.Context(), first.ID, protocol.Heartbeat{InstanceID: "worker-b", LeaseToken: first.LeaseToken}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("cross-worker heartbeat error = %v", err)
	}
	if err := store.Heartbeat(t.Context(), first.ID, protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: "stale"}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale heartbeat error = %v", err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))

	clock.Advance(leaseDuration)
	if err := store.Heartbeat(t.Context(), first.ID, heartbeat); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expired heartbeat error = %v", err)
	}
	staleCompletion := protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0, Result: []byte(`{"stale":true}`)}
	if err := store.Complete(t.Context(), first.ID, staleCompletion); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("expired completion error = %v", err)
	}

	incompatible, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"other"}, []string{"machinist"}))
	if err != nil || incompatible != nil {
		t.Fatalf("incompatible poll = %#v, %v", incompatible, err)
	}
	assertReclaimedRun(t, store, first.ID, jobID)

	redispatched, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}))
	if err != nil || redispatched == nil || redispatched.ID != first.ID || redispatched.LeaseToken == first.LeaseToken {
		t.Fatalf("redispatched lease = %#v, %v", redispatched, err)
	}
	if err := store.Complete(t.Context(), first.ID, staleCompletion); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale completion after redispatch error = %v", err)
	}
	output, err := store.RunOutput(t.Context(), first.ID)
	if err != nil || output.Result != "" || output.Events != "" {
		t.Fatalf("stale output = %#v, %v", output, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || snapshot.Jobs[0].Runs[1].State != "pending" {
		t.Fatalf("pipeline advanced after stale completion: %#v, %v", snapshot, err)
	}
}

func TestConcurrentPollsReclaimExpiredRunOnce(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(leaseDuration)

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-b", "worker-c"} {
		group.Add(1)
		go func(instance string) {
			defer group.Done()
			<-start
			run, pollErr := store.Poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}))
			results <- run
			errorsChannel <- pollErr
		}(instance)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)
	for pollErr := range errorsChannel {
		if pollErr != nil {
			t.Fatal(pollErr)
		}
	}
	leases := 0
	for run := range results {
		if run != nil {
			leases++
			if run.ID != initial.ID || run.LeaseToken == initial.LeaseToken {
				t.Fatalf("reclaimed lease = %#v", run)
			}
		}
	}
	if leases != 1 {
		t.Fatalf("reclaimed leases = %d, want 1", leases)
	}
}

func TestOpenStoreMigratesExistingDatabaseAndRecoversRunningLease(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	createPreviousDatabase(t, database)
	store := openTestStore(t, database)
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now

	columns := map[string]int{}
	rows, err := store.db.QueryContext(t.Context(), `PRAGMA table_info(runs)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name]++
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"lease_expires_at", "model", "duration_millis", "token_usage"} {
		if columns[name] != 1 {
			t.Fatalf("%s columns = %d", name, columns[name])
		}
	}
	output, err := store.RunOutput(t.Context(), "run-completed")
	if err != nil || output.Result != `{"answer":42,"duration_millis":60000,"token_usage":1234}` || output.Events != "completed event\n" {
		t.Fatalf("preserved output = %#v, %v", output, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 2 || len(snapshot.Workers) != 1 {
		t.Fatalf("preserved snapshot = %#v, %v", snapshot, err)
	}
	migrated := findRun(t, snapshot, "run-completed")
	if migrated.DurationMillis == nil || *migrated.DurationMillis != 60000 || migrated.TokenUsage == nil || *migrated.TokenUsage != 1234 {
		t.Fatalf("migrated metrics = %#v", migrated)
	}
	if migrated.Model != "" {
		t.Fatalf("migrated model = %q", migrated.Model)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-new", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil || run.ID != "run-running" || run.LeaseToken == "old-token" {
		t.Fatalf("recovered pre-migration lease = %#v, %v", run, err)
	}
	assertLeaseExpiry(t, store, run.ID, clock.Now().Add(leaseDuration))
}

func TestStorePersistsCurrentWorkerRepositories(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"other", "machinist", "machinist"})); err != nil || run != nil {
		t.Fatalf("first poll = %#v, %v", run, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || len(snapshot.Workers[0].Repositories) != 2 || snapshot.Workers[0].Repositories[0] != "machinist" || snapshot.Workers[0].Repositories[1] != "other" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
	if run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"})); err != nil || run != nil {
		t.Fatalf("second poll = %#v, %v", run, err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers[0].Repositories) != 1 || snapshot.Workers[0].Repositories[0] != "machinist" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
}

func TestAvailableRepositoriesExcludesStaleWorkerInstances(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"removed"})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id='worker-old'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Poll(t.Context(), pollRequest("worker-new", []string{"codex"}, []string{"machinist"})); err != nil {
		t.Fatal(err)
	}
	repositories, err := store.AvailableRepositories(t.Context(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != "machinist" {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestAvailableRepositoriesIncludesWorkerWithRunningExecution(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "agent", "plan", []config.ResolvedAgent{testAgent("plan", "Plan request")}); err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-busy", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=? WHERE instance_id='worker-busy'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	repositories, err := store.AvailableRepositories(t.Context(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != "machinist" {
		t.Fatalf("repositories = %#v", repositories)
	}
}

func TestStoreSchedulesOneNonOverlappingShepherdRunPerRepository(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC))
	store.now = clock.Now
	schedule := config.ResolvedShepherdSchedule{
		Name: "api", Repository: "api", Every: 15 * time.Minute, MaxActions: 3,
		Agent: testAgent("shepherd", "Inspect every pull request; at most 3 actions."),
	}

	jobID, created, err := store.CreateScheduledJob(t.Context(), schedule)
	if err != nil || !created || jobID == "" {
		t.Fatalf("first schedule = %q, %t, %v", jobID, created, err)
	}
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || created {
		t.Fatalf("duplicate schedule created = %t, %v", created, err)
	}
	if _, err := store.CreateJob(t.Context(), "manual", "api", "agent", "shepherd", []config.ResolvedAgent{schedule.Agent}); err == nil {
		t.Fatal("manual Shepherd overlapped the scheduled run")
	}
	pipeline := []config.ResolvedAgent{testAgent("plan", "Plan"), schedule.Agent}
	if _, err := store.CreateJob(t.Context(), "pipeline", "api", "pipeline", "merge", pipeline); err == nil {
		t.Fatal("pipeline containing Shepherd overlapped the scheduled run")
	}
	other := schedule
	other.Name = "api-second"
	if _, created, err := store.CreateScheduledJob(t.Context(), other); err != nil || created {
		t.Fatalf("overlapping repository schedule created = %t, %v", created, err)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"api"}))
	if err != nil || run == nil || run.Agent != "shepherd" || run.RenderedPrompt != schedule.Agent.Prompt {
		t.Fatalf("scheduled lease = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || created {
		t.Fatalf("schedule repeated before due = %t, %v", created, err)
	}

	clock.Advance(15 * time.Minute)
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("due schedule created = %t, %v", created, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 2 || snapshot.Jobs[0].ScheduleName != "api" || snapshot.Jobs[1].ScheduleName != "api" {
		t.Fatalf("scheduled jobs = %#v", snapshot.Jobs)
	}
}

func TestStoreScheduleDoesNotOverlapActivePipelineContainingShepherd(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC))
	store.now = clock.Now
	shepherd := testAgent("shepherd", "Inspect every pull request")
	pipeline := []config.ResolvedAgent{testAgent("plan", "Plan"), shepherd}
	if _, err := store.CreateJob(t.Context(), "pipeline", "api", "pipeline", "merge", pipeline); err != nil {
		t.Fatal(err)
	}
	schedule := config.ResolvedShepherdSchedule{
		Name: "api", Repository: "api", Every: 15 * time.Minute, MaxActions: 3, Agent: shepherd,
	}
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || created {
		t.Fatalf("schedule overlapped pipeline = %t, %v", created, err)
	}
	if _, err := store.CreateJob(t.Context(), "ordinary", "api", "pipeline", "checks", []config.ResolvedAgent{testAgent("review", "Review")}); err != nil {
		t.Fatalf("ordinary pipeline was blocked: %v", err)
	}
}

func TestStorePersistsShepherdScheduleAcrossRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	clock := newTestClock(time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC))
	schedule := config.ResolvedShepherdSchedule{
		Name: "api", Repository: "api", Every: time.Hour, MaxActions: 1,
		Agent: testAgent("shepherd", "Inspect every pull request; at most 1 action."),
	}
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("initial schedule created = %t, %v", created, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.now = clock.Now
	if _, created, err := reopened.CreateScheduledJob(t.Context(), schedule); err != nil || created {
		t.Fatalf("restart repeated schedule = %t, %v", created, err)
	}
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testAgent(name, prompt string) config.ResolvedAgent {
	return config.ResolvedAgent{Name: name, Executor: "codex", Prompt: prompt, Timeout: time.Minute, Hash: name + "-hash"}
}

func pollRequest(instance string, executors, repositories []string) protocol.PollRequest {
	return protocol.PollRequest{InstanceID: instance, Name: "test", Executors: executors, Repositories: repositories}
}

func assertFailedPipeline(t *testing.T, snapshot Snapshot, jobID string) {
	t.Helper()
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "failed" {
		t.Fatalf("jobs = %#v", snapshot.Jobs)
	}
	runs := snapshot.Jobs[0].Runs
	if len(runs) != 3 || runs[0].State != "succeeded" || runs[1].State != "failed" || runs[2].State != "skipped" {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[1].Error != "build failed" {
		t.Fatalf("run payloads = %#v", runs)
	}
	if runs[0].DurationMillis == nil || runs[1].DurationMillis == nil || runs[0].TokenUsage != nil || runs[1].TokenUsage != nil {
		t.Fatalf("completed run metrics = %#v", runs)
	}
}

func assertStoredMetrics(t *testing.T, store *Store, jobID string) {
	t.Helper()
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || len(snapshot.Jobs[0].Runs) != 2 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	reported, missing := snapshot.Jobs[0].Runs[0], snapshot.Jobs[0].Runs[1]
	if reported.DurationMillis == nil || *reported.DurationMillis != 1250 || reported.TokenUsage == nil || *reported.TokenUsage != 987 {
		t.Fatalf("reported metrics = %#v", reported)
	}
	if missing.DurationMillis == nil || *missing.DurationMillis != 2500 || missing.TokenUsage != nil {
		t.Fatalf("missing token metrics = %#v", missing)
	}
}

func findRun(t *testing.T, snapshot Snapshot, runID string) Run {
	t.Helper()
	for _, job := range snapshot.Jobs {
		for _, run := range job.Runs {
			if run.ID == runID {
				return run
			}
		}
	}
	t.Fatalf("run %q not found", runID)
	return Run{}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(now time.Time) *testClock {
	return &testClock{now: now}
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(duration)
}

func assertLeaseExpiry(t *testing.T, store *Store, runID string, want time.Time) {
	t.Helper()
	var got sql.NullInt64
	if err := store.db.QueryRowContext(t.Context(), `SELECT lease_expires_at FROM runs WHERE id=?`, runID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != want.UnixNano() {
		t.Fatalf("lease expiry = %v, want %d", got, want.UnixNano())
	}
}

func assertReclaimedRun(t *testing.T, store *Store, runID, jobID string) {
	t.Helper()
	var state string
	var worker, token, expiry, started any
	if err := store.db.QueryRowContext(t.Context(), `SELECT state,worker_instance,lease_token,lease_expires_at,started_at FROM runs WHERE id=?`, runID).Scan(&state, &worker, &token, &expiry, &started); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || worker != nil || token != nil || expiry != nil || started != nil {
		t.Fatalf("reclaimed run = state %q worker %v token %v expiry %v started %v", state, worker, token, expiry, started)
	}
	var jobState string
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "running" {
		t.Fatalf("job state = %q, want running", jobState)
	}
}

func createPreviousDatabase(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const schema = `
CREATE TABLE jobs (
  id TEXT PRIMARY KEY, prompt TEXT NOT NULL, repository TEXT NOT NULL,
  selection_kind TEXT NOT NULL, selection_name TEXT NOT NULL, state TEXT NOT NULL,
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE runs (
  id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES jobs(id), step INTEGER NOT NULL,
  agent TEXT NOT NULL, agent_hash TEXT NOT NULL, executor TEXT NOT NULL,
  repository TEXT NOT NULL, rendered_prompt TEXT NOT NULL, timeout_ms INTEGER NOT NULL,
  state TEXT NOT NULL, worker_instance TEXT, lease_token TEXT, exit_code INTEGER,
  error TEXT, result TEXT, events TEXT, started_at TEXT, completed_at TEXT,
  UNIQUE(job_id, step)
);
CREATE TABLE workers (
  instance_id TEXT PRIMARY KEY, name TEXT NOT NULL, last_seen_at TEXT NOT NULL
);
INSERT INTO jobs VALUES ('job-completed','done','machinist','agent','plan','succeeded','2026-08-24T12:00:00Z','2026-08-24T12:01:00Z');
INSERT INTO jobs VALUES ('job-running','active','machinist','agent','plan','running','2026-08-24T13:00:00Z','2026-08-24T13:01:00Z');
INSERT INTO workers VALUES ('worker-old','old worker','2026-08-24T13:01:00Z');
INSERT INTO runs VALUES ('run-completed','job-completed',0,'plan','plan-hash','codex','machinist','Done',60000,'succeeded','worker-old','completed-token',0,'','{"answer":42,"duration_millis":60000,"token_usage":1234}','completed event
','2026-08-24T12:00:00Z','2026-08-24T12:01:00Z');
INSERT INTO runs VALUES ('run-running','job-running',0,'plan','plan-hash','codex','machinist','Active',60000,'running','worker-old','old-token',NULL,'',NULL,NULL,'2026-08-24T13:01:00Z',NULL);
`
	if _, err := db.ExecContext(t.Context(), schema); err != nil {
		t.Fatal(err)
	}
}
