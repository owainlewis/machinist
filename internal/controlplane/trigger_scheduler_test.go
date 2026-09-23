package controlplane

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

func TestManagedTriggerRejectsStaleConfigurationSnapshot(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := config.ResolvedTrigger{
		Identity: "interval/audit", Family: "interval", Every: time.Hour,
		Repository: "machinist", Prompt: "Audit", SelectionName: "audit", Signature: "v1",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute},
	}
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: "v2", NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, now: func() time.Time { return clock }}
	if err := server.processManagedTrigger(t.Context(), trigger); !errors.Is(err, ErrTriggerStale) {
		t.Fatalf("stale trigger error = %v, want ErrTriggerStale", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 0 {
		t.Fatalf("stale trigger admitted jobs: %#v", snapshot.Jobs)
	}
}

func TestManagedIntervalTriggerCoalescesBacklogAndActiveOccurrences(t *testing.T) {
	startup := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	clock := startup.Add(3*time.Hour + 30*time.Minute)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := config.ResolvedTrigger{
		Identity: "interval/audit", Family: "interval", Every: time.Hour,
		Repository: "machinist", Prompt: "Audit", SelectionName: "audit", Signature: "interval-signature",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute},
	}
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: startup.Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, now: func() time.Time { return clock }}
	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantOccurrence := startup.Add(3 * time.Hour).Format(time.RFC3339Nano)
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].OccurrenceKey != wantOccurrence || snapshot.Triggers[0].CoalescedCount != 2 {
		t.Fatalf("catch-up state = %#v", snapshot)
	}

	clock = startup.Add(4 * time.Hour)
	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Triggers[0].CoalescedCount != 3 || snapshot.Triggers[0].Health != "coalesced" || snapshot.Triggers[0].NextDueAt == nil || !snapshot.Triggers[0].NextDueAt.Equal(startup.Add(5*time.Hour)) {
		t.Fatalf("active coalescing state = %#v", snapshot)
	}
}

func TestManagedIntervalTriggerRetriesPendingOccurrenceAfterLaterDueTime(t *testing.T) {
	startup := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	trigger := config.ResolvedTrigger{
		Identity: "interval/audit", Family: "interval", Every: time.Hour,
		Repository: "machinist", Prompt: "Audit", SelectionName: "audit", Signature: "interval-signature",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute},
	}
	assertFixedTriggerRetriesPendingOccurrence(t, trigger, startup.Add(time.Hour), startup.Add(2*time.Hour+30*time.Minute), startup.Add(3*time.Hour))
}

func TestManagedCronTriggerRetriesPendingOccurrenceAfterLaterDueTime(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "audit.md"), []byte("Audit: {{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := `[commands.audit]
executor="test"
prompt_file="audit.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.cron.audit]
schedule="0 * * * *"
timezone="UTC"
repository="machinist"
command="audit"
prompt="Audit"
`
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.LoadTriggers(path)
	if err != nil {
		t.Fatal(err)
	}
	startup := time.Date(2026, 8, 27, 0, 30, 0, 0, time.UTC)
	assertFixedTriggerRetriesPendingOccurrence(t, resolved[0], startup.Add(30*time.Minute), startup.Add(2*time.Hour), startup.Add(2*time.Hour+30*time.Minute))
}

func TestManagedFixedTriggerWaitsForPreviousConfigurationJobAcrossABA(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	pendingDue := clock
	store := openManagedTriggerTestStore(t, &clock)
	identity := "interval/audit"
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v1", NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateTriggeredJob(t.Context(), TriggerAdmission{
		Identity: identity, Family: "interval", ConfigSignature: "v1", ConfigGeneration: mustTriggerGeneration(t, store, identity),
		ScheduledAt: clock.Add(-time.Hour), NextDueAt: clock,
		Prompt: "Old audit", Repository: "machinist", SelectionName: "audit",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "v1", Prompt: "Old audit", Timeout: time.Minute},
	})
	if err != nil || !created {
		t.Fatalf("admit v1 job = %v, %v", created, err)
	}
	trigger := config.ResolvedTrigger{
		Identity: identity, Family: "interval", Every: time.Hour, Signature: "v1",
		Repository: "machinist", Prompt: "New audit", SelectionName: "audit",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "v1-new", Prompt: "New audit", Timeout: time.Minute},
	}
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v2", NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: identity, Family: "interval", ConfigSignature: "v1", NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, now: func() time.Time { return clock }}
	if err := server.processManagedTrigger(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status := snapshot.Triggers[0]
	if len(snapshot.Jobs) != 1 || status.PendingOccurrenceAt == nil || !status.PendingOccurrenceAt.Equal(pendingDue) || status.NextDueAt == nil || !status.NextDueAt.Equal(pendingDue) || status.CoalescedCount != 0 || status.Health != "healthy" || status.ActiveJobID != "" {
		t.Fatalf("new A occurrence was not preserved behind old A work: %#v", snapshot)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll v1 job = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	clock = pendingDue.Add(3 * time.Hour)
	if err := server.processManagedTrigger(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	v2Admitted := false
	for _, job := range snapshot.Jobs {
		v2Admitted = v2Admitted || job.OccurrenceKey == pendingDue.Format(time.RFC3339Nano)
	}
	if len(snapshot.Jobs) != 2 || !v2Admitted || snapshot.Triggers[0].PendingOccurrenceAt != nil || snapshot.Triggers[0].CoalescedCount != 3 {
		t.Fatalf("new A occurrence was not admitted after old A completion: %#v", snapshot)
	}
}

func assertFixedTriggerRetriesPendingOccurrence(t *testing.T, trigger config.ResolvedTrigger, firstDue, retryAt, wantNext time.Time) {
	t.Helper()
	database := filepath.Join(t.TempDir(), "machinist.db")
	clock := firstDue
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return clock }
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: firstDue}}); err != nil {
		t.Fatal(err)
	}

	invalid := trigger
	invalid.Command = config.ResolvedCommand{}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{invalid}, now: func() time.Time { return clock }}
	if err := server.processManagedTriggers(t.Context()); err == nil {
		t.Fatal("expected first admission to fail")
	}
	statuses, err := store.TriggerSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if statuses[0].PendingOccurrenceAt == nil || !statuses[0].PendingOccurrenceAt.Equal(firstDue) {
		t.Fatalf("pending occurrence after failure = %#v, want %s", statuses[0].PendingOccurrenceAt, firstDue)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	clock = retryAt
	reopened, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.now = func() time.Time { return clock }
	if err := reopened.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: trigger.FirstDue(clock)}}); err != nil {
		t.Fatal(err)
	}
	server = &Server{store: reopened, triggers: []config.ResolvedTrigger{trigger}, now: func() time.Time { return clock }}
	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantOccurrence := firstDue.Format(time.RFC3339Nano)
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].OccurrenceKey != wantOccurrence {
		t.Fatalf("retried jobs = %#v, want occurrence %s", snapshot.Jobs, wantOccurrence)
	}
	status := snapshot.Triggers[0]
	if status.PendingOccurrenceAt != nil || status.NextDueAt == nil || !status.NextDueAt.Equal(wantNext) || status.CoalescedCount != 1 {
		t.Fatalf("trigger after retry = %#v, want next %s and one coalesced occurrence", status, wantNext)
	}
}

func openManagedTriggerTestStore(t *testing.T, clock *time.Time) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "machinist.db"))
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return *clock }
	t.Cleanup(func() { _ = store.Close() })
	return store
}
