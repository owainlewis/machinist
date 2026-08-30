package controlplane

import (
	"context"
	"database/sql"

	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

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
			if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
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

func TestOpenStoreReplacesLegacySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE jobs(id TEXT PRIMARY KEY, selection_kind TEXT); INSERT INTO jobs VALUES('legacy','pipeline')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count, version int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if count != 0 || version != 1 {
		t.Fatalf("migrated database count=%d version=%d", count, version)
	}
}

func TestOpenStoreRejectsNewerSchemaWithoutDeletingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machinist.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE future_data(value TEXT); INSERT INTO future_data VALUES('preserved'); PRAGMA user_version=2;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(path); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		if store != nil {
			store.Close()
		}
		t.Fatalf("open newer schema error = %v", err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`SELECT value FROM future_data`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("future data = %q, %v", value, err)
	}
}

func TestConcurrentPollsLeaseRunOnce(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
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

func TestStoreConcurrentJobLimitLeavesAdditionalJobsQueued(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	firstJob, err := store.CreateJob(t.Context(), "first", "machinist", "plan", testAgent("plan", "First request"))
	if err != nil {
		t.Fatal(err)
	}
	secondJob, err := store.CreateJob(t.Context(), "second", "machinist", "plan", testAgent("plan", "Second request"))
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}), 1)
	if err != nil || first == nil || first.JobID != firstJob {
		t.Fatalf("first lease = %#v, %v", first, err)
	}
	blocked, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1)
	if err != nil || blocked != nil {
		t.Fatalf("poll at capacity = %#v, %v", blocked, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]string, len(snapshot.Jobs))
	for _, job := range snapshot.Jobs {
		states[job.ID] = job.State
	}
	if states[firstJob] != "running" || states[secondJob] != "queued" {
		t.Fatalf("jobs at capacity = %#v", snapshot.Jobs)
	}

	if err := store.Complete(t.Context(), first.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: first.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	second, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1)
	if err != nil || second == nil || second.JobID != secondJob {
		t.Fatalf("second lease = %#v, %v", second, err)
	}
}

func TestStoreConcurrentJobLimitRedispatchesExpiredActiveJob(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	activeJob, err := store.CreateJob(t.Context(), "active", "machinist", "plan", testAgent("plan", "Active request"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), "queued", "machinist", "review", testAgent("review", "Queued request")); err != nil {
		t.Fatal(err)
	}
	initial, err := store.poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}), 1)
	if err != nil || initial == nil || initial.JobID != activeJob {
		t.Fatalf("initial lease = %#v, %v", initial, err)
	}

	clock.Advance(leaseDuration)
	redispatched, err := store.poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}), 1)
	if err != nil || redispatched == nil || redispatched.ID != initial.ID || redispatched.LeaseToken == initial.LeaseToken {
		t.Fatalf("redispatched lease = %#v, %v", redispatched, err)
	}
}

func TestConcurrentPollsRespectGlobalJobLimit(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	for _, prompt := range []string{"first", "second"} {
		if _, err := store.CreateJob(t.Context(), prompt, "machinist", "plan", testAgent("plan", prompt)); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan *protocol.RunSpec, 2)
	errorsChannel := make(chan error, 2)
	var group sync.WaitGroup
	for _, instance := range []string{"worker-a", "worker-b"} {
		group.Add(1)
		go func(instance string) {
			defer group.Done()
			<-start
			run, err := store.poll(context.Background(), pollRequest(instance, []string{"codex"}, []string{"machinist"}), 1)
			results <- run
			errorsChannel <- err
		}(instance)
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
		t.Fatalf("leased jobs = %d, want 1", leased)
	}
}

func TestStoreRenewsAndRedispatchesExpiredLease(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request"))
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

	clock.Advance(5 * time.Second)
	heartbeat := protocol.Heartbeat{InstanceID: "worker-a", LeaseToken: first.LeaseToken}
	if err := store.Heartbeat(t.Context(), first.ID, heartbeat); err != nil {
		t.Fatal(err)
	}
	assertLeaseExpiry(t, store, first.ID, clock.Now().Add(leaseDuration))
	var lastSeen string
	if err := store.db.QueryRowContext(t.Context(), `SELECT last_seen_at FROM workers WHERE instance_id='worker-a'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen != clock.Now().Format(time.RFC3339Nano) {
		t.Fatalf("worker last seen = %q, want %q", lastSeen, clock.Now().Format(time.RFC3339Nano))
	}
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
	if err != nil || snapshot.Jobs[0].State != "running" || len(snapshot.Jobs[0].Runs) != 1 {
		t.Fatalf("stale completion changed job: %#v, %v", snapshot, err)
	}
}

func TestConcurrentPollsReclaimExpiredRunOnce(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 25, 12, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
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
	repositories, err := store.KnownRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repositories, []string{"machinist", "other"}) {
		t.Fatalf("known repositories = %#v", repositories)
	}
}

func TestStorePrunesSupersededWorkerAndPreservesRunWorkerName(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-old", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist", "retired"}})
	if err != nil || run == nil {
		t.Fatalf("old worker poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-old", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if _, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-new", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist"}}); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneSupersededWorkers(t.Context(), clock.Now().Add(-workerAvailabilityWindow))
	if err != nil || pruned != 1 {
		t.Fatalf("pruned workers = %d, %v", pruned, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Workers) != 1 || snapshot.Workers[0].InstanceID != "worker-new" {
		t.Fatalf("workers = %#v", snapshot.Workers)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].Runs[0].WorkerName != "builder" {
		t.Fatalf("jobs = %#v", snapshot.Jobs)
	}
	repositories, err := store.KnownRepositories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repositories, []string{"machinist", "retired"}) {
		t.Fatalf("known repositories = %#v", repositories)
	}
}

func TestStoreRetainsLatestDisconnectedWorkerRegistration(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if _, err := store.Poll(t.Context(), protocol.PollRequest{InstanceID: "worker-only", Name: "builder", Executors: []string{"codex"}, Repositories: []string{"machinist"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `UPDATE workers SET last_seen_at=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneSupersededWorkers(t.Context(), time.Now().Add(-time.Minute))
	if err != nil || pruned != 0 {
		t.Fatalf("pruned workers = %d, %v", pruned, err)
	}
}

func TestStoreReclaimsExpiredLeaseWithoutWorkerPoll(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	clock.Advance(leaseDuration + time.Second)
	reclaimed, err := store.ReclaimExpiredLeases(t.Context())
	if err != nil || reclaimed != 1 {
		t.Fatalf("reclaimed leases = %d, %v", reclaimed, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].State != "running" || snapshot.Jobs[0].Runs[0].State != "queued" || snapshot.Jobs[0].Runs[0].WorkerName != "" || !snapshot.Jobs[0].Runs[0].StartedAt.IsZero() {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestStoreDeletesTerminalJobAndRejectsActiveJob(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	jobID, err := store.CreateJob(t.Context(), "active", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); !errors.Is(err, ErrJobActive) {
		t.Fatalf("active delete error = %v", err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0, Result: []byte(`{"answer":42}`), Events: "event\n"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RunOutput(t.Context(), run.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted run output error = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 0 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
}

func TestStoreDeletesDisconnectedWorkerAndPreservesHistory(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-old", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(workerAvailabilityWindow + time.Second)
	if err := store.DeleteWorker(t.Context(), "worker-old", clock.Now().Add(-workerAvailabilityWindow)); err != nil {
		t.Fatal(err)
	}
	var workerRepositories int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM worker_repositories WHERE worker_instance='worker-old'`).Scan(&workerRepositories); err != nil || workerRepositories != 0 {
		t.Fatalf("worker repositories = %d, %v", workerRepositories, err)
	}
	var knownRepositories int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM known_repositories WHERE repository='machinist'`).Scan(&knownRepositories); err != nil || knownRepositories != 1 {
		t.Fatalf("known repositories = %d, %v", knownRepositories, err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].ID != jobID || snapshot.Jobs[0].Runs[0].WorkerName != "test" {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	if _, err := store.Poll(t.Context(), pollRequest("worker-old", []string{"codex"}, []string{"machinist"})); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil || len(snapshot.Workers) != 1 || snapshot.Workers[0].InstanceID != "worker-old" || len(snapshot.Workers[0].Repositories) != 1 {
		t.Fatalf("re-registered worker = %#v, %v", snapshot.Workers, err)
	}
}

func TestStoreRejectsConnectedOrActiveWorkerDeletion(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	clock := newTestClock(time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC))
	store.now = clock.Now
	if _, err := store.Poll(t.Context(), pollRequest("worker-connected", []string{"codex"}, []string{"machinist"})); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteWorker(t.Context(), "worker-connected", clock.Now().Add(-workerAvailabilityWindow)); !errors.Is(err, ErrWorkerConnected) {
		t.Fatalf("connected deletion error = %v", err)
	}
	jobID, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-active", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	clock.Advance(workerAvailabilityWindow + time.Second)
	if err := store.DeleteWorker(t.Context(), "worker-active", clock.Now().Add(-workerAvailabilityWindow)); !errors.Is(err, ErrWorkerActive) {
		t.Fatalf("active deletion error = %v", err)
	}
	var jobs int
	if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE id=?`, jobID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs = %d, %v", jobs, err)
	}
}

func TestStoreDeletingTriggeredJobPreservesPendingReconciliation(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "github/intake", Family: "github", ConfigSignature: "v1"}}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{
		Identity: "github/intake", Family: "github", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "github/intake"),
		OccurrenceKey: "github.com/event/1", Subject: "https://github.com/owainlewis/machinist/issues/396", ScheduledAt: time.Now().UTC(),
		Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
		GitHubRepository: "owainlewis/machinist", GitHubIssueNumber: 396, RequestActor: "owner", RequestLabel: "machinist:requested",
	}
	jobID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("triggered job = %q, %v, %v", jobID, created, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteJob(t.Context(), jobID); err != nil {
		t.Fatal(err)
	}
	reconciliations, err := store.GitHubTriggerReconciliations(t.Context(), admission.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciliations) != 1 || reconciliations[0].OccurrenceKey != admission.OccurrenceKey || reconciliations[0].State != "admitted" || reconciliations[0].JobID != "" {
		t.Fatalf("reconciliations = %#v", reconciliations)
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
	if _, err := store.CreateJob(t.Context(), "request", "machinist", "plan", testAgent("plan", "Plan request")); err != nil {
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
		Command: testAgent("shepherd", "Inspect every pull request; at most 3 actions."),
	}

	jobID, created, err := store.CreateScheduledJob(t.Context(), schedule)
	if err != nil || !created || jobID == "" {
		t.Fatalf("first schedule = %q, %t, %v", jobID, created, err)
	}
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || created {
		t.Fatalf("duplicate schedule created = %t, %v", created, err)
	}
	if _, err := store.CreateJob(t.Context(), "manual", "api", "shepherd", schedule.Command); err == nil {
		t.Fatal("manual Shepherd overlapped the scheduled run")
	}
	other := schedule
	other.Name = "api-second"
	if _, created, err := store.CreateScheduledJob(t.Context(), other); err != nil || created {
		t.Fatalf("overlapping repository schedule created = %t, %v", created, err)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"api"}))
	if err != nil || run == nil || run.Command != "shepherd" || run.RenderedPrompt != schedule.Command.Prompt {
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

func TestStoreReschedulesWhenScheduleConfigurationChanges(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	clock := newTestClock(time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC))
	store.now = clock.Now
	schedule := config.ResolvedShepherdSchedule{
		Name: "queue", Repository: "api", Every: time.Hour, MaxActions: 1,
		Command: testAgent("shepherd", "Inspect every pull request"),
	}
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("initial schedule created = %t, %v", created, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"api"}))
	if err != nil || run == nil {
		t.Fatalf("lease initial schedule = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now

	schedule.Repository = "web"
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("repository change rescheduled = %t, %v", created, err)
	}
	run, err = store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"web"}))
	if err != nil || run == nil {
		t.Fatalf("lease changed repository schedule = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-b", LeaseToken: run.LeaseToken, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now

	schedule.Every = 10 * time.Minute
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("interval change rescheduled = %t, %v", created, err)
	}
	run, err = store.Poll(t.Context(), pollRequest("worker-c", []string{"codex"}, []string{"web"}))
	if err != nil || run == nil {
		t.Fatalf("lease interval-changed schedule = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-c", LeaseToken: run.LeaseToken, State: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = clock.Now

	schedule.MaxActions = 4
	schedule.Prompt = "Run Shepherd with max_actions=4"
	schedule.Command = testAgent("shepherd", "Updated policy; at most 4 actions")
	schedule.Command.Timeout = 2 * time.Minute
	if _, created, err := store.CreateScheduledJob(t.Context(), schedule); err != nil || !created {
		t.Fatalf("execution settings change rescheduled = %t, %v", created, err)
	}
	run, err = store.Poll(t.Context(), pollRequest("worker-d", []string{"codex"}, []string{"web"}))
	if err != nil || run == nil || run.RenderedPrompt != schedule.Command.Prompt || run.TimeoutMillis != schedule.Command.Timeout.Milliseconds() {
		t.Fatalf("lease execution-settings schedule = %#v, %v", run, err)
	}
}

func TestStorePersistsShepherdScheduleAcrossRestart(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	clock := newTestClock(time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC))
	schedule := config.ResolvedShepherdSchedule{
		Name: "api", Repository: "api", Every: time.Hour, MaxActions: 1,
		Command: testAgent("shepherd", "Inspect every pull request; at most 1 action."),
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

func TestShepherdScheduleSignatureCoversExecutionSettings(t *testing.T) {
	base := config.ResolvedShepherdSchedule{
		Name: "api", Repository: "api", Every: time.Hour, MaxActions: 1,
		Prompt:  "Run with max_actions=1",
		Command: testAgent("shepherd", "Inspect every pull request; at most 1 action."),
	}
	want, err := shepherdScheduleSignature(base)
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(*config.ResolvedShepherdSchedule){
		"max actions":     func(schedule *config.ResolvedShepherdSchedule) { schedule.MaxActions = 2 },
		"schedule prompt": func(schedule *config.ResolvedShepherdSchedule) { schedule.Prompt = "Run with max_actions=2" },
		"agent prompt":    func(schedule *config.ResolvedShepherdSchedule) { schedule.Command.Prompt = "Updated queue policy" },
		"executor":        func(schedule *config.ResolvedShepherdSchedule) { schedule.Command.Executor = "claude" },
		"model":           func(schedule *config.ResolvedShepherdSchedule) { schedule.Command.Model = "sol" },
		"timeout":         func(schedule *config.ResolvedShepherdSchedule) { schedule.Command.Timeout = 2 * time.Hour },
	}
	for name, change := range variants {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			got, err := shepherdScheduleSignature(changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == want {
				t.Fatal("execution setting did not change schedule signature")
			}
		})
	}
}

func TestStoreSyncsDurableTriggerStateAcrossRestartAndConfigurationChanges(t *testing.T) {
	database := filepath.Join(t.TempDir(), "machinist.db")
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, database)
	store.now = clock.Now
	firstDue := clock.Now().Add(time.Hour)
	definitions := []TriggerDefinition{
		{Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", NextDueAt: firstDue},
		{Identity: "github/intake", Family: "github", ConfigSignature: "v1"},
	}
	if err := store.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/intake", mustTriggerGeneration(t, store, "github/intake"), 3, errors.New(strings.Repeat("x", maxTriggerErrorLength+10))); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := store.SetTriggerNextDue(t.Context(), "interval/audit", mustTriggerGeneration(t, store, "interval/audit"), firstDue.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, database)
	reopened.now = clock.Now
	if err := reopened.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	statuses, err := reopened.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[0].Identity != "github/intake" || statuses[1].Identity != "interval/audit" {
		t.Fatalf("trigger statuses = %#v", statuses)
	}
	if statuses[0].Health != "failed" || statuses[0].CandidateCount != 3 || len([]rune(statuses[0].LatestError)) != maxTriggerErrorLength {
		t.Fatalf("GitHub status = %#v", statuses[0])
	}
	if statuses[1].NextDueAt == nil || !statuses[1].NextDueAt.Equal(firstDue.Add(time.Hour)) {
		t.Fatalf("unchanged next due = %#v", statuses[1].NextDueAt)
	}

	changedDue := clock.Now().Add(4 * time.Hour)
	if err := reopened.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "github/intake", Family: "github", ConfigSignature: "v2", NextDueAt: changedDue}}); err != nil {
		t.Fatal(err)
	}
	statuses, err = reopened.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Identity != "github/intake" || statuses[0].Health != "healthy" || statuses[0].CandidateCount != 0 || statuses[0].LastAttemptAt != nil || statuses[0].LatestError != "" {
		t.Fatalf("changed trigger status = %#v", statuses)
	}
	if statuses[0].NextDueAt == nil || !statuses[0].NextDueAt.Equal(changedDue) {
		t.Fatalf("changed next due = %#v", statuses[0].NextDueAt)
	}
}

func TestStoreAdmitsUniqueFixedOccurrencesAndCoalescesOverlap(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", NextDueAt: clock.Now().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{
		Identity: "interval/audit", Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "interval/audit"), ScheduledAt: clock.Now().Add(time.Hour), NextDueAt: clock.Now().Add(2 * time.Hour),
		Prompt: "Audit", Repository: "machinist", SelectionName: "audit", Command: testAgent("audit", "Audit"),
	}
	jobID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("first admission = %q, %v, %v", jobID, created, err)
	}
	duplicateID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || created || duplicateID != jobID {
		t.Fatalf("duplicate admission = %q, %v, %v", duplicateID, created, err)
	}
	later := admission
	later.ScheduledAt = clock.Now().Add(2 * time.Hour)
	later.NextDueAt = clock.Now().Add(3 * time.Hour)
	activeID, created, err := store.CreateTriggeredJob(t.Context(), later)
	if err != nil || created || activeID != jobID {
		t.Fatalf("coalesced admission = %q, %v, %v", activeID, created, err)
	}
	if err := store.AddTriggerCoalesced(t.Context(), admission.Identity, admission.ConfigGeneration, 2); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Triggers) != 1 || snapshot.Triggers[0].ActiveJobID != jobID || snapshot.Triggers[0].AdmissionCount != 1 || snapshot.Triggers[0].CoalescedCount != 3 || snapshot.Triggers[0].Health != "coalesced" {
		t.Fatalf("active trigger snapshot = %#v", snapshot.Triggers)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].TriggerID != admission.Identity || snapshot.Jobs[0].OccurrenceKey != admission.ScheduledAt.Format(time.RFC3339Nano) {
		t.Fatalf("triggered job snapshot = %#v", snapshot.Jobs)
	}

	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	clock.Advance(time.Second)
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].ActiveJobID != "" || statuses[0].LastSuccessAt == nil || !statuses[0].LastSuccessAt.Equal(clock.Now()) || statuses[0].Health != "healthy" {
		t.Fatalf("completed trigger status = %#v", statuses[0])
	}
	secondID, created, err := store.CreateTriggeredJob(t.Context(), later)
	if err != nil || !created || secondID == jobID {
		t.Fatalf("catch-up admission = %q, %v, %v", secondID, created, err)
	}
}

func TestStorePreservesLatestFailedJobHealthAcrossSuccessfulPolls(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	definitions := []TriggerDefinition{
		{Identity: "github/jobs", Family: "github", ConfigSignature: "jobs"},
		{Identity: "github/polls", Family: "github", ConfigSignature: "polls"},
	}
	if err := store.SyncTriggers(t.Context(), definitions); err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
		Identity: "github/jobs", Family: "github", ConfigSignature: "jobs", ConfigGeneration: mustTriggerGeneration(t, store, "github/jobs"),
		OccurrenceKey: "github.com:event:1", Subject: "https://github.com/o/r/issues/1", ScheduledAt: clock.Now(),
		Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
		GitHubRepository: "o/r", GitHubIssueNumber: 1, RequestActor: "owner", RequestLabel: "machinist:requested",
	})
	if err != nil || !created {
		t.Fatalf("admit github job = %v, %v", created, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll github job = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1, Error: "agent failed"}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	if err := store.RecordTriggerAttempt(t.Context(), "github/jobs", mustTriggerGeneration(t, store, "github/jobs"), 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/polls", mustTriggerGeneration(t, store, "github/polls"), 0, errors.New("temporary poll failure")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), "github/polls", mustTriggerGeneration(t, store, "github/polls"), 0, nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byIdentity := map[string]TriggerStatus{}
	for _, status := range statuses {
		byIdentity[status.Identity] = status
	}
	if status := byIdentity["github/jobs"]; status.Health != "failed" || status.LatestError != "agent failed" {
		t.Fatalf("failed job health was erased by successful poll: %#v", status)
	}
	if status := byIdentity["github/polls"]; status.Health != "healthy" || status.LatestError != "" {
		t.Fatalf("recovered poll error was retained: %#v", status)
	}
}

func TestStorePreservesTriggerHealthFromActualCompletionOrder(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	identity := "github/jobs"
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "github", ConfigSignature: "jobs"}}); err != nil {
		t.Fatal(err)
	}
	generation := mustTriggerGeneration(t, store, identity)
	for issue := 1; issue <= 2; issue++ {
		_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
			Identity: identity, Family: "github", ConfigSignature: "jobs", ConfigGeneration: generation,
			OccurrenceKey: fmt.Sprintf("github.com:event:%d", issue), Subject: fmt.Sprintf("https://github.com/o/r/issues/%d", issue), ScheduledAt: clock.Now(),
			Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", Command: testAgent("foreman", "Complete issue"),
			GitHubRepository: "o/r", GitHubIssueNumber: issue, RequestActor: "owner", RequestLabel: "machinist:requested",
		})
		if err != nil || !created {
			t.Fatalf("admit github job %d = %v, %v", issue, created, err)
		}
	}
	older, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || older == nil {
		t.Fatalf("poll older job = %#v, %v", older, err)
	}
	newer, err := store.Poll(t.Context(), pollRequest("worker-b", []string{"codex"}, []string{"machinist"}))
	if err != nil || newer == nil || newer.JobID == older.JobID {
		t.Fatalf("poll newer job = %#v, %v", newer, err)
	}
	if err := store.Complete(t.Context(), newer.ID, protocol.Completion{InstanceID: "worker-b", LeaseToken: newer.LeaseToken, State: "failed", ExitCode: 1, Error: "newer job failed"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(t.Context(), older.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: older.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTriggerAttempt(t.Context(), identity, generation, 0, nil); err != nil {
		t.Fatal(err)
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Health != "healthy" || statuses[0].LatestError != "" {
		t.Fatalf("completion-order health = %#v, want healthy", statuses)
	}
}

func TestStoreIgnoresCompletionFromPreviousTriggerConfiguration(t *testing.T) {
	for _, completion := range []protocol.Completion{
		{State: "succeeded", ExitCode: 0},
		{State: "failed", ExitCode: 1, Error: "old configuration failed"},
	} {
		t.Run(completion.State, func(t *testing.T) {
			clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
			store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
			store.now = clock.Now
			identity := "interval/audit"
			if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v1", NextDueAt: clock.Now()}}); err != nil {
				t.Fatal(err)
			}
			_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
				Identity: identity, Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, identity),
				ScheduledAt: clock.Now(), NextDueAt: clock.Now().Add(time.Hour),
				Prompt: "Audit", Repository: "machinist", SelectionName: "audit",
				Command: testAgent("audit", "Audit"),
			})
			if err != nil || !created {
				t.Fatalf("admit v1 trigger = %v, %v", created, err)
			}
			run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
			if err != nil || run == nil {
				t.Fatalf("poll v1 trigger = %#v, %v", run, err)
			}
			if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v2", NextDueAt: clock.Now().Add(2 * time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			completion.InstanceID = "worker-a"
			completion.LeaseToken = run.LeaseToken
			if err := store.Complete(t.Context(), run.ID, completion); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordTriggerAttempt(t.Context(), identity, mustTriggerGeneration(t, store, identity), 0, nil); err != nil {
				t.Fatal(err)
			}
			statuses, err := store.TriggerSnapshot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(statuses) != 1 || statuses[0].ConfigSignature != "v2" || statuses[0].Health != "healthy" || statuses[0].LastSuccessAt != nil || statuses[0].LatestError != "" {
				t.Fatalf("v2 status changed by v1 completion: %#v", statuses)
			}
		})
	}
}

func TestStoreRejectsSchedulerWritesFromPreviousTriggerConfiguration(t *testing.T) {
	clock := newTestClock(time.Date(2026, time.August, 27, 9, 0, 0, 0, time.UTC))
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	store.now = clock.Now
	identity := "interval/audit"
	v2Due := clock.Now().Add(2 * time.Hour)
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v2", NextDueAt: v2Due}}); err != nil {
		t.Fatal(err)
	}
	staleWrites := []func() error{
		func() error {
			return store.RecordTriggerAttempt(t.Context(), identity, "v1", 1, errors.New("stale failure"))
		},
		func() error { return store.SetTriggerNextDue(t.Context(), identity, "v1", clock.Now().Add(time.Hour)) },
		func() error { return store.SetTriggerPendingOccurrence(t.Context(), identity, "v1", clock.Now()) },
		func() error { return store.AddTriggerCoalesced(t.Context(), identity, "v1", 1) },
	}
	for index, write := range staleWrites {
		if err := write(); !errors.Is(err, ErrTriggerStale) {
			t.Fatalf("stale write %d error = %v, want ErrTriggerStale", index, err)
		}
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].ConfigSignature != "v2" || statuses[0].NextDueAt == nil || !statuses[0].NextDueAt.Equal(v2Due) || statuses[0].PendingOccurrenceAt != nil || statuses[0].LastAttemptAt != nil || statuses[0].Health != "healthy" || statuses[0].CandidateCount != 0 || statuses[0].CoalescedCount != 0 || statuses[0].LatestError != "" {
		t.Fatalf("v2 status changed by stale scheduler: %#v", statuses)
	}
}

func TestStoreUsesDistinctTriggerGenerationsAcrossABAAndRecreation(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	identity := "interval/audit"
	sync := func(signature string) string {
		t.Helper()
		if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: signature, NextDueAt: time.Now().UTC()}}); err != nil {
			t.Fatal(err)
		}
		return mustTriggerGeneration(t, store, identity)
	}
	a1 := sync("a")
	if got := sync("a"); got != a1 {
		t.Fatalf("unchanged configuration generation changed: %q then %q", a1, got)
	}
	b := sync("b")
	a2 := sync("a")
	if a1 == b || b == a2 || a1 == a2 {
		t.Fatalf("ABA generations are not unique: a1=%q b=%q a2=%q", a1, b, a2)
	}
	if err := store.SyncTriggers(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	a3 := sync("a")
	if a3 == a2 || a3 == a1 {
		t.Fatalf("recreated trigger reused a generation: a1=%q a2=%q a3=%q", a1, a2, a3)
	}
}

func TestStoreRetriesUncommittedAdmissionAndPreventsSubjectOverlap(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "machinist.db"))
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{
		{Identity: "github/intake", Family: "github", ConfigSignature: "v1"},
		{Identity: "github/security", Family: "github", ConfigSignature: "v1"},
	}); err != nil {
		t.Fatal(err)
	}
	admission := TriggerAdmission{Identity: "github/intake", Family: "github", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, "github/intake"), OccurrenceKey: "github.com/event/1", Subject: "https://github.com/owainlewis/machinist/issues/396", Prompt: "Complete issue", Repository: "machinist", SelectionName: "foreman", GitHubRepository: "owainlewis/machinist", GitHubIssueNumber: 396, RequestActor: "owner", RequestLabel: "machinist:requested", ScheduledAt: time.Now().UTC()}
	if _, _, err := store.CreateTriggeredJob(t.Context(), admission); err == nil {
		t.Fatal("expected incomplete admission to fail")
	}
	admission.Command = testAgent("foreman", "Complete issue")
	firstID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || !created {
		t.Fatalf("retried admission = %q, %v, %v", firstID, created, err)
	}
	exists, err := store.TriggerOccurrenceExists(t.Context(), admission.Identity, admission.OccurrenceKey)
	if err != nil || !exists {
		t.Fatalf("committed occurrence exists = %v, %v", exists, err)
	}
	reapplied := admission
	reapplied.Identity = "github/security"
	reapplied.ConfigSignature = "v1"
	reapplied.ConfigGeneration = mustTriggerGeneration(t, store, "github/security")
	reapplied.OccurrenceKey = "github.com/event/2"
	activeID, created, err := store.CreateTriggeredJob(t.Context(), reapplied)
	if err != nil || created || activeID != firstID {
		t.Fatalf("overlapping subject = %q, %v, %v", activeID, created, err)
	}
	exists, err = store.TriggerOccurrenceExists(t.Context(), reapplied.Identity, reapplied.OccurrenceKey)
	if err != nil || exists {
		t.Fatalf("blocked occurrence exists = %v, %v", exists, err)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"codex"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "failed", ExitCode: 1, Error: "work failed"}); err != nil {
		t.Fatal(err)
	}
	duplicateID, created, err := store.CreateTriggeredJob(t.Context(), admission)
	if err != nil || created || duplicateID != firstID {
		t.Fatalf("terminal duplicate occurrence = %q, %v, %v", duplicateID, created, err)
	}
	secondID, created, err := store.CreateTriggeredJob(t.Context(), reapplied)
	if err != nil || !created || secondID == firstID {
		t.Fatalf("reapplied occurrence = %q, %v, %v", secondID, created, err)
	}
}

func mustTriggerGeneration(t *testing.T, store *Store, identity string) string {
	t.Helper()
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Identity == identity {
			return status.ConfigGeneration
		}
	}
	t.Fatalf("trigger %q has no generation", identity)
	return ""
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

func testAgent(name, prompt string) config.ResolvedCommand {
	return config.ResolvedCommand{Name: name, Executor: "codex", Prompt: prompt, Timeout: time.Minute, Hash: name + "-hash"}
}

func pollRequest(instance string, executors, repositories []string) protocol.PollRequest {
	return protocol.PollRequest{InstanceID: instance, Name: "test", Executors: executors, Repositories: repositories}
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
	var state, workerName string
	var worker, token, expiry, started any
	if err := store.db.QueryRowContext(t.Context(), `SELECT state,worker_instance,worker_name,lease_token,lease_expires_at,started_at FROM runs WHERE id=?`, runID).Scan(&state, &worker, &workerName, &token, &expiry, &started); err != nil {
		t.Fatal(err)
	}
	if state != "queued" || worker != nil || workerName != "" || token != nil || expiry != nil || started != nil {
		t.Fatalf("reclaimed run = state %q worker %v worker name %q token %v expiry %v started %v", state, worker, workerName, token, expiry, started)
	}
	var jobState string
	if err := store.db.QueryRowContext(t.Context(), `SELECT state FROM jobs WHERE id=?`, jobID).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if jobState != "running" {
		t.Fatalf("job state = %q, want running", jobState)
	}
}
