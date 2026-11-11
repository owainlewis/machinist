package tickets

import (
	"strings"
	"testing"
	"time"
)

func fleet(t *testing.T, store *Store) ([]Runner, Queue) {
	t.Helper()
	runners, queue, err := store.Fleet(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return runners, queue
}

func TestRecordRunnerKeepsTheLatestDescription(t *testing.T) {
	store := openStore(t)
	if err := store.RecordRunner(t.Context(), "mac-mini", "local", []string{"claude"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "cluster", "kubernetes", []string{"claude", "codex"}, 8); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "mac-mini", "local", nil, 2); err != nil {
		t.Fatal(err)
	}
	runners, queue := fleet(t, store)
	if len(runners) != 2 || runners[0].Name != "cluster" || runners[1].Name != "mac-mini" {
		t.Fatalf("runners = %+v", runners)
	}
	cluster, mini := runners[0], runners[1]
	if cluster.Kind != "kubernetes" || cluster.Slots != 8 || strings.Join(cluster.Harnesses, ",") != "claude,codex" {
		t.Fatalf("cluster = %+v", cluster)
	}
	if mini.Slots != 2 || mini.Harnesses == nil || len(mini.Harnesses) != 0 || mini.Jobs == nil || len(mini.Jobs) != 0 {
		t.Fatalf("mac-mini = %+v", mini)
	}
	if queue.Waiting != 0 || queue.Oldest != nil {
		t.Fatalf("queue = %+v", queue)
	}
}

func TestRecordRunnerTreatsNoSlotsAsOne(t *testing.T) {
	store := openStore(t)
	if err := store.RecordRunner(t.Context(), "old-runner", "", []string{"claude"}, 0); err != nil {
		t.Fatal(err)
	}
	if runners, _ := fleet(t, store); runners[0].Slots != 1 {
		t.Fatalf("slots = %d", runners[0].Slots)
	}
}

func TestTouchRunnerMovesLastContact(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, 1); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := store.TouchRunner(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchRunner(t.Context(), "never-polled"); err != nil {
		t.Fatal(err)
	}
	runners, _ := fleet(t, store)
	if len(runners) != 1 || !runners[0].LastContact.Equal(now) {
		t.Fatalf("runners = %+v", runners)
	}
}

func TestFleetShowsRunningJobsAndTheQueue(t *testing.T) {
	store := openStore(t)
	queued := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	now := queued
	store.SetClock(func() time.Time { return now })
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, 2); err != nil {
		t.Fatal(err)
	}
	running := createTicket(t, store, "api:1")
	claim := claimRun(t, store, running)
	waiting := createTicket(t, store, "api:2")
	if _, err := store.QueueRun(t.Context(), waiting.ID, "codex"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	later := createTicket(t, store, "api:3")
	if _, err := store.QueueRun(t.Context(), later.ID, "codex"); err != nil {
		t.Fatal(err)
	}

	runners, queue := fleet(t, store)
	if len(runners[0].Jobs) != 1 {
		t.Fatalf("jobs = %+v", runners[0].Jobs)
	}
	job := runners[0].Jobs[0]
	if job.RunID != claim.Run.ID || job.TicketID != running.ID || job.Title != running.Title || job.Kind != RunKindAgent || job.StartedAt.IsZero() {
		t.Fatalf("job = %+v", job)
	}
	if queue.Waiting != 2 || queue.Oldest == nil || !queue.Oldest.Equal(queued) {
		t.Fatalf("queue = %+v", queue)
	}
}

func TestRevokingATokenRemovesItsRunner(t *testing.T) {
	store := openStore(t)
	if _, err := store.CreateToken(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if runners, _ := fleet(t, store); len(runners) != 0 {
		t.Fatalf("runners = %+v", runners)
	}
}
