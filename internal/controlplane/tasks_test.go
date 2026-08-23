package controlplane

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestTaskAdmissionWorkerLifecycleAndAggregateRun(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Review factory", Prompt: "Review the repository for real bugs.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.ConcurrencyLimit != 10 || len(task.Repositories) != 1 {
		t.Fatalf("task = %#v", task)
	}
	detail, created, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "review-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !created || detail.Run.State != protocol.RunQueued || len(detail.Sessions) != 1 {
		t.Fatalf("admitted Run = %#v", detail)
	}
	replayed, created, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "review-1"})
	if err != nil || created || replayed.Run.ID != detail.Run.ID {
		t.Fatalf("replayed Run = %#v, created %v, err %v", replayed, created, err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "claim-1", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if claim.Session.ID != detail.Sessions[0].ID || claim.Session.RunID != detail.Run.ID {
		t.Fatalf("claim does not identify Run Session: %#v", claim)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "No blocking bugs.",
	}); err != nil {
		t.Fatal(err)
	}
	detail, err = store.Run(context.Background(), detail.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.State != protocol.RunSucceeded || detail.Run.SucceededCount != 1 || detail.Run.TerminalAt == nil {
		t.Fatalf("completed Run = %#v", detail.Run)
	}
	if detail.Sessions[0].Result != "No blocking bugs." || len(detail.Sessions[0].Attempts) != 1 {
		t.Fatalf("completed Session = %#v", detail.Sessions[0])
	}
}

func TestRunPagePreservesRepositorySummaryWithoutPrompt(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Board summary", Prompt: "Do not expose this prompt.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	admitted, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "board-summary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE runs SET task_snapshot = json_set(
			json_remove(task_snapshot, '$.repositories'), '$.repository_ids', json_array(?)
		) WHERE id = ?
	`, worker.Repositories[0].ID, admitted.Run.ID); err != nil {
		t.Fatal(err)
	}
	page, err := store.RunPage(context.Background(), 10, "")
	if err != nil || len(page.Runs) != 1 {
		t.Fatalf("Run page = %#v, err %v", page, err)
	}
	summary := page.Runs[0].Task
	if summary.Prompt != "" || summary.TimeoutSeconds != 0 || summary.ConcurrencyLimit != 0 {
		t.Fatalf("Run page leaked Task execution detail: %#v", summary)
	}
	if len(summary.Repositories) != 1 || summary.Repositories[0].RemoteIdentity != "github.com/owainlewis/factory" {
		t.Fatalf("Run page repository summary = %#v", summary.Repositories)
	}
	if page.Runs[0].OutcomeContract != protocol.OutcomeProcessExit || len(page.Runs[0].Targets) != 1 ||
		page.Runs[0].Targets[0].RepositoryID != worker.Repositories[0].ID {
		t.Fatalf("Run page frozen Work fields = %#v", page.Runs[0])
	}
}

func TestTaskRunReplayReturnsCommittedRunAfterTaskChanges(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Replay after edit", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, wasCreated, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "ambiguous-run"})
	if err != nil || !wasCreated {
		t.Fatalf("initial Run = %#v, created %v, err %v", created, wasCreated, err)
	}
	if _, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: "Replay after edit", Prompt: "Use the changed prompt.", Runtime: protocol.RuntimeCodex,
		ExpectedGeneration: task.Generation,
	}); err != nil {
		t.Fatal(err)
	}
	replayed, wasCreated, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "ambiguous-run"})
	if err != nil || wasCreated || replayed.Run.ID != created.Run.ID {
		t.Fatalf("replayed Run = %#v, created %v, err %v", replayed, wasCreated, err)
	}
	if replayed.Run.Task.Prompt != "Review the repository." {
		t.Fatalf("replayed immutable Task snapshot = %#v", replayed.Run.Task)
	}
}

func TestTaskRunReplayRejectsDifferentImmutableIdentity(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	createTask := func(name string) protocol.Task {
		task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
			Name: name, Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
			RepositoryIDs: []string{worker.Repositories[0].ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	first := createTask("First identity")
	second := createTask("Second identity")
	detail, _, err := store.RunTask(context.Background(), first.ID, protocol.RunTaskRequest{RequestKey: "identity-key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), second.ID, protocol.RunTaskRequest{RequestKey: "identity-key"}); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different Task identity error = %v", err)
	}
	firstDue := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)
	if _, _, err := store.admitTask(context.Background(), first.ID, "schedule", "identity-key", &firstDue, nil, ""); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different source identity error = %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE runs SET source = 'schedule', scheduled_at = ? WHERE id = ?
	`, firstDue.UnixMilli(), detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	secondDue := firstDue.Add(time.Hour)
	if _, _, err := store.admitTask(context.Background(), first.ID, "schedule", "identity-key", &secondDue, nil, ""); !serviceErrorCode(err, "request_key_conflict") {
		t.Fatalf("different scheduled instant identity error = %v", err)
	}
}

func TestRunDetailClosesSessionRowsBeforeLoadingAttempts(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Connection-safe detail", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "connection-safe-detail"})
	if err != nil {
		t.Fatal(err)
	}
	store.db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	loaded, err := store.Run(ctx, detail.Run.ID)
	if err != nil || len(loaded.Sessions) != 1 {
		t.Fatalf("Run detail with one connection = %#v, err %v", loaded, err)
	}
}

func TestTaskLimitCountsOnlyEditableTasks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 500
		)
		INSERT INTO tasks(
			id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
			generation, archived, migration_only, read_only, schedule_enabled,
			schedule_health_status, created_at, updated_at
		)
		SELECT 'history-' || value, 'History ' || value, 'history ' || value,
			'Preserved history', 'codex', 7200, 10, 1, 1, 0, 1, 0, 'disabled', 0, 0
		FROM sequence
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "First editable", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); err != nil {
		t.Fatalf("read-only history consumed the Task limit: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		WITH RECURSIVE sequence(value) AS (
			SELECT 1 UNION ALL SELECT value + 1 FROM sequence WHERE value < 499
		)
		INSERT INTO tasks(
			id, name, name_key, prompt, runtime, timeout_seconds, concurrency_limit,
			generation, archived, migration_only, read_only, schedule_enabled,
			schedule_health_status, created_at, updated_at
		)
		SELECT 'editable-' || value, 'Editable ' || value, 'editable ' || value,
			'Review.', 'codex', 7200, 10, 1, 0, 0, 0, 0, 'disabled', 0, 0
		FROM sequence
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(ctx, protocol.SaveTaskRequest{
		Name: "Over limit", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "task_limit_reached") {
		t.Fatalf("editable Task limit error = %v", err)
	}
}

func TestTaskNamesUseUnicodeCaseNormalization(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Éclair", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "éCLAIR", Prompt: "Review again.", Runtime: protocol.RuntimeCodex,
	}); !serviceErrorCode(err, "task_name_conflict") {
		t.Fatalf("Unicode Task name conflict error = %v", err)
	}
}

func TestTaskArchiveRequiresExplicitState(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Archive guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskArchived(context.Background(), task.ID, protocol.SetTaskArchivedRequest{
		ExpectedGeneration: task.Generation,
	}); !serviceErrorCode(err, "task_archived_required") {
		t.Fatalf("missing archived state error = %v", err)
	}
	unchanged, err := store.Task(context.Background(), task.ID)
	if err != nil || unchanged.Archived || unchanged.Generation != task.Generation {
		t.Fatalf("Task changed after malformed archive request = %#v, err %v", unchanged, err)
	}
}

func TestManualTaskRunRejectsSchedulerRequestKeys(t *testing.T) {
	store := newTestStore(t)
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Reserved key guard", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "schedule:" + task.ID + ":1:1234",
	}); !serviceErrorCode(err, "reserved_request_key") {
		t.Fatalf("reserved scheduler request key error = %v", err)
	}
}

func TestCancelSessionCancelsOnlyTheSelectedSession(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"},
		protocol.RepositoryRegistration{Key: "neo", RemoteIdentity: "github.com/owainlewis/neo"},
	)
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Two repository review", Prompt: "Review both repositories.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID, worker.Repositories[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "two-sessions"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err = store.CancelSession(context.Background(), detail.Run.ID, detail.Sessions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Sessions[0].State != protocol.SessionCancelled || detail.Sessions[1].State != protocol.SessionQueued || detail.Run.State != protocol.RunRunning {
		t.Fatalf("partially cancelled Run = %#v", detail)
	}
}

func TestRunAggregateUsesCanonicalPrecedence(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		states []protocol.SessionState
		expect protocol.RunState
		active int
	}{
		{name: "blocked", states: []protocol.SessionState{protocol.SessionBlocked}, expect: protocol.RunBlocked, active: 1},
		{name: "queued", states: []protocol.SessionState{protocol.SessionQueued}, expect: protocol.RunQueued, active: 1},
		{name: "mixed active", states: []protocol.SessionState{protocol.SessionSucceeded, protocol.SessionQueued}, expect: protocol.RunRunning, active: 1},
		{name: "ready and queued", states: []protocol.SessionState{protocol.SessionReady, protocol.SessionQueued}, expect: protocol.RunRunning, active: 1},
		{name: "no change and blocked", states: []protocol.SessionState{protocol.SessionNoChange, protocol.SessionBlocked}, expect: protocol.RunRunning, active: 1},
		{name: "failed and cancelled", states: []protocol.SessionState{protocol.SessionFailed, protocol.SessionCancelled}, expect: protocol.RunFailed},
		{name: "partial", states: []protocol.SessionState{protocol.SessionSucceeded, protocol.SessionFailed}, expect: protocol.RunPartial},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessions := make([]protocol.Session, len(test.states))
			for index, state := range test.states {
				sessions[index].State = state
			}
			run := protocol.Run{}
			applyRunAggregate(&run, sessions, now)
			if run.State != test.expect || run.ActiveCount != test.active {
				t.Fatalf("aggregate = state %s active %d, want %s active %d", run.State, run.ActiveCount, test.expect, test.active)
			}
		})
	}
	run := protocol.Run{}
	applyRunAggregate(&run, []protocol.Session{{
		State: protocol.SessionBlocked, BlockedReason: taskConcurrencyBlockedReason,
	}}, now)
	if run.NeedsAttention {
		t.Fatal("normal Task concurrency throttling needs attention")
	}
	applyRunAggregate(&run, []protocol.Session{{
		State: protocol.SessionBlocked, BlockedReason: "No compatible Worker is online.",
	}}, now)
	if !run.NeedsAttention {
		t.Fatal("actionable Worker block does not need attention")
	}
}

func TestOverviewDoesNotFlagTaskConcurrencyThrottling(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10,
		protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"},
		protocol.RepositoryRegistration{Key: "neo", RemoteIdentity: "github.com/owainlewis/neo"},
	)
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Throttled review", Prompt: "Review both repositories.", Runtime: protocol.RuntimeCodex,
		ConcurrencyLimit: 1,
		RepositoryIDs:    []string{worker.Repositories[0].ID, worker.Repositories[1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), task.ID,
		protocol.RunTaskRequest{RequestKey: "throttled-review"}); err != nil {
		t.Fatal(err)
	}
	overview, err := store.Overview(context.Background())
	if err != nil || overview.ActiveRuns != 1 || overview.NeedsAttention != 0 {
		t.Fatalf("throttled Overview = %#v, err %v", overview, err)
	}
}

func TestTaskScheduleUsesFrozenOccurrencePromptAndSkipsMissedInstants(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Morning review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	if err := store.AdmitDueTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.RunPage(context.Background(), 10, "")
	if err != nil || len(page.Runs) != 1 {
		t.Fatalf("scheduled Run page = %#v, err %v", page, err)
	}
	detail, err := store.Run(context.Background(), page.Runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Source != "schedule" || detail.Run.ScheduledAt == nil ||
		!strings.Contains(detail.Sessions[0].ResolvedPrompt, "Trusted schedule occurrence") {
		t.Fatalf("scheduled Run = %#v", detail)
	}
	updated, err := store.Task(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantNext := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	if updated.Schedule.NextDueAt == nil || !updated.Schedule.NextDueAt.Equal(wantNext) {
		t.Fatalf("next due = %v, want %v", updated.Schedule.NextDueAt, wantNext)
	}
}

func TestTaskScheduleReservesPromptCapacityForOccurrenceMetadata(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Manual oversized schedule", Prompt: strings.Repeat("x", protocol.MaxTaskPromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: task.Name, Prompt: strings.Repeat("x", protocol.MaxTaskPromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:           protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
		ExpectedGeneration: task.Generation,
	})
	if !serviceErrorCode(err, "task_schedule_prompt_too_large") {
		t.Fatalf("schedule edit prompt capacity error = %v", err)
	}

	_, err = store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Oversized schedule", Prompt: strings.Repeat("x", protocol.MaxTaskPromptBytes),
		Runtime: protocol.RuntimeCodex, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule: protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if !serviceErrorCode(err, "task_schedule_prompt_too_large") {
		t.Fatalf("schedule prompt capacity error = %v", err)
	}
}

func TestTasksMigrationLeavesOnlyFinalProductTables(t *testing.T) {
	store := newTestStore(t)
	for _, table := range []string{"tasks", "task_repositories", "runs", "sessions"} {
		var exists int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("final table %s exists = %d, err %v", table, exists, err)
		}
	}
	// The legacy tasks and runs tables were dropped before migration 030 reused
	// those names for the renamed Task and Run tables, so they are checked above.
	for _, table := range []string{"jobs", "definitions", "workflows", "automations"} {
		var exists int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&exists); err != nil || exists != 0 {
			t.Fatalf("legacy table %s exists = %d, err %v", table, exists, err)
		}
	}
}

func TestTaskDraftListAndRunContract(t *testing.T) {
	store := newTestStore(t)
	draft, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Draft review", Prompt: "Review the selected repositories later.", Runtime: protocol.RuntimeCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if draft.RepositoryCount != 0 || len(draft.Repositories) != 0 {
		t.Fatalf("draft repositories = %#v", draft)
	}
	page, err := store.Tasks(context.Background(), false, 10, "")
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("Task page = %#v, err %v", page, err)
	}
	if page.Tasks[0].Prompt != "" || page.Tasks[0].PromptPreview == "" ||
		page.Tasks[0].Repositories != nil || page.Tasks[0].RepositoryCount != 0 {
		t.Fatalf("Task list leaked detail or lost summary: %#v", page.Tasks[0])
	}
	if _, _, err := store.RunTask(context.Background(), draft.ID, protocol.RunTaskRequest{RequestKey: "draft-run"}); !serviceErrorCode(err, "task_has_no_repositories") {
		t.Fatalf("draft Run error = %v", err)
	}
	_, err = store.UpdateTask(context.Background(), draft.ID, protocol.SaveTaskRequest{
		Name: draft.Name, Prompt: draft.Prompt, Runtime: draft.Runtime,
		TimeoutSeconds: draft.TimeoutSeconds, ConcurrencyLimit: draft.ConcurrencyLimit,
		ExpectedGeneration: draft.Generation,
		Schedule:           protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if !serviceErrorCode(err, "task_repository_required") {
		t.Fatalf("scheduled draft error = %v", err)
	}
}

func TestOverviewHandlesFreshInstallAndRedactsUpcomingTasks(t *testing.T) {
	store := newTestStore(t)
	overview, err := store.Overview(context.Background())
	if err != nil || overview.WorkersOnline != 0 || overview.WorkersTotal != 0 {
		t.Fatalf("fresh Overview = %#v, err %v", overview, err)
	}
	if overview.RunMetrics.Window != "24h" || overview.RunMetrics.TotalRuns != 0 ||
		overview.RunMetrics.CompletionRate != nil || overview.RunMetrics.AverageQueueTimeSeconds != nil ||
		overview.RunMetrics.AverageCycleTimeSeconds != nil {
		t.Fatalf("fresh Overview run metrics = %#v", overview.RunMetrics)
	}
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	_, err = store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Private scheduled review", Prompt: "Do not expose this prompt on Overview.",
		Runtime:        protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	overview, err = store.Overview(context.Background())
	if err != nil || len(overview.UpcomingTasks) != 1 {
		t.Fatalf("scheduled Overview = %#v, err %v", overview, err)
	}
	summary := overview.UpcomingTasks[0]
	if summary.Prompt != "" || summary.Repositories != nil || summary.RepositoryCount != 1 {
		t.Fatalf("Overview leaked Task detail: %#v", summary)
	}
}

func TestOverviewReportsRunPerformanceForRunAdmittedInLastDay(t *testing.T) {
	store := newTestStore(t)
	base := time.Date(2026, time.August, 13, 9, 0, 0, 0, time.UTC)
	now := base
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Measured review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	measured, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "measured-run"})
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(20 * time.Second)
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "measured-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(30 * time.Second)
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "Retry this run.",
	}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(35 * time.Second)
	if _, err := store.HeartbeatWorker(context.Background(), worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetrySession(context.Background(), measured.Run.ID, measured.Sessions[0].ID); err != nil {
		t.Fatal(err)
	}
	now = base.Add(50 * time.Second)
	retryClaim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "retry-claim", LeaseToken: tokenA})
	if err != nil || retryClaim == nil {
		t.Fatalf("retry claim = %#v, err %v", retryClaim, err)
	}
	if _, err := store.StartAttempt(context.Background(), retryClaim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(60 * time.Second)
	if _, err := store.CompleteAttempt(context.Background(), retryClaim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "succeeded", Result: "Done.",
	}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(70 * time.Second)
	if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "active-run"}); err != nil {
		t.Fatal(err)
	}
	now = base.Add(90 * time.Second)
	overview, err := store.Overview(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metrics := overview.RunMetrics
	if metrics.Window != "24h" || metrics.TotalRuns != 2 || metrics.CompletedRuns != 1 {
		t.Fatalf("run metric counts = %#v", metrics)
	}
	if metrics.CompletionRate == nil || *metrics.CompletionRate != 0.5 {
		t.Fatalf("completion rate = %#v", metrics.CompletionRate)
	}
	if metrics.AverageQueueTimeSeconds == nil || *metrics.AverageQueueTimeSeconds != 20 {
		t.Fatalf("average queue time = %#v", metrics.AverageQueueTimeSeconds)
	}
	if metrics.AverageCycleTimeSeconds == nil || *metrics.AverageCycleTimeSeconds != 60 {
		t.Fatalf("average cycle time = %#v", metrics.AverageCycleTimeSeconds)
	}
}

func TestOverviewDoesNotFlagFailuresOlderThanOneDay(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Failing review", Prompt: "Fail this review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{RequestKey: "failed-review"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{RequestID: "failed-claim", LeaseToken: tokenA})
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, err %v", claim, err)
	}
	if _, err := store.StartAttempt(context.Background(), claim.Attempt.ID, protocol.StartAttemptRequest{LeaseToken: tokenA}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteAttempt(context.Background(), claim.Attempt.ID, protocol.CompleteAttemptRequest{
		LeaseToken: tokenA, State: "failed", Error: "expected failure",
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour)
	overview, err := store.Overview(context.Background())
	if err != nil || overview.NeedsAttention != 0 || overview.CompletedLast24H != 0 {
		t.Fatalf("stale failure Overview = %#v, err %v", overview, err)
	}
}

func TestEditingAndReenablingTaskPreservesBlockedPendingOccurrence(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Blocked review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule:      protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
	_, due, _, found, err := store.claimDueTask(context.Background())
	if err != nil || !found {
		t.Fatalf("claim occurrence = due %v, found %v, err %v", due, found, err)
	}
	admissionErr := conflict("task_repository_missing", "the frozen repository is unavailable")
	if err := store.finishTaskOccurrence(context.Background(), task.ID, due, false, admissionErr); err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Task(context.Background(), task.ID)
	if err != nil || blocked.Schedule.HealthStatus != "blocked" || blocked.Schedule.HealthCode != "task_repository_missing" {
		t.Fatalf("blocked Task = %#v, err %v", blocked, err)
	}
	updated, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: "Blocked review renamed", Prompt: task.Prompt, Runtime: task.Runtime,
		TimeoutSeconds: task.TimeoutSeconds, ConcurrencyLimit: task.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: blocked.Generation,
		Schedule: protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Schedule.HealthStatus != "blocked" || updated.Schedule.HealthCode != "task_repository_missing" ||
		updated.Schedule.PendingDueAt == nil || !updated.Schedule.PendingDueAt.Equal(due) {
		t.Fatalf("edited blocked Task = %#v", updated)
	}
	paused, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: updated.Name, Prompt: updated.Prompt, Runtime: updated.Runtime,
		TimeoutSeconds: updated.TimeoutSeconds, ConcurrencyLimit: updated.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: updated.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Schedule.Enabled || paused.Schedule.HealthStatus != "disabled" ||
		paused.Schedule.HealthCode != "task_repository_missing" || paused.Schedule.PendingDueAt == nil {
		t.Fatalf("paused blocked Task = %#v", paused)
	}
	resumed, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: paused.Name, Prompt: paused.Prompt, Runtime: paused.Runtime,
		TimeoutSeconds: paused.TimeoutSeconds, ConcurrencyLimit: paused.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: paused.Generation,
		Schedule: protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Schedule.HealthStatus != "blocked" || resumed.Schedule.HealthCode != "task_repository_missing" ||
		resumed.Schedule.PendingDueAt == nil || !resumed.Schedule.PendingDueAt.Equal(due) {
		t.Fatalf("resumed blocked Task = %#v", resumed)
	}
	if _, err := store.DiscardTaskOccurrence(context.Background(), task.ID,
		protocol.DiscardTaskOccurrenceRequest{PendingDueAt: due}); err != nil {
		t.Fatalf("discard resumed blocked occurrence: %v", err)
	}
}

func TestIncompatibleWorkerCannotReceiveOrClaimTaskRun(t *testing.T) {
	store := newTestStore(t)
	_, err := store.RegisterWorker(context.Background(), "legacy-worker", protocol.WorkerRegistration{
		Name: "Legacy Worker", WorkerVersion: "legacy", Runtime: protocol.RuntimeCodex,
		RuntimeVersion: "codex-legacy", Capacity: 1, Health: "healthy",
	})
	if !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("legacy registration error = %v", err)
	}
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE workers SET claim_protocol_version = 0 WHERE id = ?
	`, worker.ID); err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Protocol fence", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "protocol-fence",
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Sessions[0].State != protocol.SessionBlocked || detail.Sessions[0].AssignedWorkerID != "" {
		t.Fatalf("Run routed to incompatible Worker = %#v", detail.Sessions[0])
	}
	if _, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "legacy-claim", LeaseToken: tokenA,
	}); !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("legacy claim error = %v", err)
	}
}

func TestClaimReplayRequiresCurrentProtocolVersion(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 1, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Replay protocol fence", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
		RequestKey: "replay-protocol-fence",
	}); err != nil {
		t.Fatal(err)
	}
	input := protocol.ClaimRequest{RequestID: "replay-protocol-fence", LeaseToken: tokenA}
	claim, err := store.Claim(context.Background(), worker.ID, input)
	if err != nil || claim == nil {
		t.Fatalf("initial claim = %#v, err %v", claim, err)
	}
	if _, err := store.db.Exec(`UPDATE workers SET claim_protocol_version = 2 WHERE id = ?`, worker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Claim(context.Background(), worker.ID, input); !serviceErrorCode(err, "worker_upgrade_required") {
		t.Fatalf("incompatible replay error = %v, want worker_upgrade_required", err)
	}
}

func TestClaimScansPastFiftyIncompatibleBlockedSessions(t *testing.T) {
	store := newTestStore(t)
	worker := registerTestWorker(t, store, workerA, 100, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	sessionIDs := createQueuedTaskSessions(t, store, worker, 51)
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM executions`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE sessions
		SET state = 'blocked', blocked_reason = 'No compatible Worker.',
		    assigned_worker_id = NULL, required_runtime = ?
	`, protocol.RuntimeClaudeCode); err != nil {
		t.Fatal(err)
	}
	wantSession := sessionIDs[len(sessionIDs)-1]
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE sessions SET required_runtime = ? WHERE id = ?
	`, protocol.RuntimeCodex, wantSession); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), worker.ID, protocol.ClaimRequest{
		RequestID: "paged-blocked-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil || claim.Session.ID != wantSession {
		t.Fatalf("claim after incompatible prefix = %#v, err %v, want session %s", claim, err, wantSession)
	}
}

func TestClaimScansPastFiftyHealthyQueuedAssignmentsToReroute(t *testing.T) {
	store := newTestStore(t)
	repository := protocol.RepositoryRegistration{Key: "factory", RemoteIdentity: "github.com/owainlewis/factory"}
	claimingWorker := registerTestWorker(t, store, workerA, 100, repository)
	healthyWorker := registerTestWorker(t, store, "worker-b", 100, repository)
	offlineWorker := registerTestWorker(t, store, "worker-c", 100, repository)
	sessionIDs := createQueuedTaskSessions(t, store, claimingWorker, 51)
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE executions SET assigned_worker_id = ?
	`, healthyWorker.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE sessions SET assigned_worker_id = ?
	`, healthyWorker.ID); err != nil {
		t.Fatal(err)
	}
	wantSession := sessionIDs[len(sessionIDs)-1]
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE executions SET assigned_worker_id = ? WHERE session_id = ?
	`, offlineWorker.ID, wantSession); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE sessions SET assigned_worker_id = ? WHERE id = ?
	`, offlineWorker.ID, wantSession); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
		UPDATE workers SET last_heartbeat = 0 WHERE id = ?
	`, offlineWorker.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(context.Background(), claimingWorker.ID, protocol.ClaimRequest{
		RequestID: "paged-reroute-claim", LeaseToken: tokenA,
	})
	if err != nil || claim == nil || claim.Session.ID != wantSession {
		t.Fatalf("claim after healthy assignment prefix = %#v, err %v, want session %s", claim, err, wantSession)
	}
}

func createQueuedTaskSessions(t *testing.T, store *Store, worker protocol.Worker, count int) []string {
	t.Helper()
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Paged claim fixture", Prompt: "Review.", Runtime: protocol.RuntimeCodex,
		RepositoryIDs: []string{worker.Repositories[0].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		if _, _, err := store.RunTask(context.Background(), task.ID, protocol.RunTaskRequest{
			RequestKey: fmt.Sprintf("paged-claim-%03d", index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.QueryContext(context.Background(), `
		SELECT id FROM sessions ORDER BY admitted_at, id
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != count {
		t.Fatalf("created %d sessions, want %d", len(ids), count)
	}
	return ids
}

func TestFrozenOccurrenceRechecksPausedTaskBeforeAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		pause     func(*Store, protocol.Task, string) error
		errorCode string
	}{
		{
			name: "disabled",
			pause: func(store *Store, task protocol.Task, repositoryID string) error {
				_, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
					Name: task.Name, Prompt: task.Prompt, Runtime: task.Runtime,
					TimeoutSeconds: task.TimeoutSeconds, ConcurrencyLimit: task.ConcurrencyLimit,
					RepositoryIDs: []string{repositoryID}, ExpectedGeneration: task.Generation,
				})
				return err
			},
			errorCode: "task_schedule_disabled",
		},
		{
			name: "archived",
			pause: func(store *Store, task protocol.Task, _ string) error {
				_, err := store.SetTaskArchived(context.Background(), task.ID, protocol.SetTaskArchivedRequest{
					Archived: boolPointer(true), ExpectedGeneration: task.Generation,
				})
				return err
			},
			errorCode: "task_archived",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
			store.now = func() time.Time { return now }
			worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
				Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
			})
			task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
				Name: "Race guard " + test.name, Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
				RepositoryIDs: []string{worker.Repositories[0].ID},
				Schedule:      protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
			})
			if err != nil {
				t.Fatal(err)
			}
			now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
			_, due, snapshot, found, err := store.claimDueTask(context.Background())
			if err != nil || !found {
				t.Fatalf("claim occurrence = due %v, found %v, err %v", due, found, err)
			}
			if err := test.pause(store, task, worker.Repositories[0].ID); err != nil {
				t.Fatal(err)
			}
			_, _, admissionErr := store.admitTask(context.Background(), task.ID, "schedule",
				"schedule:race:"+test.name, &due, &snapshot, "")
			if !serviceErrorCode(admissionErr, test.errorCode) {
				t.Fatalf("paused occurrence admission error = %v", admissionErr)
			}
			if err := store.finishTaskOccurrence(context.Background(), task.ID, due, false, admissionErr); err != nil {
				t.Fatal(err)
			}
			page, err := store.RunPage(context.Background(), 10, "")
			if err != nil || len(page.Runs) != 0 {
				t.Fatalf("paused occurrence admitted Run = %#v, err %v", page, err)
			}
			paused, err := store.Task(context.Background(), task.ID)
			if err != nil || paused.Schedule.HealthStatus != "disabled" || paused.Schedule.PendingDueAt == nil {
				t.Fatalf("paused Task = %#v, err %v", paused, err)
			}
		})
	}
}

func TestDisablingTaskPausesFrozenOccurrenceUntilDiscard(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, time.August, 10, 8, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	worker := registerTestWorker(t, store, workerA, 10, protocol.RepositoryRegistration{
		Key: "factory", RemoteIdentity: "github.com/owainlewis/factory",
	})
	task, err := store.CreateTask(context.Background(), protocol.SaveTaskRequest{
		Name: "Paused review", Prompt: "Review the repository.", Runtime: protocol.RuntimeCodex,
		TimeoutSeconds: 3600, ConcurrencyLimit: 10, RepositoryIDs: []string{worker.Repositories[0].ID},
		Schedule: protocol.TaskSchedule{Enabled: true, Cron: "0 9 * * *", Timezone: "UTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, time.August, 10, 9, 1, 0, 0, time.UTC)
	_, due, snapshot, found, err := store.claimDueTask(context.Background())
	if err != nil || !found || snapshot.ScheduleCron != "0 9 * * *" || snapshot.ScheduleTimezone != "UTC" {
		t.Fatalf("frozen occurrence = due %v, snapshot %#v, found %v, err %v", due, snapshot, found, err)
	}
	paused, err := store.UpdateTask(context.Background(), task.ID, protocol.SaveTaskRequest{
		Name: task.Name, Prompt: task.Prompt, Runtime: task.Runtime,
		TimeoutSeconds: task.TimeoutSeconds, ConcurrencyLimit: task.ConcurrencyLimit,
		RepositoryIDs: []string{worker.Repositories[0].ID}, ExpectedGeneration: task.Generation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if paused.Schedule.Enabled || paused.Schedule.PendingDueAt == nil || paused.Schedule.Cron != "0 9 * * *" ||
		paused.Schedule.HealthStatus != "disabled" {
		t.Fatalf("paused Task = %#v", paused)
	}
	if err := store.AdmitDueTasks(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	page, err := store.RunPage(context.Background(), 10, "")
	if err != nil || len(page.Runs) != 0 {
		t.Fatalf("paused occurrence admitted Run = %#v, err %v", page, err)
	}
	discarded, err := store.DiscardTaskOccurrence(context.Background(), task.ID,
		protocol.DiscardTaskOccurrenceRequest{PendingDueAt: due})
	if err != nil || discarded.Schedule.PendingDueAt != nil {
		t.Fatalf("discard paused occurrence = %#v, err %v", discarded, err)
	}
	replayed, err := store.DiscardTaskOccurrence(context.Background(), task.ID,
		protocol.DiscardTaskOccurrenceRequest{PendingDueAt: due})
	if err != nil || replayed.Schedule.PendingDueAt != nil || replayed.ID != discarded.ID {
		t.Fatalf("replayed discard = %#v, err %v", replayed, err)
	}
}
