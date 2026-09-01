package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/protocol"
)

type fakeGitHubTriggerClient struct {
	candidates       []GitHubCandidate
	details          GitHubIssueDetails
	detailsByNumber  map[int]GitHubIssueDetails
	detailsSequence  []GitHubIssueDetails
	detailsErrors    map[int]error
	detailsCalls     int
	permission       string
	permissions      map[string]string
	searchErr        error
	searchStarted    chan<- struct{}
	searchRelease    <-chan struct{}
	honorLimit       bool
	replaceErr       error
	replaceCalls     int
	permissionActor  string
	permissionActors []string
}

func (f *fakeGitHubTriggerClient) SearchRequestedIssues(ctx context.Context, _ []string, label string, limit int) ([]GitHubCandidate, error) {
	if f.searchStarted != nil {
		select {
		case f.searchStarted <- struct{}{}:
		default:
		}
	}
	if f.searchRelease != nil {
		select {
		case <-f.searchRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if !f.honorLimit || f.searchErr != nil {
		return f.candidates, f.searchErr
	}
	var candidates []GitHubCandidate
	for _, candidate := range f.candidates {
		details, ok := f.detailsByNumber[candidate.Number]
		if ok && hasGitHubLabel(details.Labels, label) {
			candidates = append(candidates, candidate)
		}
		if len(candidates) == limit {
			break
		}
	}
	return candidates, nil
}

func (f *fakeGitHubTriggerClient) IssueDetails(_ context.Context, _ string, number int, _ string) (GitHubIssueDetails, error) {
	call := f.detailsCalls
	f.detailsCalls++
	if err := f.detailsErrors[call]; err != nil {
		return GitHubIssueDetails{}, err
	}
	if len(f.detailsSequence) > 0 {
		if call >= len(f.detailsSequence) {
			call = len(f.detailsSequence) - 1
		}
		return f.detailsSequence[call], nil
	}
	if details, ok := f.detailsByNumber[number]; ok {
		return details, nil
	}
	return f.details, nil
}

func (f *fakeGitHubTriggerClient) Permission(_ context.Context, _, actor string) (string, error) {
	f.permissionActor = actor
	f.permissionActors = append(f.permissionActors, actor)
	if permission, ok := f.permissions[actor]; ok {
		return permission, nil
	}
	return f.permission, nil
}

func (f *fakeGitHubTriggerClient) AcknowledgeRequest(_ context.Context, _ string, number int, requestedLabel, queuedLabel string, accepted bool) error {
	f.replaceCalls++
	if f.replaceErr != nil {
		return f.replaceErr
	}
	if len(f.detailsSequence) > 0 {
		return nil
	}
	details := f.details
	if mapped, ok := f.detailsByNumber[number]; ok {
		details = mapped
	}
	labels := details.Labels[:0]
	for _, label := range details.Labels {
		if !strings.EqualFold(label, requestedLabel) {
			labels = append(labels, label)
		}
	}
	if accepted && !hasGitHubLabel(labels, queuedLabel) {
		labels = append(labels, queuedLabel)
	}
	details.Labels = labels
	if f.detailsByNumber != nil {
		f.detailsByNumber[number] = details
	} else {
		f.details = details
	}
	return nil
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
		details:    GitHubIssueDetails{GitHubCandidate: GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, Title: "Make cards readable", URL: "https://github.com/owainlewis/machinist/issues/396", State: "open", CreatedAt: clock}, Labels: []string{"machinist:requested"}, RequestedEvent: event},
		permission: "write", replaceErr: errors.New("label update failed"),
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}

	if err := processManagedTriggers(t.Context(), server); err == nil {
		t.Fatal("expected label update failure")
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].OccurrenceKey != "github.com:123" || snapshot.Jobs[0].GitHubIssueTitle != "Make cards readable" || snapshot.Triggers[0].Health != "failed" {
		t.Fatalf("snapshot after partial label update = %#v", snapshot)
	}

	clock = clock.Add(trigger.Every)
	client.replaceErr = nil
	if err := processManagedTriggers(t.Context(), server); err != nil {
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

func TestManagedGitHubTriggerFinishesAdmittedLabelRepairAfterRepositoryRemoval(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{candidate},
		details: GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{
			ID: "123", Actor: "owner", CreatedAt: clock, OccurrenceKey: "github.com:123",
		}},
		permission: "write", replaceErr: errors.New("label update failed"),
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}
	if err := processManagedTriggers(t.Context(), server); err == nil {
		t.Fatal("expected initial label update failure")
	}

	current := trigger
	current.Signature = "github-signature-v2"
	current.GitHubRepositories = map[string]string{"other": "owainlewis/other"}
	clock = clock.Add(trigger.Every)
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: current.Identity, Family: current.Family, ConfigSignature: current.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	server.triggers = []config.ResolvedTrigger{current}
	client.replaceErr = nil
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if client.replaceCalls != 2 || hasGitHubLabel(client.details.Labels, trigger.Label) || !hasGitHubLabel(client.details.Labels, queuedGitHubLabel) {
		t.Fatalf("admitted label repair was discarded after repository removal: calls=%d labels=%v", client.replaceCalls, client.details.Labels)
	}
	reconciliations, err := store.GitHubTriggerReconciliations(t.Context(), trigger.Identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciliations) != 0 {
		t.Fatalf("completed reconciliation remains pending: %#v", reconciliations)
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
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 0 || client.replaceCalls != 1 || snapshot.Triggers[0].CandidateCount != 1 {
		t.Fatalf("unauthorized request admitted: %#v", snapshot)
	}
}

func TestManagedGitHubTriggerRecoversNewerRequestAfterPostRemovalReadFailure(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	database := filepath.Join(t.TempDir(), "machinist.db")
	store, err := OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return clock }
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: 396, State: "open", CreatedAt: clock}
	old := GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{ID: "41", Actor: "first-owner", CreatedAt: clock, OccurrenceKey: "github.com:41"}}
	newer := GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:queued"}, RequestedEvent: &GitHubLabelEvent{ID: "42", Actor: "second-owner", CreatedAt: clock.Add(time.Second), OccurrenceKey: "github.com:42"}}
	mutatedTimeline := newer
	mutatedEvent := *newer.RequestedEvent
	mutatedEvent.Actor = "machinist"
	mutatedTimeline.RequestedEvent = &mutatedEvent
	client := &fakeGitHubTriggerClient{
		candidates: []GitHubCandidate{candidate}, detailsSequence: []GitHubIssueDetails{old, old, newer, newer, mutatedTimeline},
		detailsErrors: map[int]error{2: errors.New("post-removal timeline read failed")}, permission: "write",
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}

	if err := processManagedTriggers(t.Context(), server); err == nil {
		t.Fatal("expected the post-removal read to fail")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.now = func() time.Time { return clock }
	currentTrigger := trigger
	currentTrigger.Signature = "github-signature-v2"
	currentTrigger.Label = "machinist:pickup"
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: currentTrigger.Identity, Family: currentTrigger.Family, ConfigSignature: currentTrigger.Signature, NextDueAt: currentTrigger.FirstDue(clock)}}); err != nil {
		t.Fatal(err)
	}
	server.store = store
	server.triggers = []config.ResolvedTrigger{currentTrigger}
	delete(client.detailsErrors, 2)
	clock = clock.Add(currentTrigger.Every)
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 {
		t.Fatalf("newer request overlapped active work: %#v", snapshot.Jobs)
	}
	run, err := store.Poll(t.Context(), pollRequest("worker-a", []string{"test"}, []string{"machinist"}))
	if err != nil || run == nil {
		t.Fatalf("poll first request = %#v, %v", run, err)
	}
	if err := store.Complete(t.Context(), run.ID, protocol.Completion{InstanceID: "worker-a", LeaseToken: run.LeaseToken, State: "succeeded", ExitCode: 0}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(currentTrigger.Every)
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newerAdmitted := false
	for _, job := range snapshot.Jobs {
		newerAdmitted = newerAdmitted || job.OccurrenceKey == "github.com:42"
	}
	if len(snapshot.Jobs) != 2 || !newerAdmitted {
		t.Fatalf("newer durable request was not admitted: %#v", snapshot.Jobs)
	}
	if !slices.Contains(client.permissionActors, "second-owner") || slices.Contains(client.permissionActors, "machinist") {
		t.Fatalf("permission actors = %v, want original newer actor", client.permissionActors)
	}
}

func TestManagedGitHubTriggerConsumesUnauthorizedCandidatesBeyondSearchWindow(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	trigger := githubTestTrigger()
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{{Identity: trigger.Identity, Family: trigger.Family, ConfigSignature: trigger.Signature, NextDueAt: clock}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitHubTriggerClient{honorLimit: true, detailsByNumber: map[int]GitHubIssueDetails{}, permissions: map[string]string{"writer": "write"}, permission: "read"}
	for number := 1; number <= maxGitHubCandidates+1; number++ {
		actor := "reader"
		if number == maxGitHubCandidates+1 {
			actor = "writer"
		}
		candidate := GitHubCandidate{Repository: "owainlewis/machinist", Number: number, State: "open", CreatedAt: clock.Add(time.Duration(number) * time.Second)}
		client.candidates = append(client.candidates, candidate)
		client.detailsByNumber[number] = GitHubIssueDetails{GitHubCandidate: candidate, Labels: []string{"machinist:requested"}, RequestedEvent: &GitHubLabelEvent{ID: fmt.Sprint(number), Actor: actor, CreatedAt: candidate.CreatedAt, OccurrenceKey: fmt.Sprintf("github.com:%d", number)}}
	}
	server := &Server{store: store, triggers: []config.ResolvedTrigger{trigger}, github: client, now: func() time.Time { return clock }}
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(trigger.Every)
	if err := processManagedTriggers(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].OccurrenceKey != fmt.Sprintf("github.com:%d", maxGitHubCandidates+1) {
		t.Fatalf("valid request behind rejected search window was not admitted: %#v", snapshot.Jobs)
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
	if !strings.Contains(string(workflow), "MACHINIST_REQUEST_LABEL: machinist:requested") || !strings.Contains(string(workflow), "labels: [process.env.MACHINIST_REQUEST_LABEL]") {
		t.Fatal("comment intake workflow does not expose its configured request label")
	}
	if !strings.Contains(string(workflow), "error.status !== 404") {
		t.Fatal("comment intake workflow does not safely deny non-collaborators")
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

	if err := processManagedTriggers(t.Context(), server); err != nil {
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

func TestManagedTriggerSchedulersIsolateBlockedGitHubPoll(t *testing.T) {
	clock := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	store := openManagedTriggerTestStore(t, &clock)
	githubTrigger := githubTestTrigger()
	intervalTrigger := config.ResolvedTrigger{
		Identity: "interval/audit", Family: "interval", Every: time.Hour,
		Repository: "machinist", Prompt: "Audit", SelectionName: "audit", Signature: "interval-signature",
		Command: config.ResolvedCommand{Name: "audit", Executor: "test", Hash: "hash", Prompt: "Audit", Timeout: time.Minute},
	}
	if err := store.SyncTriggers(t.Context(), []TriggerDefinition{
		{Identity: githubTrigger.Identity, Family: githubTrigger.Family, ConfigSignature: githubTrigger.Signature, NextDueAt: clock},
		{Identity: intervalTrigger.Identity, Family: intervalTrigger.Family, ConfigSignature: intervalTrigger.Signature, NextDueAt: clock},
	}); err != nil {
		t.Fatal(err)
	}
	searchStarted := make(chan struct{}, 1)
	searchRelease := make(chan struct{})
	server := &Server{
		store: store, triggers: []config.ResolvedTrigger{githubTrigger, intervalTrigger},
		github: &fakeGitHubTriggerClient{searchStarted: searchStarted, searchRelease: searchRelease},
		now:    func() time.Time { return clock }, schedulerEvery: 5 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.runScheduler(ctx) }()
	select {
	case <-searchStarted:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("GitHub trigger did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, err := store.Snapshot(t.Context())
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if len(snapshot.Jobs) == 1 && snapshot.Jobs[0].TriggerID == intervalTrigger.Identity {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("interval trigger was blocked by GitHub poll: %#v", snapshot.Jobs)
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(searchRelease)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
}

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
	if err := processManagedTriggers(t.Context(), server); err != nil {
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
	if err := processManagedTriggers(t.Context(), server); err != nil {
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
	if err := processManagedTriggers(t.Context(), server); err == nil {
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
	if err := processManagedTriggers(t.Context(), server); err != nil {
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
		SelectionName:      "foreman", Signature: "github-signature",
		Command: config.ResolvedCommand{Name: "foreman", Executor: "test", Hash: "hash", Prompt: "Task: {{machinist.prompt}}", Timeout: time.Minute},
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

func processManagedTriggers(ctx context.Context, server *Server) error {
	var failures []error
	for _, trigger := range server.triggers {
		if err := server.processManagedTrigger(ctx, trigger); err != nil {
			failures = append(failures, fmt.Errorf("trigger %q: %w", trigger.Identity, err))
		}
	}
	return errors.Join(failures...)
}
