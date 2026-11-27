package tickets

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/owainlewis/machinist/internal/protocol"
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
	if err := store.RecordRunner(t.Context(), "mac-mini", "local", []string{"claude"}, []string{"MacOS", "arm64", "macos"}, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "cluster", "kubernetes", []string{"claude", "codex"}, []string{"gpu", "k8s"}, 8, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "mac-mini", "local", nil, []string{"macos"}, 2, false); err != nil {
		t.Fatal(err)
	}
	runners, queue := fleet(t, store)
	if len(runners) != 2 || runners[0].Name != "cluster" || runners[1].Name != "mac-mini" {
		t.Fatalf("runners = %+v", runners)
	}
	cluster, mini := runners[0], runners[1]
	if cluster.Kind != "kubernetes" || cluster.Slots != 8 || strings.Join(cluster.Harnesses, ",") != "claude,codex" || strings.Join(cluster.Labels, ",") != "gpu,k8s" {
		t.Fatalf("cluster = %+v", cluster)
	}
	if mini.Slots != 2 || mini.Harnesses == nil || len(mini.Harnesses) != 0 || strings.Join(mini.Labels, ",") != "macos" || mini.Jobs == nil || len(mini.Jobs) != 0 {
		t.Fatalf("mac-mini = %+v", mini)
	}
	if queue.Waiting != 0 || queue.Oldest != nil {
		t.Fatalf("queue = %+v", queue)
	}
}

func TestRecordRunnerTreatsNoSlotsAsOne(t *testing.T) {
	store := openStore(t)
	if err := store.RecordRunner(t.Context(), "old-runner", "", []string{"claude"}, nil, 0, false); err != nil {
		t.Fatal(err)
	}
	if runners, _ := fleet(t, store); runners[0].Slots != 1 || runners[0].Labels == nil || len(runners[0].Labels) != 0 {
		t.Fatalf("runner = %+v", runners[0])
	}
}

func TestTouchRunnerMovesLastContact(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, nil, 1, false); err != nil {
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
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, nil, 2, false); err != nil {
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
	if _, err := store.CreateToken(t.Context(), "runner-1", RoleAll); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeToken(t.Context(), "runner-1"); err != nil {
		t.Fatal(err)
	}
	if runners, _ := fleet(t, store); len(runners) != 0 {
		t.Fatalf("runners = %+v", runners)
	}
}

func TestFleetShowsWhatARunningJobIsDoing(t *testing.T) {
	store := openStore(t)
	if err := store.RecordRunner(t.Context(), "runner-1", "local", []string{"claude"}, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	claim := claimRun(t, store, createTicket(t, store, "api:1"))
	output := func(sequence int, stream, text string) json.RawMessage {
		body, _ := json.Marshal(protocol.RunEvent{Sequence: int64(sequence), Type: "process.output", Stream: stream, Encoding: "base64", Data: base64.StdEncoding.EncodeToString([]byte(text))})
		return body
	}
	started := func(sequence int, name, at string) json.RawMessage {
		body, _ := json.Marshal(protocol.RunEvent{Sequence: int64(sequence), Type: "step.started", Message: "name=" + name, Time: at})
		return body
	}
	ingest := func(events ...json.RawMessage) RunnerJob {
		t.Helper()
		if _, err := store.IngestRunEvents(t.Context(), claim.Run.ID, events); err != nil {
			t.Fatal(err)
		}
		runners, _ := fleet(t, store)
		return runners[0].Jobs[0]
	}

	if job := ingest(started(1, "checkout", "2026-11-10T09:00:00Z")); job.Step != "checkout" || job.StepStartedAt == nil || job.Action != "" {
		t.Fatalf("job = %+v", job)
	}
	job := ingest(
		started(2, "work", "2026-11-10T09:01:00Z"),
		output(3, "stdout", "Claude Code started\n▸ Read go.mod\nThinking\n▸ Run go test ./...\n"),
		output(4, "stderr", "▸ not an action on stderr\n"),
		output(5, "stdout", "Plain output\n"),
	)
	if job.Step != "work" || !job.StepStartedAt.Equal(time.Date(2026, 11, 10, 9, 1, 0, 0, time.UTC)) || job.Action != "Run go test ./..." {
		t.Fatalf("job = %+v", job)
	}
	if job := ingest(output(6, "stdout", "▸ Edit src/"), output(7, "stdout", "main.go\n▸ Run go te")); job.Action != "Edit src/main.go" {
		t.Fatalf("an action split across events = %q", job.Action)
	}
	if job := ingest(output(8, "stdout", "st ./...\n")); job.Action != "Run go test ./..." {
		t.Fatalf("an action split across batches = %q", job.Action)
	}
	if job := ingest(started(9, "publish", "2026-11-10T09:05:00Z")); job.Step != "publish" || job.Action != "" {
		t.Fatalf("a new step kept the last one's action: %+v", job)
	}
}

func TestRemoveRunnerForgetsAnIdleOfflineRunner(t *testing.T) {
	store := openStore(t)
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	store.SetClock(func() time.Time { return now })
	for _, name := range []string{"old-laptop", "runner-1", "mac-mini"} {
		if err := store.RecordRunner(t.Context(), name, "local", []string{"claude"}, nil, 1, false); err != nil {
			t.Fatal(err)
		}
	}
	claimRun(t, store, createTicket(t, store, "api:1"))
	now = now.Add(time.Hour)
	if err := store.TouchRunner(t.Context(), "mac-mini"); err != nil {
		t.Fatal(err)
	}
	onlineSince := now.Add(-time.Minute)

	if err := store.RemoveRunner(t.Context(), "runner-1", onlineSince); !errors.Is(err, ErrRunnerBusy) {
		t.Fatalf("removing a runner with a running job: err = %v", err)
	}
	if err := store.RemoveRunner(t.Context(), "mac-mini", onlineSince); !errors.Is(err, ErrRunnerOnline) {
		t.Fatalf("removing an online runner: err = %v", err)
	}
	if err := store.RemoveRunner(t.Context(), "nobody", onlineSince); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing an unknown runner: err = %v", err)
	}
	if err := store.RemoveRunner(t.Context(), "old-laptop", onlineSince); err != nil {
		t.Fatal(err)
	}
	runners, _ := fleet(t, store)
	if len(runners) != 2 || runners[0].Name != "mac-mini" || runners[1].Name != "runner-1" {
		t.Fatalf("runners = %+v", runners)
	}
	// A removed runner that asks for work again is back.
	if err := store.RecordRunner(t.Context(), "old-laptop", "local", nil, nil, 1, false); err != nil {
		t.Fatal(err)
	}
	if runners, _ := fleet(t, store); len(runners) != 3 {
		t.Fatalf("runners = %+v", runners)
	}
}

func TestQueuedSpecsCountsWaitingRunsByPreset(t *testing.T) {
	store := openStore(t)
	claimRun(t, store, createTicket(t, store, "api:1"))
	for _, key := range []string{"api:2", "api:3"} {
		if _, err := store.QueueRun(t.Context(), createTicket(t, store, key).ID, "steps"); err != nil {
			t.Fatal(err)
		}
	}
	other := request("api:4")
	other.Agent = "review"
	ticket, _, err := store.CreateTicket(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueRun(t.Context(), ticket.ID, "steps"); err != nil {
		t.Fatal(err)
	}
	specs, err := store.QueuedSpecs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	presets := map[string]int{}
	for _, spec := range specs {
		presets[spec.Preset] += spec.Waiting
	}
	preset := request("api:5").Agent
	if len(presets) != 2 || presets[preset] != 2 || presets["review"] != 1 {
		t.Fatalf("specs = %+v", specs)
	}
}
