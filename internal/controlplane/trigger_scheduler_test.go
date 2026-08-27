package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
)

type fakeGitHubTriggerClient struct {
	candidates      []GitHubCandidate
	details         GitHubIssueDetails
	permission      string
	searchErr       error
	replaceErr      error
	replaceCalls    int
	permissionActor string
}

func (f *fakeGitHubTriggerClient) SearchRequestedIssues(context.Context, []string, string, int) ([]GitHubCandidate, error) {
	return f.candidates, f.searchErr
}

func (f *fakeGitHubTriggerClient) IssueDetails(context.Context, string, int, string) (GitHubIssueDetails, error) {
	return f.details, nil
}

func (f *fakeGitHubTriggerClient) Permission(_ context.Context, _, actor string) (string, error) {
	f.permissionActor = actor
	return f.permission, nil
}

func (f *fakeGitHubTriggerClient) ReplaceRequestLabel(context.Context, string, int, string, string) error {
	f.replaceCalls++
	return f.replaceErr
}

func TestManagedGitHubTriggerCommitsBeforeLabelsAndRepairsWithoutDuplicate(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{
		Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock,
	}}); err != nil {
		t.Fatal(err)
	}
	event := &GitHubLabelEvent{ID: "123", Actor: "owner", CreatedAt: clock, OccurrenceKey: "github.com:123"}
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{{Repository: "owainlewis/machinist", Number: 396, URL: "https://github.com/owainlewis/machinist/issues/396", State: "open", CreatedAt: clock}},
		details:    GitHubIssueDetails{GitHubCandidate: GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, URL: "https://github.com/owainlewis/machinist/issues/396", State: "open", CreatedAt: clock}, Labels: []string{"machinist:requested"}, RequestedEvent: event},
		permission: "write", replaceErr: errors.New("label update failed"),
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}

	if err := server.processManagedTriggers(t.Context()); err == nil {
		t.Fatal("expected label update failure")
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].OccurrenceKey != "github.com:123" || snapshot.Triggers[0].Health != "failed" {
		t.Fatalf("snapshot after partial label update = %#v", snapshot)
	}

	clock = clock.Add(trigger.Every)
	client.replaceErr = nil
	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || client.replaceCalls != 2 || snapshot.Triggers[0].AdmissionCount != 1 {
		t.Fatalf("label repair duplicated work: jobs=%d replacements=%d trigger=%#v", len(snapshot.Jobs), client.replaceCalls, snapshot.Triggers[0])
	}
}

func TestManagedGitHubTriggerRejectsUnauthorizedActor(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}},
		details:    GitHubIssueDetails{GitHubCandidate: GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{ID: "123", Actor: "reader", CreatedAt: clock, OccurrenceKey: "github.com:123"}},
		permission: "read",
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}
	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 0 || client.replaceCalls != 0 || snapshot.Triggers[0].CandidateCount != 1 {
		t.Fatalf("unauthorized request admitted: %#v", snapshot)
	}
}

func TestManagedGitHubTriggerAdmitsAuthorizedWorkflowTokenActor(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", "examples", "github-actions", "machinist-comment-intake.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "github-token: ${{ secrets.MACHINIST_INTAKE_TOKEN }}") {
		t.Fatal("comment intake workflow does not label with its documented collaborator token")
	}

	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}},
		details: GitHubIssueDetails{
			GitHubCandidate: GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock},
			Labels:          []string{"machinist:requested"},
			RequestedEvent:  &GitHubLabelEvent{ID: "123", Actor: "machinist-intake-bot", CreatedAt: clock, OccurrenceKey: "github.com:123"},
		},
		permission: "write",
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}

	if err := server.processManagedTriggers(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if client.permissionActor != "machinist-intake-bot" || len(snapshot.Jobs) != 1 || client.replaceCalls != 1 {
		t.Fatalf("workflow actor was not admitted: actor=%q jobs=%d replacements=%d", client.permissionActor, len(snapshot.Jobs), client.replaceCalls)
	}
}

func TestManagedIntervalTriggerCoalescesBacklogAndActiveOccurrences(t *testing.T) {
	startup := time.Date(2026, 8, 27, 8, 0, 0, 0, time.UTC)
	clock := startup.Add(3*time.Hour + 30*time.Minute)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := config.ResolvedTrigger{
		Identity: "interval/audit", Family: "interval", Every: time.Hour,
		Repository: "machinist", Prompt: "Audit", SelectionKind: "agent", SelectionName: "audit", Signature: "interval-signature",
		Agents: []config.ResolvedAgent{{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute}},
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
		Repository: "machinist", Prompt: "Audit", SelectionKind: "agent", SelectionName: "audit", Signature: "interval-signature",
		Agents: []config.ResolvedAgent{{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute}},
	}
	assertFixedTriggerRetriesPendingOccurrence(t, trigger, startup.Add(time.Hour), startup.Add(2*time.Hour+30*time.Minute), startup.Add(3*time.Hour))
}

func TestManagedCronTriggerRetriesPendingOccurrenceAfterLaterDueTime(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "audit.md"), []byte("Audit: {{machinist.prompt}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configuration := `[agents.audit]
executor="test"
prompt_file="audit.md"
[github.repositories]
machinist="owainlewis/machinist"
[triggers.cron.audit]
schedule="0 * * * *"
timezone="UTC"
repository="machinist"
agent="audit"
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
	invalid.Agents = nil
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

func githubTestTrigger() config.ResolvedTrigger {
	return config.ResolvedTrigger{
		Identity: "github/intake", Family: "github", Every: 5 * time.Minute, Label: "machinist:requested",
		GitHubRepositories: map[string]string{"machinist": "owainlewis/machinist"},
		SelectionKind:      "agent", SelectionName: "foreman", Signature: "github-signature",
		Agents: []config.ResolvedAgent{{Name: "foreman", Executor: "test", Hash: "hash", Prompt: "Task: {{machinist.prompt}}", Timeout: time.Minute}},
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
